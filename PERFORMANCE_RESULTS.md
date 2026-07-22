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
