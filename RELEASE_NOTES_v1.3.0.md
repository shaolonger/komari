# Komari v1.3.0

v1.3.0 完成了服务端从遥测接收、实时状态、SQLite 写入与历史查询，到可选规模化存储和发布供应链的系统性性能重构。默认部署仍然是单文件、SQLite 优先的 Lite 模式；安全、兼容性与数据正确性不是性能交换项。

## 性能与可扩展性

- 增加有界、单次、类型化的 HTTP/WebSocket 报告解码，以及经过显式协商的二进制遥测协议 v2；JSON v1 继续完整兼容。
- 用分片不可变快照和原子 minute accumulator 取代存在并发覆盖风险的通用缓存复合更新。
- SQLite 遥测改为有界单写器、批量 prepared SQL、有限重试和 shutdown drain；增加热路径组合索引、增量历史压缩与 raw/1m/15m/1h 多级聚合。
- 历史、流量、Ping、舰队报告和通知查询改为集合化查询，统一请求预算、字段投影、峰值保真降采样和权限感知缓存。
- Dashboard 改为 snapshot/delta/sequence 模型；调度器、WebSocket 生命周期、慢消费者隔离、HTTP 超时、日志采样和静态资源 manifest 均有明确资源上限。
- 静态资源支持强 ETag、条件请求、immutable cache、预生成 gzip/Brotli representation 和动态 index generation cache。
- 新增存储契约：Lite 构建继续使用 SQLite；`scale` 构建可显式选择 PostgreSQL 强一致控制面和 ClickHouse 遥测面。
- 虚拟 Agent 回放器新增确定性 `-ramp-up`，可复现真实 Agent 的重连抖动，同时保留零 ramp 的极端连接风暴探针。

## 安全与兼容

- Token/Session/API Key 热缓存只保存摘要键，并在轮换、吊销、删除和权限变化后主动失效。
- 请求体、WebSocket 帧、查询窗口、返回点数、并发、队列、主题文件和缓存均有硬限制。
- PostgreSQL/ClickHouse 是显式 opt-in；连接失败不会静默降级到可能过期的 SQLite 权限状态。
- TLS、主题路径、上传解压、SSRF、管理诊断入口、慢客户端和日志脱敏边界保持或加强。
- SQLite v1.2.13 物理数据库可原地迁移至 schema v3；升级前快照可由 v1.2.13 直接启动，实现可验证回滚。
- v1.2.8 Agent → v1.3.0 Server 使用 v1；v1.3.0 Agent → v1.2.13 Server 自动回退 v1；新 Agent 与新 Server 协商 v2。

## 构建与供应链

- Go、Node、Zig、前端 commit/lockfile、GitHub Actions 和容器基底均固定到可审计版本或完整摘要。
- 发布构建启用 `-trimpath`、稳定元数据、空 build ID、PGO 和可复现性验证。
- 前端构建使用固定 commit、固定 lockfile 安全补丁、`npm ci --ignore-scripts` 和固定 build epoch；在线/离线两次产物逐文件一致。
- CI 包含 default/scale 测试、race、vet、供应链、漏洞、性能回退、前端/二进制复现和跨平台构建门禁。
- Release 资产合同为 7 个二进制及其 7 个 SHA-256：Windows amd64/arm64/386，Linux amd64/arm64/386/riscv64。

## 验收摘要

- 10,000 个虚拟节点 HTTP 上报全部成功；2,000 个 WebSocket 同时连接硬风暴的 6,000 次上报全部成功。
- 10,000 个 WebSocket 在 5 秒确定性 ramp 下全部连接并上报成功；100 节点、1 秒间隔、约 2 分钟长稳测试的 12,000 次上报全部成功。
- 全部压力完成后 `/ping` 正常、SQLite `integrity_check` 为 `ok`，进程可优雅关闭。
- default/scale unit、完整 race、vet、全包 benchmark、容器存储集成、安全回归、可复现构建和完整发布矩阵通过。

完整设计、逐项实现和量化结果见 [`PERFORMANCE_REFACTOR_PLAN.md`](PERFORMANCE_REFACTOR_PLAN.md)、[`PERFORMANCE_REFACTOR_TODOLIST.md`](PERFORMANCE_REFACTOR_TODOLIST.md) 与 [`PERFORMANCE_RESULTS.md`](PERFORMANCE_RESULTS.md)。
