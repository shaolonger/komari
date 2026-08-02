# Komari v1.4.3

v1.4.3 是 telemetry v3 批量掉线的稳定性热修复。它切断 v1.4.2 服务端与
v1.4.1 Agent 在“已接收但尚未持久化”窗口中的 ACK/重放反馈环，同时保留历史
数据只有落盘后才能确认的安全边界。

## 根因与修复

- 服务端继续只确认已经与历史行原子持久化的连续序列，不用内存 accepted 水位
  冒充 durable 水位，断电恢复语义不变。
- 每条 WebSocket 连接只发送正数且向前推进的 durable ACK；重复帧不再收到相同
  ACK。即使尚未升级的旧 Agent 仍把 ACK 错当成重放信号，也无法形成无限闭环。
- ACK 新增 `accepted_through` 信息字段，明确区分进程已接收和数据库已持久化的
  水位；旧 Agent 可安全忽略该新增字段。
- WebSocket 心跳改为 25 秒 Ping、75 秒 Pong 窗口，可容忍三个心跳周期的单核
  调度抖动；消息大小、写超时和每连接队列仍保持严格上限。
- 断线日志现在包含客户端 UUID、连接 ID 和底层结束原因，便于区分超时、远端关闭、
  慢消费者和连接替换。

## 兼容与安全

- telemetry v1/v2、旧主题、旧 Agent 的协议保持兼容；主题仓库无需修改。
- Token 仍只允许通过 `Authorization` 请求头传输，不恢复 URL Query Token，不降低
  TLS、鉴权、SSRF 或远程能力默认禁用策略。
- 401 表示该节点 Token 缺失、失效、过期/撤销，或反向代理未转发
  `Authorization`；本版本不会把无效凭据自动变成有效凭据。

## 验收

全仓 unit、race、vet、telemetry v2/v3 fuzz、跨仓协议、单核/128 MiB 回放、迁移
回滚、删除/鉴权安全矩阵和 lite 构建门禁通过。交叉契约已固定 durable/accepted
双水位与“仅 NACK 触发重放”规则。
