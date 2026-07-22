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
