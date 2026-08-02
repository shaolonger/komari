# Komari v1.4.3 Stability Hotfix Todo

## 根因与协议

- [x] **K-HF-001** 从生产日志还原 v3 短连接和独立 401 请求序列
- [x] **K-HF-002** 定位 durable ACK 与旧 Agent ACK 重放形成的反馈环
- [x] **K-HF-003** 固化 accepted/durable 双水位和 only-NACK-replays 契约

## 服务端止损

- [x] **K-HF-101** 每连接只发送正数且推进型 durable ACK
- [x] **K-HF-102** ACK 返回 additive `accepted_through` 水位
- [x] **K-HF-103** 保持旧 Agent 兼容并禁止重复 ACK 放大
- [x] **K-HF-104** 将 WebSocket 心跳窗口扩展为三个有界周期
- [x] **K-HF-105** 为所有遥测连接结束记录 UUID、connID 和真实原因

## 验收与发布

- [x] **K-HF-201** 定向反馈环、ACK 契约和心跳配置回归
- [x] **K-HF-202** 全仓 unit、race、vet 与 fuzz
- [x] **K-HF-203** 单核/受限内存、迁移、删除、安全与构建门禁
- [x] **K-HF-204** 与 Agent v1.4.2 的共享 schema 和跨仓契约门禁
- [ ] **K-HF-205** 提交、推送、v1.4.3 tag、GitHub Release 与资产验收
