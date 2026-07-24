# Komari 性能重构结果

本文按 Todo 记录可重复的修改前后证据。K-001 固化完整服务端基线与回放环境；后续任务在相同硬件和命令下保留修改前后局部对照。

## K-001 服务端性能基线与虚拟 Agent 回放器

已增加六类基准：JSON/v2 报告解码、60/600 样本分钟聚合、256 行批写、2,000 行历史压缩、10 万行表上的范围查询，以及 10,000 点流量汇总。数据库基准使用 GORM logger 对真实执行的 SQL statement 计数，并与 `ns/op`、`B/op`、`allocs/op` 一起输出。

基线环境：Apple M4、macOS arm64、Go 1.26.4；固定输入和命令如下：

```sh
go test ./api/client -run '^$' -bench 'BenchmarkDecode' -benchtime=100000x -benchmem -count=3
go test ./utils -run '^$' -bench 'BenchmarkMinuteAggregation' -benchtime=1000x -benchmem -count=3
go test ./database/records -run '^$' \
  -bench 'Benchmark(BatchWrite|HistoryCompression|HistoryRangeQuery|TrafficSummary)$' \
  -benchtime=3x -benchmem -count=1
```

修改生产热路径之前的结果：

| 热路径 | 固定输入 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|---:|
| JSON v1 decode | 445 B | 3,679～3,861 | 464 | 12 | — |
| binary v2 decode + conversion | 199 B | 183.7～189.5 | 214 | 6 | — |
| minute aggregate | 60 reports / 2 GPU | 84,922～103,496 | 14,576～14,586 | 113 | — |
| minute aggregate | 600 reports / 2 GPU | 1,129,285～1,172,389 | 134,960～134,965 | 145 | — |
| batch write | 256 records | 1,564,222 | 834,293 | 6,485 | 1 |
| history compression | 2,000 raw rows | 11,315,403 | 5,576,005 | 114,198 | 270 |
| history range query | 2,401/10,000 node rows returned | 5,495,820 | 1,702,160 | 53,411 | 1 |
| traffic summary | 10,000 points / 15m buckets | 952,778 | 1,649,184 | 674 | — |

[`tools/replay`](tools/replay) 是受限的虚拟 Agent 回放器。它支持 HTTP/WS、节点数、上报间隔、持续时间或固定消息数，以及 `{index}` Token 模板。JSON 结果包含 attempted/succeeded/failed、bytes、reports/s、p50/p95/p99、采样数和运行期间峰值 heap。延迟样本默认最多 100,000、硬上限 1,000,000，节点数和单节点消息数也有硬上限，避免压测工具自身成为无界内存源。

```sh
# HTTP：每个节点使用独立 Bearer Token；Token 不进入 URL 或结果日志
go run ./tools/replay \
  -mode http \
  -endpoint http://127.0.0.1:25774/api/clients/report \
  -token-template 'token-{index}' \
  -nodes 1000 -interval 1s -duration 10m

# WebSocket 固定次数、适合 CI smoke
go run ./tools/replay \
  -mode ws \
  -endpoint ws://127.0.0.1:25774/api/clients/report \
  -token-template 'token-{index}' \
  -nodes 10 -reports-per-node 20 -interval 10ms
```

HTTP percentile 表示完整请求/响应延迟；WS percentile 表示有 write deadline 的消息发送延迟，因为当前报告协议不返回逐消息 ACK。工具只把凭据写入 `Authorization: Bearer` Header，连接错误使用脱敏文本，不回显 Token。

验收通过：HTTP/WS `httptest` 集成、配置边界和取消测试；专项 `-race`；仓库 `go test ./...`、`go vet ./...`；CLI 编译；所有 benchmark smoke。

## K-002 受控运行时指标与诊断入口

增加了固定 schema、仅使用原子计数器的低基数指标注册表，覆盖报告接收/拒绝/字节/耗时、flush 队列、批次/行数/重试、SQLite 操作/错误/耗时、压缩、历史查询、Agent WebSocket 连接/重连/慢消费者，以及 Go heap/goroutine。接收、GORM Trace、历史查询、压缩和 WebSocket 生命周期已接入；后续单写器和慢消费者实现直接使用同一固定 API。

指标 API 不接受调用方提供的 label 或名称，因此无法把 UUID、IP、URL、Token、Session、API Key 或脚本内容变成高基数/敏感标签。`GET /api/admin/metrics` 只在现有 Admin role 中间件之后可用，返回 `no-store` 的 Prometheus text。

pprof、CPU profile 和 runtime trace 默认不注册。只有显式设置 `--diagnostics` 或 `KOMARI_DIAGNOSTICS=true` 后，才在同一个 Admin role 边界下注册 `/api/admin/debug/pprof/*`；未认证请求仍返回 401，关闭状态即使 Admin 请求也返回 404。没有新增独立公网诊断监听端口。

指标热路径并行 benchmark（1,000,000 次固定样本，Apple M4）：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ObserveReport`（3 个原子计数 + 固定桶） | 69.7～153.8 | 0 | 0 |

验证包括：未认证 metrics/pprof 全部拒绝、诊断默认关闭、Admin metrics 可读且禁止缓存、输出敏感词/高基数字段扫描、16 个 goroutine 并发写入和采集；首次 race 运行发现并修复了复制原子桶的问题。最终专项 `go test -race`、仓库 `go test ./...` 和 `go vet ./...` 全部通过。

## K-101 有界、一次、类型化的报告解码

HTTP 上报现在先要求认证中间件提供 `client_uuid`，再用 `http.MaxBytesReader` 把 body 限制为 64 KiB；读取后仅对 `common.Report` 做一次 `json.Unmarshal`。删除了原先无界 `io.ReadAll` 后先解码 `map[string]interface{}`、再解码 `common.Report` 的路径。畸形/多值 JSON 返回 400，超过上限返回 413。

WebSocket 在升级前要求同一个认证身份，升级后立即设置 64 KiB read limit。JSON v1 使用一个静态 typed union 一次性解析 report/ping type 和 payload，不再先解析 type 再解析 report；binary v2 继续使用 K-104 的严格长度/schema decoder。HTTP 和 WS 都无条件用认证上下文覆盖消息内 UUID，Admin 身份没有目标 client identity 时不能代替 Agent 上报。旧 Agent 不发送 UUID、携带未知扩展字段或不协商 subprotocol 的行为保持兼容。

相同 445-byte v1 fixture、100,000 次固定 benchmark：

| 路径 | 修改前 ns/op | 修改后 ns/op | 修改前 B/op | 修改后 B/op | 修改前 allocs/op | 修改后 allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| HTTP JSON v1 | 8,611～8,748 | 3,747～3,770 | 5,504 | 464 | 138 | 12 |
| WS JSON v1 | 5,936～6,223 | 3,918～3,934 | 664 | 464 | 16 | 12 |

HTTP typed decode 约快 2.3×，分配字节减少 91.6%、分配次数减少 91.3%；WS decode 约快 1.5×，并消除重复反序列化。表中不含网络和有界 body buffer，确保只比较被替换的 decode 逻辑。

安全/兼容验证包括：无认证身份、UUID 伪造、畸形和尾随 JSON、64 KiB+1 HTTP/WS payload、v1 未知扩展、JSON report/ping typed union、v1/v2 golden 对照；两个 JSON fuzz target 本轮合计执行超过 21 万输入且无 panic/身份绕过。专项 race、仓库 `go test ./...`、`go vet ./...` 全部通过。

## K-102 分片快照与有界 minute accumulator

删除 `go-cache` 中每个 UUID 的可增长 `[]common.Report` 和 `Get → append → Set` 竞态，改为 256 个固定 shard。每次更新在单个 shard 锁内原子完成，不同 UUID 可并行；同 UUID 不会覆盖并发样本。每个节点状态只保留：

- 一份深拷贝所有引用字段的最新不可变 snapshot；
- 当前和至多一个尚未 drain 的分钟窗口；
- 每个 Record/GPU 指标的固定容量 min-heap，精确保留受支持窗口内 top 30% 所需候选；
- 120 samples/minute（2 Hz）和 64 GPU devices 的硬上限。

默认 Agent 1 Hz 在上限内保持现有 `AverageReport`/`AverageGPUReports` top-30% 结果逐字段精确一致。第 121 个同分钟样本返回明确错误，HTTP 映射为 429；第三个未 drain 窗口也显式失败，不会静默覆盖。最新 snapshot 即使达到聚合上限仍更新；过期 snapshot 和已 drain 空节点按原来一分钟 TTL 清理。

公开 recent API 和 JSON-RPC 保留原数组/字段结构，但数组现在只包含最新不可变 snapshot，不再把攻击者可控频率转换为整分钟原始 Report 列表。原始输入和返回值的 GPU slice 均深拷贝，调用方不能修改内部状态。

60 reports、2 GPU、完整 Add + snapshot ownership + drain + Record/GPU materialization，10,000 次固定 benchmark：

| 实现 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 原始切片 + 多字段重复排序 | 25,806～28,916 | 30,776 | 112 |
| 256-shard bounded accumulator | 12,680～15,972 | 7,352 | 9 |

新路径约快 1.6～2.3×，分配字节减少 76.1%，分配次数减少 92.0%；输入频率超过上限时内存保持常量，而旧路径继续线性增长。

验证覆盖：同 UUID 120 goroutine 并发、512 个跨 shard UUID、分钟边界、两个窗口 drain/第三窗口背压、GPU 多设备、120 样本/64 GPU 限制、source/returned snapshot 不可变性、TTL 清理，以及新旧 Record/GPU 完整结构对照。专项 race、仓库 `go test ./...`、`go vet ./...` 全部通过。

## K-103 有界批次与 SQLite 单写器

新增进程级 SQLite telemetry writer，Record、GPURecord 和完成鉴权/任务归属校验的 PingRecord 统一走一个后台写协程。队列默认 64、硬上限 1,024 batches；单批最多 100,000 rows；调用方 context 在满队列时形成明确背压，不创建每批 goroutine，也不静默丢弃。

writer 按最多 256 rows 构建和缓存 prepared multi-row INSERT，同一批的三类记录在一个 `database/sql` transaction 中提交；任何一种写入失败都会回滚整批。prepared statements 在进入 transaction 前建立，因此 `MaxOpenConns(1)` 下不会发生“事务占住唯一连接、Prepare 等待另一连接”的死锁。

SQLite `BUSY`/`LOCKED` 使用 10ms 起始的指数退避，默认最多重试 4 次；context 取消、schema/constraint 等永久错误不重试。重试耗尽会把错误返回给调用方。分钟 flush 保留失败批次并在下次调用重试，在失败未解决前不继续 drain 新窗口；内存由两分钟 accumulator 上限约束。Ping handler 同步等待 durable result，保持原先“成功即已写入”的语义。

优雅关闭顺序改为：停止 HTTP、停止 Nezha compatibility listener、关闭 Agent WebSocket、flush 当前部分分钟、向 writer 队列尾部发送 drain barrier，最后关闭 prepared statements。deadline 到期会取消当前 transaction 并返回/记录错误，不伪装成功。

与 K-001 相同的 256 Record 批次：

| 实现 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| GORM `Create` baseline | 1,564,222 | 834,293 | 6,485 | 1 |
| prepared single-writer | 1,359,453～1,497,863 | 480,465～480,858 | 2,088～2,089 | 1 |

端到端 Submit/queue/transaction 路径延迟改善约 4～13%，分配字节减少约 42.4%，分配次数减少约 67.8%；主要收益是消除多写者争用、提供原子混合批次和可证明的失败语义。

验证覆盖：300+300+Ping 原子提交、跨 chunk、注入两次 BUSY 后成功、永久 schema 失败整批回滚、满队列 context 背压、6 批 shutdown drain、关闭后拒绝、真实 `BEGIN IMMEDIATE` 锁释放恢复、32 个并发批次下 WAL 查询，以及任务归属验证后的 Ping writer。专项 race、仓库 `go test ./...`、`go vet ./...` 全部通过。

## K-201 不可变配置快照与主动发布

配置初始化/旧 schema 迁移完成后，一次性读取 `configs` 并通过 `atomic.Pointer` 发布不可变 snapshot。`Get`、`GetAs`、`GetMany`、`GetManyAs` 和 `GetAll` 的命中路径不再执行 SQL；标量直接读取，map/slice/struct 在交给调用方前深拷贝，Set 输入和 subscriber event 也由 snapshot 取得独立所有权，调用方不能反向修改全局配置。

所有 Set/SetMany/SetManyAs 和首次默认值写入由同一写锁排序，并在数据库 transaction/UPSERT 成功后复制旧 map、应用变更、一次原子替换 snapshot，随后发布事件。数据库失败时既不替换 snapshot，也不发布事件。并发首次默认值使用 `ON CONFLICT DO NOTHING` 和锁内二次检查，较晚的默认值不会覆盖已经存在的配置。SetManyAs 同时修复了把 `json:"name,omitempty"` 整串误用作 key 的问题。

`GetAs[int]` 固定 10,000 次 benchmark：

| 实现 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| 原逐次 GORM + JSON | 7,575～8,993 | 3,874 | 64 | 1 |
| atomic snapshot | 13.45～28.33 | 8 | 1 | 0 |

热读约快 267～669×，分配字节减少约 99.8%，并完全移除认证/中间件热路径的 SQLite 读锁竞争。

验证覆盖：5 种读取 API 各 1,000 次 SQL 计数为零、默认值只持久化/发布一次、订阅 old/new、注入事务失败不发布、输入/event/返回 map 不可变、JSON tag option、4 writers + 16 readers 并发和最终 DB/snapshot 一致性。专项 race、仓库 `go test ./...`、`go vet ./...` 全部通过。

## K-104 Agent 协议 v2 与 v1 兼容协商

服务端 WebSocket 现在按标准 subprotocol 显式协商：优先 `komari.telemetry.v2`，其次 `komari.telemetry.v1`；旧 Agent 或代理未携带/转发 subprotocol 时默认 JSON v1。只有成功协商 v2 的连接可发送二进制帧；v2 连接仍接受 Agent 在编码失败时发送的 JSON v1 text fallback。未知协议和 legacy 连接上的 binary frame fail closed，且 Token/控制能力仍由原认证链决定。

v2 帧使用 16-byte 固定 header、schema ID、精确 payload length、little-endian typed payload 和 64KB WebSocket/read/decode 硬上限。decoder 在分配 GPU slice 前检查计数，并拒绝未知版本/flag/schema、截断/尾随数据、非 UTF-8、非有限浮点、`used > total` 和原生整数溢出。

跨仓库 golden：[`protocol/telemetryv2/testdata/report_v2.hex`](protocol/telemetryv2/testdata/report_v2.hex)，与 komari-agent 的同路径文件逐字节一致。

验证命令：

```sh
go test ./api/client ./protocol/telemetryv2 -run '^$' \
  -bench 'BenchmarkDecode' -benchtime=100000x -benchmem -count=5
```

Apple M4/macOS、同一份 detailed GPU 语义 fixture：

| Decoder | 帧大小 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| JSON v1 | 445 B | 3,653～3,973 | 464 | 12 |
| Binary v2 + common.Report 转换 | 199 B | 178～191 | 214 | 6 |

v2 帧缩小约 55.3%，完整服务端解码/转换约快 19～22×，分配次数和字节分别减少 50% 与约 54%。协议纯 decoder 约 111～150ns（常态区间）、118 B、4 allocs。

正确性与安全验证包括：

- v1/v2 解码为相同 `common.Report`（含 detailed GPU）；
- Agent 生成、服务端消费同一 199-byte cross-repo fixture；
- 无 subprotocol/v1/v2/未知版本的协商和降级；
- 每一个截断前缀、magic/version/flag/header/length/schema 破坏、尾随数据和超限帧；
- fuzz seed 保证任意输入不 panic；
- uint64→int64/int 溢出、GPU fallback 和 malformed frame 回归；
- WebSocket read limit 在第一次读取前设置，不影响现有控制消息鉴权。

## K-202 API Key、客户端 Token 与 Session 安全缓存

新增总容量严格受限的 64-shard credential cache。Client Token 与 Session 只以 SHA-256 固定长度摘要作为 map key；缓存值只包含 UUID、版本、凭据过期时间和客户端吊销时间，不保存第二份明文凭据。每类缓存容量上限 16,384，positive TTL 为 5 分钟，negative TTL 为 10 秒；随机凭据喷射只会触发分片内近似淘汰，内存不会随输入基数无限增长。

数据库 miss 前记录全局单调 invalidation generation，查询完成后只在 generation 未变化时发布结果。Token 轮换、重发、吊销、客户端删除/创建以及 Session 创建、删除、批量删除、过期清理、账户删除和密码修改都在数据库事务成功后先推进 generation、再失效摘要项。因此，与轮换并发的旧查询不能在失效后把旧权限重新写回缓存；数据库失败也不会提前失效当前有效权限。

API Key 继续从 K-201 的不可变配置 snapshot 获取，不增加另一份缓存；验证改为分别 SHA-256 后对固定 32-byte digest 使用常量时间比较。短、等长和超长错误输入走相同固定长度 compare。认证缓存命中/未命中/失效指标只有固定无 label 原子计数，不含凭据、UUID 或 IP。

安全审查同时发现原 GORM debug logger 会把 SQL 参数插值到日志。logger 现实现 GORM `ParamsFilter`，所有 SQL 诊断只显示占位符，仍保留语句结构、耗时、行数和错误分类，但 Token、Session、密码、IP 与用户输入不会作为 SQL 实参进入日志。回归测试通过真实 GORM/SQLite 查询验证明文凭据不出现在输出中。

Apple M4、macOS arm64、Go 1.26.4，`-benchtime=200ms -count=3`：

| 热路径 | 修改前 ns/op | 修改后 ns/op | 修改前 B/op | 修改后 B/op | 修改前 allocs/op | 修改后 allocs/op | 修改前 SQL/op | 修改后 SQL/op |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Client Token → UUID | 6,468～6,553 | 117.7～118.2 | 5,779 | 0 | 81 | 0 | 1 | 0 |
| Session → UUID | 6,428～6,476 | 116.8～117.5 | 5,155 | 0 | 86 | 0 | 1 | 0 |
| API Key fixed-digest compare | — | 89.9～91.3 | — | 0 | — | 0 | 0 | 0 |

Client Token 和 Session 稳态认证均约快 54～56×，完全移除缓存命中时的 SQLite 访问、heap allocation 和明文凭据副本分配。API Key 的短、等长、超长错误输入 benchmark 均处于同一约 90ns 区间。

验证覆盖：摘要键结构扫描、严格容量上限、TTL/negative entry、stale generation 拒绝回填、10 万随机凭据、有/无命中、Token 过期/轮换/吊销/客户端删除、Session 过期自动删除/主动删除/密码变更、32 goroutine 并发失效、API Key 输入长度行为、GORM 实际日志脱敏和固定低基数指标。最终仓库 `go test ./...`、相关包 `go test -race` 和 `go vet ./...` 全部通过。

## K-203 Session 活跃信息合并与节流写回

认证请求不再同步执行 `UPDATE sessions`。新增容量固定为 16,384 的 activity tracker，以 K-202 同一个 32-byte SHA-256 Session 摘要作为唯一内存键；状态中不保留明文 Session。单槽 wake channel 合并通知，默认 5ms 内把 UA/IP 真实变化提交给后台 writer；相同 Session 的 `latest_online` 每 60 秒最多写回一次。相同 UA/IP 的高频请求只在锁内更新时间并进入 0 SQL、0 allocation 热路径。

tracker 每批最多 256 个 Session，在一个 SQLite transaction 中复用 prepared `UPDATE`。更新条件包含摘要索引和 `expires > now`，过期或已删除 Session 不会被活动写回重新激活。数据库失败时 dirty state 不清除，1 秒后重试；容量全部被未提交状态占用时返回明确背压错误，不会无界增长或静默覆盖。已成功写入且空闲 10 分钟的状态会清理，容量压力下只淘汰 clean state。

Session schema 增加唯一 `session_digest` BLOB、`expires` 索引和 `uuid` 索引。新 Session 创建时直接持久化摘要；旧数据库中的 Session 在第一次成功认证时懒回填摘要，因此升级不需要使现有登录全部失效。管理端删除、账户密码变更、账户删除和过期清理同时移除缓存/活动状态。`/api/me` 在生产中复用 Identity middleware 已认证的 UUID，避免同一请求再次认证 Session。

关闭顺序在停止 HTTP、关闭 Agent 连接和最终遥测 flush 之后，使用独立 5 秒 context drain 全部 Session activity batch，再关闭 prepared statement；超时和数据库错误明确返回并记录。固定低基数指标只记录 activity batch/rows/errors，不含 Session、UUID、UA 或 IP label。

Apple M4、macOS arm64、Go 1.26.4，`-benchtime=300ms -count=3`：

| 实现 | ns/op | B/op | allocs/op | 稳态 SQL/request |
|---|---:|---:|---:|---:|
| 原每请求 GORM `Updates` | 17,749～18,220 | 7,323～7,325 | 89 | 1 |
| 摘要 tracker coalesced touch | 62.03～62.19 | 0 | 0 | 0 |

稳态请求约快 285～294×，同时完全移除每请求 SQLite 写锁和 heap allocation。1,000 次相同 Session/UA/IP 触碰在专项测试中合并为 1 行；此后一分钟内保持 0 写，UA 变化立即产生 1 行更新，60 秒边界再产生 1 行 online 更新。

验证覆盖：写入次数、UA/IP 状态变化、时间不回退、过期 Session 拒写、旧 Session 摘要回填、三个索引、失败保留/重试、300 条跨 3 批退出 drain、deadline 取消、输入长度限制、容量耗尽/clean eviction、明文结构扫描、32 goroutine × 1,000 次并发触碰、真实 SQLite transaction 和 Identity context 复用。最终仓库 `go test ./...`、相关包 `go test -race` 和 `go vet ./...` 全部通过。

## K-301 SQLite 连接治理、版本迁移与复合索引

SQLite 不再把整个应用限制为一个通用连接。普通 GORM 查询使用按 `GOMAXPROCS` 调整、最多 8 条连接的 WAL read/general pool；K-103 遥测批写和 K-203 Session 活跃写回共用独立的单连接 writer pool，从结构上保持 SQLite 单写者，同时允许已有读事务与写事务并行。writer 使用 `BEGIN IMMEDIATE`，避免事务执行一半才升级锁。

所有新建连接都通过 driver `ConnectHook` 设置 `foreign_keys=ON`、5 秒 `busy_timeout`、`synchronous=NORMAL`、8 MiB page cache、memory temp store 和 256 MiB mmap；WAL 也写入 DSN。测试会同时固定多条连接逐一读取 PRAGMA，防止只配置连接池中的偶然第一条连接。相对旧配置，本次还真正启用了每连接外键检查；由此暴露并修正了 Ping、Session 和通知测试中的非法孤儿夹具。

新增仅追加的 `schema_migrations` 账本和 SQLite `user_version`。每条迁移有 SHA-256 checksum，在单个 transaction 中执行 DDL、写入版本记录并更新 `user_version`；重复启动幂等，迁移内容被修改、数据库版本高于程序、版本重复/乱序都会拒绝继续。回滚演练使用故意失败的第二条 DDL，验证已执行的第一条 DDL、迁移账本和版本号全部回滚。升级不会扫描或重写历史遥测行。

version 1 为 recent/long-term Record、GPU、Ping 和 Session 增加与实际过滤、排序相匹配的复合/唯一索引。10 万行 Record 固定范围查询、Apple M4/macOS arm64、三次结果：

| 索引 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 原分离 `client`/`time` 索引 | 1,289,003～1,337,755 | 824 | 21 |
| `(client,time)` 复合索引 | 5,380～5,396 | 824 | 21 |

同一 SQL 在不改变结果或内存分配的情况下约快 239～249×。`EXPLAIN QUERY PLAN` 测试同时锁定 Record、GPU、Ping 和 Session 查询使用目标索引，避免未来 schema 漂移造成静默退化。

验证覆盖：带空格绝对路径和共享内存 DSN、连接池上限、每连接 PRAGMA、WAL reader snapshot 与并发 writer、writer 外键约束、旧库数据保留升级、重复迁移、checksum/新版本拒绝、事务回滚、所有热查询计划和 10 万行 benchmark。仓库 `go test ./...`、涉及数据库与并发包的 `go test -race`、`go vet ./...` 全部通过。

## K-302 增量、分块、幂等的 Record/GPU 历史压缩

原实现每次把所有 4 小时前的 Record/GPU 行完整加载到内存，以拼接字符串作为 bucket key，对每个 bucket 先 `COUNT` 再逐条 `CREATE/UPDATE`，最后在一个长事务中删除全部 5 小时前的数据。新实现以结构化 `(client,slot)` / `(client,device,slot)` 为 key，只处理 4 小时稳定边界之前的完整 15 分钟 bucket，并将已完成边界写入 `telemetry_compaction_state`。每次重跑只扫描 high-watermark 前 1 小时到新边界，既避免重复遍历全部历史，又允许仍在 5 小时 raw 保留窗口内的迟到数据重新计算。

稀疏/常规数据按最多 24 小时、10 万 raw rows 的有界窗口处理；超过上限的密集窗口退化为每页 64 个 client/device group，仍保持内存上限。聚合结果依赖 version 2 migration 新增的唯一 bucket 索引，通过批量 `ON CONFLICT DO UPDATE` 写入，不再执行每 bucket 的存在性查询。Record 的 70%/网络速率 20% 分位数语义已用旧实现作为 oracle 逐字段对照；GPU 按 `(client,device_index,slot)` 聚合，设备名称取该窗口最后一个非空值。

删除使用每次最多 5,000 行的短事务。开始聚合前捕获本窗口 `max(rowid)`；聚合完成后把 `[bucket,bucket_end)`、rowid 上界写入 durable pending marker，再开始删除。新到达的行具有更大 rowid，不会被本轮误删。若进程在任意删除 chunk 后崩溃，重启先根据 marker 继续删除，不会用残余 raw 子集覆盖完整 aggregate；marker 清除与 monotonic watermark 更新在同一事务完成。

与 K-001 相同的 2,000 行、33 小时稀疏历史输入，单次结果：

| 实现 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| 原全表扫描/逐 bucket upsert | 11,315,403 | 5,576,005 | 114,198 | 270 |
| 增量窗口/批量 upsert | 10,329,125～15,095,751 | 4,638,672～4,642,576 | 97,610～97,645 | 27 |

新路径在相同量级的端到端耗时下把 SQL 数减少 90%，分配字节减少约 16.7%，分配次数减少约 14.5%；后续稳定运行只扫描 high-watermark 的一小时重叠窗口，而基线每次重新读取全部历史。额外的 1,000,000 raw rows、1,000 clients 固定基准为 9.177 秒（约 109k rows/s）、265 SQL；总 allocation 2.68 GB，但运行时单页最多 64 groups/10 万 rows，峰值不会随数据库总历史量无界增长。

验证覆盖：4/5 小时精确边界、partial bucket、重复执行、high-watermark、迟到数据重算、分块删除中途崩溃与恢复、GPU 多设备隔离、旧新 Record 语义对照、version 2 旧库去重/唯一索引升级和百万行 benchmark。仓库 `go test ./...`、Records/迁移专项 `go test -race`、`go vet ./...` 全部通过。

## K-303 多级聚合与独立保留

服务端的数据层次现在明确为：内存最新 snapshot（raw）、分钟 accumulator 持久化表（1m）、`records_long_term` / GPU 对应表（15m）、`records_hourly` / GPU 对应表（1h）。`SelectRecordTier` 根据窗口、点数预算和是否需要 live snapshot 选择能满足预算的最细粒度；K-402 查询规划器直接消费该选择结果。

version 3 migration 增加 hourly 表、复合唯一/时间索引和 `record_rollup_summaries`。1h builder 只读取已完成的 15m bucket，以独立 monotonic high-watermark 增量推进，并重算最近 1 小时以吸收 K-302 的迟到更新。每小时按最多 64 个 client 分页；GPU 页继承 K-102 的每节点 64 设备硬上限，所有读取和 UPSERT 均有界。崩溃发生在页提交后、watermark 前时只会幂等重做，不会产生重复行。

代表性指标继续使用兼容的分位数聚合；同时每个 15m/1h bucket 保存 sample count、首末累计上下行计数器、首末速率、精确层内流量、counter reset 次数，以及 CPU/GPU/load/温度峰值。合并 1h 时会把四个 15m bucket 内部流量相加，并补算相邻 bucket 首末样本之间的 delta/reset。因此 counter reset、总量和峰值不会因降低展示采样密度而消失。60 个分钟样本（含一次上下行 counter reset）对照测试中，1h summary 的 up/down/reset 与直接对原分钟序列执行 `SummarizeTrafficRecords` 完全一致。

保留策略彼此独立：raw/1m 工作集由 K-302 保持 5 小时；15m 默认保持 7 天；1h 保持到用户配置的最终保留边界。15m 删除前必须检查对应 1h stream 的 durable watermark；没有 watermark、构建落后或构建失败时宁可保留数据。所有层按 5,000 行短块删除，summary 与数据使用相同安全边界；用户明确配置的最终边界始终优先，因为边界之外本就不属于保留合同。

一年单节点范围读取（35,040 个 15m 点对 8,760 个 1h 点），Apple M4、三次固定结果：

| 层级 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| 15m | 122,816,833～126,084,541 | 55,562,080～55,566,816 | 1,086,491～1,086,565 | 1 |
| 1h | 30,775,750～31,349,000 | 14,988,888～14,990,608 | 315,621～315,641 | 1 |

1h 层将一年查询耗时降低约 74–76%（约 3.9–4.1×），分配字节降低约 73%，分配次数降低约 71%，同时保留 summary 中的总量/reset/峰值语义。

验证覆盖：raw/1m/15m/1h 预算选择、完整/partial hour、15m→1h 一致性、跨 bucket counter reset、峰值、GPU 多设备、迟到 overlap 重建、watermark 落后时禁止删除、watermark 推进后安全过期、version 3 旧库迁移和一年范围 benchmark。仓库 `go test ./...`、Records/迁移专项 `go test -race`、`go vet ./...` 全部通过。

## K-401 查询预算、字段投影与峰值保真降采样

新增一个供公开 REST、JSON-RPC 和后续管理历史接口共用的预算层。匿名调用默认/最大总点数为 4,000/20,000，最大窗口 366 天、最大节点数 10,000；现有 Admin 会话为 20,000/100,000、10 年、100,000 节点。时间反转、零/负预算、窗口/节点/点数越界均在查询前返回明确错误。旧 JSON-RPC 的 `maxCount=-1` 不再代表真正无界，而是映射到当前权限的硬上限。

公共 load、Ping、Traffic 和 JSON-RPC load/Ping 已接入同一预算。Traffic 的共享 series 与可选 per-node series 在执行节点循环前计算响应点数；超限直接拒绝。单节点 load 接口支持 `max_count`，默认即有界；GPU 设备共享同一总预算，并在每个 device series 内独立采样，避免一个设备挤掉其他设备。

`load_type` 不再先 `SELECT *` 后才过滤 JSON。固定 allowlist 将 CPU 映射为 `client,time,cpu`、RAM 映射为 `client,time,ram,ram_total`、network 映射为四个计数/速率列等；用户输入永远不拼入 SQL。真实 API 集成测试确认 CPU 请求执行 `SELECT client,time,cpu`，响应不含 RAM，同时 SQL 注入形态的 `load_type` 全部在数据库之前被拒绝。

原等距抽样替换为 Largest-Triangle-Three-Buckets。首点/尾点固定保留，面积按所请求指标计算；RAM/swap/disk 使用利用率，network 使用吞吐，GPU 设备以 utilization 计算。1,001 点中唯一 CPU=100 尖峰压到 50 点后仍保留且严格有序。10 万点压到 2,000 点的固定 benchmark：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| LTTB 100k → 2k | 1,666,304～2,297,885 | 1,128,038～1,128,039 | 1 |

这一次分配就是固定大小的 2,000 点结果数组；算法工作内存不随额外辅助结构增长。

验证覆盖：公共/Admin 精确窗口边界、默认/legacy unlimited/最大点数、节点上限、反向时间、所有投影白名单与注入输入、0/1/2 点预算、首尾/尖峰/排序、CPU API 真实投影与超限响应、GPU 多设备总预算和 100k 点 benchmark。仓库 `go test ./...`、Records/Public 专项 `go test -race`、`go vet ./...` 全部通过。

## K-402 多级表无重叠查询规划器

原来的 REST 和 JSON-RPC 各自查询 recent/long-term 表：long-term 查询覆盖完整窗口，recent 又覆盖最近窗口，随后依赖“如果 long-term 非空就把 recent 临时按 15 分钟分组”的条件逻辑。GPU 查询甚至在 long-term 失败时返回 recent + nil error。现在所有 Record/GPU 历史读取统一经过同一个规划器，所有 segment 使用严格半开区间 `[start,end)`；相邻 segment 的 `previous.end == next.start`，任何时间点只能属于一个物理表。

规划器结合 K-401 的 MaxPoints 与 K-303 tier：分钟预算选择 `1h(超出15m保留) → 15m(稳定历史) → 1m(最近稳定边界后)`；15 分钟预算把最近 raw 在内存中按 15 分钟聚合；小时预算直接使用 hourly 到完整小时边界，再只读取仍在 raw 保留内的最近尾部并按小时聚合。临时聚合继续使用兼容分位数，但会把请求指标的峰值覆盖回结果；CPU、GPU utilization/temperature 等尖峰不会在 coarse recent tail 中丢失。

所有 segment 错误立即带 table/range 上下文向上传播，不再把数据库故障伪装成部分成功。最终结果按 `(time,client[,device])` 排序并防御性去重。JSON-RPC 的单节点/全节点 load 和公开 REST/GPU 已删除各自的拼接实现并接入统一执行器；字段投影仍贯穿每个物理表。

固定一年/4,000 点规划（选择 hourly + recent raw tail）的纯规划成本：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PlanRecordQuery` | 94.66～114.4 | 192 | 1 |

验证覆盖：精确 4/5 小时切分、overlap 表中故意放置的冲突值、分钟/15m/1h 预算选择、长窗口 hourly、空表、跨节点排序与防御去重、缺表错误传播、America/New_York DST 回拨的 49 小时连续覆盖，以及 `EXPLAIN QUERY PLAN` 使用 hourly 复合索引。仓库 `go test ./...`、Records/Public 专项 `go test -race`、`go vet ./...` 全部通过。

## K-403 流量接口集合查询与流式聚合

公开流量接口删除了“先取可见节点、再为每个节点各执行一次完整 Record 查询”的 N+1 路径。新的集合查询复用 K-402 的严格半开区间规划，把所有 tier segment 合并为一条 `UNION ALL`，每段只投影 `client,time,net_in,net_out,net_total_up,net_total_down` 六个必要字段，并由 SQLite 按 `(client,time)` 输出。最多 256 个节点时使用参数化 `IN` 下推过滤；更大舰队为规避不同 SQLite 构建的 bind-variable 上限，执行一次全舰队窄列扫描并以授权 UUID 摘要集合做进程内过滤。非 nil 空授权集合直接返回且不触碰数据库，避免“无可见节点”误解释成“所有节点”。

聚合器逐行消费结果，任何时刻只保留当前一个节点的前一采样和统计状态；节点切换时立即完成结果，不物化完整 `[]Record`，也不同时保留 10,000 个中间 accumulator。计数器 delta、reset、无累计计数时的速率估算、覆盖率、峰值和 bucket 分摊与原 `SummarizeTrafficRecords` 使用同一逻辑。SQL 时间参数通过 `LocalTime` Valuer 归一化后再绑定，修复任意 offset 时间窗口与 SQLite timezone-less TEXT 表示直接比较时可能得到空结果的问题。请求 context 贯穿 `QueryContext` 和 row scan，取消会立即返回；K-401 的 per-node series 总点数预算仍在查询前执行。

10,000 节点、每节点两个采样、关闭 per-node series，Apple M4/macOS arm64、单次冷基准三次结果：

| 操作 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| 集合窄列扫描 + 流式聚合 | 28,508,292～29,618,958 | 15,152,272～15,152,464 | 309,646～309,650 | 1 |

耗时约 28.5～29.6ms（约 350k 节点/秒），SQL 次数恒为 1，而旧接口为每个可见节点一条查询，即 10,000 节点至少 10,000 条历史 SQL。剩余 allocation 主要来自 SQLite driver 对 20,000 个 TEXT/整数列值的 materialization 以及最终 10,000 节点响应 map，不再包含完整 Record 模型和无关指标字段。

验证覆盖：counter reset、采样缺口、累计计数缺失时速率回退、America/New_York offset 窗口、节点授权过滤、超过 SQL bind 阈值后的 Go 侧过滤、空授权集合零查询、context 取消、与旧聚合器逐字段对照、SQL 次数和 10k 节点 benchmark。仓库 `go test ./...`、Records/Public 专项 `go test -race`、`go vet ./...` 全部通过。

## K-404 舰队报告、负载/流量通知与 Ping 统计集合化

新增通用的授权节点集合查询：K-402 规划出的多个物理 tier 被拼成一条参数化 `UNION ALL`，字段仍由 K-401 固定 allowlist 决定，并按 `(client,time)` 排序返回。nil client set 明确表示内部全量查询，非 nil 空集合表示无授权节点且执行 0 SQL。最多 256 个节点时把 `IN` 下推 SQLite；更大集合只扫描窄时间窗口并在进程内再次按授权集合过滤，从而在不同 SQLite bind-variable 配置上仍保持一次查询且不泄漏非请求节点。Ping 提供对应的窄列集合查询，只读取 `client,task_id,time,value`，继续通过 `INNER JOIN ping_tasks` 隐藏旧库孤儿记录。

四条业务链路完成集合化：

- 舰队报告先用一次 Record 集合查询和一次 Ping 集合查询构建两个 per-client map，再复用原有纯统计逻辑；完整报告连同一次 client metadata 读取恒为 3 条 SQL，替代原 `1 + 2N`。
- 相同 interval 的负载通知不再为每个 task、每个 client 重读历史；一个调度 tick 只读取一次 client metadata、一次该 interval 的 Record 集合。RAM/swap/disk 百分比从预取 metadata map 计算，删除了每条 Record 内的 Client 查询；已执行任务的 `last_notified` 也合并为一次集合 UPDATE。
- 日/周/月流量通知先按 cadence 分组，每个 cadence 使用 K-403 一次性统计全部到期节点，成功投递节点再用一次集合 UPDATE 标记。无论节点数多少，一个 tick 最多为 notification/client 读取各 1 次，加 3 次流量读取和 3 次写回。
- Dashboard JSON-RPC 先读取一次 Ping task，再把所有节点缓存 miss 合并为一条 Ping 查询。task assignment 预编译为 `client → task IDs` 集合，消除原 records × tasks 内层线性查找；每节点缓存键和 1 分钟 TTL 兼容不变。

1,000 节点 × 10 个同 interval CPU 任务，共 10,000 次 task/client 判定，Apple M4/macOS arm64、单次冷基准三次结果：

| 操作 | ns/op | B/op | allocs/op | SQL/op |
|---|---:|---:|---:|---:|
| client 集合读取 + Record 集合读取 + 10k 判定 | 15,662,250～18,915,917 | 28,872,840～28,883,768 | 107,585～107,663 | 2 |

端到端约 15.7～18.9ms；旧实现仅 Record 查询就需要 10,000 条 SQL，百分比指标还会按每条 Record 再查询一次 Client，新实现稳定为 2 条读取 SQL。该基准保留完整 Client 模型是为了通知模板兼容，主要 allocation 来自 1,000 个完整节点模型和 GORM/SQLite materialization，不再随 task × client 乘积产生数据库往返。

验证覆盖：集合结果与逐节点旧查询/旧纯统计逐字段对照、CPU/RAM 阈值、ratio/cooldown、无数据节点、隐藏节点在全局运维报告与显式通知中的正确保留、授权集合过滤、超 bind 阈值过滤、空授权集合零查询、孤儿 Ping 排除、Dashboard Ping avg/p50/p99/tail/loss/latest 兼容、缓存命中零查询、取消、SQL 次数和 10k assignment benchmark。仓库 `go test ./...`、Records/Tasks/Notifier/JSON-RPC 专项 `go test -race`、`go vet ./...` 全部通过。

## K-405 权限感知历史缓存与流式 JSON

新增进程内历史响应缓存，专门缓存小型、已经编码完成的公共 REST/JSON-RPC 历史结果。cache key 包含 endpoint schema version、public/admin 权限域、完整查询参数、有效点数预算以及相对窗口的分钟 bucket；内存中只保留 key 的 SHA-256 digest。缓存最多 256 项、总计 32 MiB、单项最多 1 MiB，REST 只有 Record+GPU 合计不超过 1,000 点才尝试写入。外部响应始终设置 `Cache-Control: private, no-store`，防止浏览器或共享代理跨登录状态复用；内部 `X-Komari-History-Cache` 只暴露 hit/miss，不含 key 或身份。

每次读取先捕获单调 data/visibility generation。SQLite 单写器只有在包含 Record/GPU/Ping 的事务 durable commit 后才推进 generation；直接 Record/GPU 写入、历史删除、保留、压缩、Ping task/record 变化、Client 创建/删除/隐藏/配置更新也主动失效。失效会原子推进 generation 并清空所有项，查询期间发生失效时 `PutIfGeneration` 拒绝旧结果回填。匿名 REST 即使 cache hit 前仍重新执行隐藏节点门禁；public/admin key 物理隔离。节点被隐藏后已有公开缓存立即不可达且请求返回原有错误，重新公开后第一条请求使用新 generation。

公共 Record handler 不再用 background context 查询，Record/GPU 规划器都继承请求 context。1,000 点以上不再让 Gin 对整个 response 二次 `json.Marshal`；编码器使用固定 32 KiB buffer，逐个 Record、GPU device 和 GPURecord 写出，任何时刻只额外保留一个 point 的 JSON。慢客户端形成自然写背压，disconnect/deadline 会在下一 chunk 终止。JSON-RPC 因协议需要完整 response framing，仍由 transport 统一编码，但 1 MiB 以下 result 可复用权限隔离的已编码 `json.RawMessage`。

Apple M4/macOS arm64 实测：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 512 KiB cache hit | 60.27～61.05 | 0 | 0 |
| 原整包 `json.Marshal` 100k Record | 52,526,167～53,449,750 | 104,205,408～104,208,576 | 400,055～400,110 |
| 32 KiB bounded stream 100k Record | 58,597,750～65,413,875 | 53,749,128～53,764,744 | 700,050～700,201 |

流式路径为有界峰值内存付出约 10～23% 编码 CPU，累计分配字节减少约 48.4%，并消除约 50 MiB 级整包 output buffer；它不会把 100k point 响应放入缓存。缓存命中读取 512 KiB payload 约 60～61ns、零 allocation，随后直接交给 ResponseWriter，完全跳过 SQLite、降采样和重新编码。

验证覆盖：public/admin 隔离、参数隔离、旧 generation 拒绝回填、Client hidden/unhidden 主动失效、新 telemetry durable commit 失效、严格 entry/总内存上限、明文 key 结构扫描、2,000 点流式 JSON 完整性、最大 32 KiB writer chunk、慢客户端 deadline、并发 get/put/invalidate race、100k point stream/marshal 对照和 cache hit benchmark。仓库 `go test ./...`、Cache/Writer/Clients/Records/Tasks/Public/JSON-RPC 专项 `go test -race`、`go vet ./...` 全部通过。

## K-501 Context 调度器、稳定抖动与有界 Worker

新增通用周期调度引擎：一个 min-heap 保存全部 task 的 next-run，一个 timer 只等待最近任务，不再为每个 interval 创建 ticker/goroutine。task key 经过 FNV-1a 得到 interval 内的稳定 phase；相同配置在 reload/restart 后保持相同相位，不同 key 均匀分散。到期任务进入固定容量 channel；队列满时 dispatcher 形成 context 可取消的背压，不创建补偿 goroutine、不静默丢任务。默认 8 worker/256 queue，构造器硬限制最多 64 worker/65,536 queue；panic 被隔离在对应 worker job，调度循环继续运行。落后多个周期时显式 coalesce 到下一未来时刻，避免恢复后追发风暴。

Ping 调度从“每 interval 一个 goroutine，到点后每 task 再开 goroutine并遍历所有节点”改为 `(ping_task_id,client_uuid)` 独立 schedule。稳定相位把同一个大任务的节点下发均匀铺到整个秒级 interval；连接通过新增 O(1) 单节点只读 lookup 获取，不再为每个 job 复制在线连接 map。执行固定为 16 workers、2,048 queue；重复 client assignment 在建表时去重，取消在写入前检查。

Load notification 按 K-404 的 interval 集合批处理作为一个 schedule，固定 4 workers、64 queue；发送不再另开无界 goroutine，因此慢 provider 最多占用有限 worker。每次 Reload 原子替换 scheduler generation 并取消旧 context；Stop 有 deadline 且幂等。根 maintenance、流量阈值、流量报告、舰队报告和到期续费检查全部接入 server shutdown context；三种每分钟通知使用不同稳定 key，不再在整分钟同一时刻齐发。shutdown 在 HTTP/数据库 drain 前先取消根 context并停止 Ping/Load generation。

Apple M4/macOS arm64，10,000 个 task key 的稳定 phase 计算：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 10k `StableNextRun` | 461,971～565,412 | 0 | 0 |

10,000 个 next-run 只需约 0.46～0.57ms、零 heap allocation。1,000 个固定 key 在 60 秒 interval 内占据超过 990 个不同微秒 phase；worker 压力测试在 40 个 5ms 周期任务、4 槽队列下实测并发从未超过配置的 3。

验证覆盖：phase 确定性/范围/分布、重复 key/worker/queue 上限拒绝、满队列背压、最大并发、context 取消、panic 隔离、连续 50 次 Ping reload、连续 50 次 Load reload 的所有旧 generation 退出、Stop 幂等、expire 长 timer 取消，以及相关 `go test -race`。仓库 `go test ./...` 与 `go vet ./...` 全部通过。

## K-502 WebSocket 生命周期、Deadline 与慢消费者隔离

所有长期 WebSocket 链路现在共享同一个资源受限连接实现：Agent 上报为 1 MiB read limit、11 秒 pong/read deadline、5.5 秒 server ping、128 项发送队列；Dashboard 请求限制为 4 KiB/8 项队列；JSON-RPC 为 1 MiB/32 项；Terminal 双向帧为 256 KiB/128 项。默认连接使用 1 MiB read limit、60 秒 pong deadline、25 秒 ping 和 10 秒 write deadline。每次读都会刷新 idle deadline，pong handler 也刷新 deadline；半开 TCP 在 heartbeat 写失败或 pong/read 超时后退出，不再无限占用连接和 goroutine。

每个连接只拥有一个 writer goroutine，所有 data frame 和 heartbeat 都由它串行写入 Gorilla WebSocket。调用方只把已经复制/编码的不可变 payload 放入有界 channel，不再共享写锁等待网络。队列满时立即、无阻塞地标记 `komari_websocket_slow_consumers_total`，关闭底层 transport 并返回 `ErrSlowConsumer`；慢客户端不能占用其他连接的 writer，也不会为每条 JSON-RPC response 创建 goroutine。显式正常关闭最多等待 250ms 发送 Close frame；队列溢出或底层写超时直接关闭 transport，避免 Close control frame 与已卡住 writer 争锁后再次阻塞。只有“发送最后一条提示后马上关闭”的 Terminal 离线/超时路径使用有 write deadline 的同步 flush。

Agent 重连改用 O(1) 单连接 lookup 并同步关闭旧连接；旧 handler 的 defer 继续通过指针条件删除，绝不会删除新连接。`Close` 由 `sync.Once` 统一状态机保证幂等，并发发送在关闭后得到稳定错误。Terminal 的 Browser/Agent 指针增加 session 级 RWMutex、原子 attach 和幂等双端关闭；session map 读取/条件删除全部受锁，修复 close handler、30 秒 timer 和双向 forward 并发访问造成的数据竞争，同时修复空 Text frame 的切片越界。

确定性验证覆盖：writer 被人为永久阻塞时 1 项队列饱和，第三次发送在 100ms 预算内立即断开；server ping 失败模拟半开连接；read limit、初始/read-pong deadline、write deadline；100 个并发 Close 只触发一次底层 Close；关闭后发送；旧连接 cleanup 不删除重连替代连接。已有真实 WebSocket 集成测试继续验证超过 telemetry v2 最大帧立即断开。仓库 `go test ./...`、完整 `go test -race ./...`、`go vet ./...` 全部通过。

## K-503 Dashboard Snapshot/Delta/Sequence 与快照所有权

Dashboard 状态现由单一 RWMutex 下的不可变 store 管理。写入 `common.Report` 时立即复制值、GPU 指针及 device slice；所有公开读取再次深拷贝，因此调用方清空 UUID、修正展示值或修改 GPU 数组都不可能污染随后请求、通知或其他用户的结果。旧 `/api/clients` 客户端继续发送 `get`/`get <uuid>` 并收到完全兼容的全量结构，但实现已经从安全快照读取，修复了旧 handler 的 `report.UUID = ""` 直接修改全局报告这一共享所有权错误。

新客户端发送 `{"type":"subscribe","since":0}` 后收到带单调 `sequence` 的 `snapshot`，随后只在状态变化时收到合并后的 `delta`。delta 分开表达 `data`、`removed`、`online`、`offline`，并携带 `from_sequence`/`sequence`；`uuid` 可限制单节点。客户端带最后 sequence 重连时，在日志保留范围内直接续传 delta；sequence 超前或落后于日志则自动返回 `resync:true` 的当前 snapshot，也可显式发送 `{"type":"resync"}`。每条连接固定一个 read pump，报告更新使用 close-and-swap 通知 channel，状态读取与 wait channel 在同一把锁内捕获，不会在“读完状态、开始等待”之间丢 wakeup。

变更日志只记录 `(sequence, uuid, kind)`，不重复保留大 Report；容量固定 16,384，使用 O(1) circular overwrite，内存上限与运行时间无关。同一节点在客户端调度延迟期间的多次上报被合并为最终状态。按连续 sequence 直接计算 ring offset，正常单 delta 不扫描完整日志。snapshot/delta 都先应用登录权限、hidden 节点与单节点过滤，再清除 report 内 UUID；匿名用户不会在报告、上下线或删除事件中看到 hidden UUID。

Apple M4/macOS arm64，store 已包含 10,000 个节点：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 10k 深拷贝 snapshot | 1,295,989～1,390,314 | 4,276,935～4,276,945 | 30,034 |
| 10k 舰队中的单节点写入 + delta | 308.2～312.1 | 656 | 4 |

单节点增量约 0.31µs，CPU 相比每次重建 10k snapshot 下降约 4,200 倍，单次分配字节下降约 6,500 倍；完整 snapshot 仍保留严格所有权，且只用于首次订阅或 sequence 恢复。验证覆盖输入/输出深拷贝、嵌套 GPU slice、snapshot/delta 顺序、通知无丢失、删除/离线、journal overflow 自动恢复、匿名/admin/单节点权限过滤和 10k benchmark。仓库 `go test ./...`、完整 `go test -race ./...`、`go vet ./...` 全部通过。

## K-504 HTTP Server 资源超时与生产日志策略

生产 `http.Server` 不再使用全部为零的默认资源边界：`ReadHeaderTimeout=5s` 单独抵御 slowloris，完整 request read/write 各限制 5 分钟，keep-alive idle 为 90 秒，request header 最多 64 KiB。较长 read/write 预算允许受 100 MiB 上限保护的备份上传下载完成，同时所有连接仍有确定上界。WebSocket upgrade 后由 K-502 更短的逐连接 deadline 接管。MJPEG 是唯一有意保持的长响应，它不再受 5 分钟绝对 deadline 截断，而是在每个 2 秒 frame 开始时设置滚动 15 秒 write deadline；任意慢读客户端无法在一个 frame 内完成时立即退出。

Gin access log 改为结构化固定字段，不再把 `RawQuery` 拼入 message：只记录 method、无控制字符且限长的 path、status、latency、remote IP，以及布尔意义的 `query=<redacted>`。Authorization、Cookie、OAuth code/state、token 和任意 query value 从未进入记录。handler error 只记录数量，不复制可能包含用户输入/凭据的错误字符串；panic 保留请求、状态和 panic 类型，但不记录可能含密钥的 panic value。SQL logger 继续使用既有参数过滤，安全/审计业务日志路径未改变。

成功的 Agent report、ping result 和 task result 使用单调 counter 确定性保留首条及每 256 条中的一条，减少约 99.6% 高频 access-log 格式化、锁竞争和日志 I/O；所有 4xx/5xx、Gin errors、普通 API、连接建立/结束与审计事件 100% 保留。采样在 handler 返回后决定，失败永远不会因高频 endpoint 被漏掉。

验证覆盖：真实 TCP 只发送半个 header 时在 40ms 测试 deadline 后由 server 主动释放；生产 timeout/MaxHeaderBytes 精确值；活跃请求在 graceful shutdown 中完整 drain；25 条成功上报按 1/10 策略精确保留 3 条；5 条 401 全部保留；query/Header/panic 三类秘密均不出现在捕获日志。仓库 `go test ./...`、Cmd/Log/Public 专项 `go test -race`、`go vet ./...` 全部通过。

## K-505 静态资源 Manifest、ETag、Immutable 与预压缩

静态服务不再对每个请求执行 `os.Stat`、`os.ReadFile` 和 MIME 推断。默认嵌入主题与本地主题在首次使用、上传或切换时构建不可变 manifest；本地文件在构建时复制到受管理内存，之后的并发请求只读取不可变 map/byte slice。自定义主题 manifest 以浅共享不可变默认资源实现逐文件 fallback，覆盖 identity 时会主动丢弃默认主题的 `.br/.gz` sidecar，避免编码内容与新 identity 不一致。主题上传/切换会先完成 manifest 重建再返回成功，删除会立即失效缓存。

manifest 对单文件硬限制 5 MiB、单主题含生成压缩数据最多 64 MiB；全局最多缓存 8 个主题、128 MiB，自定义主题按构建顺序淘汰且默认 fallback 固定保留。现有上传安全门禁更严格地把 ZIP 解压内容限制在 30 MiB，因此正常主题始终落在 manifest 上限内。构建期间拒绝 symlink、反斜杠、NUL、绝对路径、`..`、非法主题 ID，并在 stat 后再次检查实际读取字节，防止 TOCTOU 扩容；路径安全比旧的纯 lexical `filepath.Rel` 更强。

每个 representation 使用 SHA-256 强 ETag。带 8 位以上构建 hash 的资源返回 `public,max-age=31536000,immutable`，其他静态文件为 5 分钟 revalidate；匹配 `If-None-Match` 直接 304。文本/JS/JSON/XML/SVG 在 manifest 构建时一次性生成不大于 4 MiB、且确实缩小体积的 `gzip.BestCompression`；主题自带 `.br` sidecar 时支持 Brotli。`Accept-Encoding` 完整处理 q 值和 wildcard、优先质量更高的 representation，每种编码拥有独立 ETag，并设置 `Vary: Accept-Encoding` 与 `nosniff`。

动态 index 使用内容 ETag、主题、站点名、描述和 system/admin 模式的摘要作为缓存键，最多保留 64 个已渲染 HTML；命中时不再执行字符串替换或重新哈希。站点名和描述在注入前 HTML escape，自定义 HTML/JS 仍保持禁用。HTML 返回 `no-cache` 强 ETag，主题或配置变化自然切换缓存 generation；Admin 导航注入也复用同一生成缓存。favicon 同样获得强 ETag，旧主题 fallback、SPA 和 `/themes/:id/*path` 路由保持兼容。

Apple M4/macOS arm64，manifest 已热且资源带 Brotli/Gzip 两种 variant：

| 操作 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 主题校验 + manifest asset lookup | 43.45～44.60 | 0 | 0 |

验证覆盖：Brotli/Gzip/identity 协商与 q 值、Gzip 解压内容、variant ETag、304 空响应、immutable、Vary、主题 fallback、identity 覆盖不继承错误 sidecar、文件修改前后显式失效、HTML 仅生成一次、主题/内存硬上限、路径穿越、非法 ID、symlink 逃逸及缓存并发 race。仓库 `go test ./...`、Public/Admin 专项 `go test -race`、`go vet ./...` 全部通过。

## K-601 遥测/控制存储契约与 SQLite 适配器

新增数据库中立的 `TelemetryStore` 与 `ControlStore`。遥测契约定义原子 batch write、Record/GPU range query、显式 resolution aggregate、多级 retention、health 和 bounded close；控制契约定义 Client Token、Session、User 强一致读取、迁移、health 和 close。接口模型只使用领域结构与 context，不暴露 `*gorm.DB`、SQL 方言、DSN 或连接池。后端注册使用原子发布与可恢复安装，读取没有全局互斥锁。

SQLite telemetry adapter 直接组合既有的单连接有界 writer、分层查询规划器、聚合语义和多级保留；不会另造一套容易漂移的 SQL。生产启动完成 schema migration 后安装 SQLite control/telemetry adapter。分钟遥测、Ping、兼容 `SaveClientReport`、Record/GPU 辅助入口、公开历史范围读取和保留任务都优先通过契约；未安装时保留原 SQLite 路径，仅用于旧测试和维护兼容。shutdown 通过统一 storage lifecycle drain writer。

认证缓存命中路径不变；cache miss 现在向 `ControlStore` 回源。Client Token 的过期/吊销字段、Session expiry 与 User 都来自强一致后端。Session 继续优先以 SHA-256 digest 查询；旧记录只在首次成功认证时用原 token 完成一次兼容查找并回填 digest。跨后端统一 `ErrNotFound` 在旧 API 边界映射为原有 GORM not-found，因此外部错误和安全语义不变。health 响应不含地址、DSN、凭据或 TLS 配置。

可复用的 adapter contract suite 覆盖：

- Record/GPU 原子批写、范围查询与请求指标峰值聚合；
- retention、health、context 取消和 32 路并发写；
- Client/Session/User 真相源、not-found、重复迁移、取消和 64 路并发读；
- SQLite 缺失 GPU 表时 Record+GPU 整批不产生部分提交；
- 真实 raw 过期删除但保留新数据、关闭连接健康失败；
- legacy Session digest 回填及空凭据拒绝。

仓库 `go test ./...`、完整 `go test -race ./...`、`go vet ./...` 和 `git diff --check` 全部通过。
