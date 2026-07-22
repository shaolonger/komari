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
