# Komari 性能重构结果

本文按 Todo 记录可重复的修改前后证据。K-001 固化完整服务端基线与回放环境；后续任务在相同硬件和命令下保留修改前后局部对照。

## K-001 服务端性能基线与虚拟 Agent 回放器

已增加六类基准：JSON/v2 报告解码、60/600 样本分钟聚合、256 行批写、2,000 行历史压缩、10 万行表上的范围查询，以及 10,000 点流量汇总。数据库基准使用 GORM logger 对真实执行的 SQL statement 计数，并与 `ns/op`、`B/op`、`allocs/op` 一起输出。

基线环境：Apple M4、macOS arm64、Go 1.26.5；固定输入和命令如下：

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
