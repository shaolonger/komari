# Komari 性能重构结果

本文按 Todo 记录可重复的修改前后证据。完整服务端基线、回放环境和 percentile 将由 K-001 补齐；各任务在相同硬件和命令下做局部对照。

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
