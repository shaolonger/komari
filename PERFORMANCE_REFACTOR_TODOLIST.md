# Komari 极致性能重构 Todo

状态：执行中  
设计文档：[`PERFORMANCE_REFACTOR_PLAN.md`](PERFORMANCE_REFACTOR_PLAN.md)
性能结果：[`PERFORMANCE_RESULTS.md`](PERFORMANCE_RESULTS.md)

## 执行规则

每个任务只有同时满足以下条件才允许从 `[ ]` 改为 `[x]`：

1. 实现、迁移、测试和文档已完成；
2. 任务列出的专项测试通过；
3. `go test ./...` 与 `go vet ./...` 通过；
4. 涉及并发的任务必须通过相关 `-race` 测试；
5. 性能任务保留修改前后 benchmark/查询计数证据；
6. 安全边界测试不得回退；
7. 已创建一个包含 Todo 勾选的独立 Git commit。

## Phase K0：基线与性能工程

- [x] **K-000 重构设计与可验收任务清单**
  - 交付：本设计文档和 Todo 清单。
  - 验证：Markdown 链接和任务 ID 完整性检查。
  - 提交：文档独立提交。

- [x] **K-001 建立服务端性能基线与回放工具**
  - 为报告解码、分钟聚合、批写、历史压缩、历史查询和流量汇总增加 benchmark。
  - 增加可配置虚拟 Agent 回放器，支持 HTTP/WS、节点数、频率和持续时间。
  - 输出 allocations/op、bytes/op、p50/p95/p99、SQL 次数和峰值内存。
  - 测试：benchmark smoke、回放器小规模集成、`go test -race`。

- [ ] **K-002 增加受控运行时性能指标和诊断入口**
  - 增加接收、队列、批次、SQLite、压缩、查询和 WS 指标。
  - pprof/trace 默认关闭或仅绑定 loopback/受保护管理面。
  - 禁止敏感高基数标签。
  - 测试：未授权不可访问、指标内容脱敏、并发采集 race。

## Phase K1：接收、聚合和实时快照

- [ ] **K-101 有界、一次、类型化的 HTTP/WS 报告解码**
  - HTTP body 和 WS message 设置硬上限。
  - 删除重复 `map[string]interface{}` 解码。
  - UUID 只信任认证上下文。
  - 测试：超限、畸形 JSON、UUID 伪造、旧 Agent 兼容、fuzz。

- [ ] **K-102 分片实时快照与原子 minute accumulator**
  - 替换 `go-cache Get/append/Set` 报告切片。
  - 固定 shard 数，值语义不可变 snapshot。
  - 保持当前 Record/GPU 聚合业务语义。
  - 测试：并发同 UUID、跨 UUID、分钟边界、GPU 多设备、旧新结果对照、race、benchmark。

- [ ] **K-103 有界批次和 SQLite 单写器**
  - flush 使用有界队列和明确背压。
  - prepared SQL 批量写入 Record/GPURecord/PingRecord。
  - 失败批次有限重试且不可静默丢失。
  - 测试：写入失败、重试、关闭 drain、并发查询、SQLite busy、race、benchmark。

- [x] **K-104 Agent 协议 v2 与 v1 兼容协商（服务端部分）**
  - 增加长度受限的二进制遥测协议。
  - 保留 JSON v1，握手明确版本和能力。
  - 未识别版本 fail closed，不影响控制能力鉴权。
  - 测试：v1/v2 对照、畸形帧、版本降级、大小限制、跨仓库兼容夹具。

## Phase K2：配置、认证与连接安全热路径

- [ ] **K-201 不可变配置快照与主动失效**
  - Get/GetAs/GetManyAs 热读不再访问数据库。
  - Set/批量 Set 事务成功后原子发布新 snapshot。
  - 保持订阅事件和默认值语义。
  - 测试：并发读写、默认值、订阅、失败事务不发布、race、benchmark。

- [ ] **K-202 API Key、客户端 Token 与 Session 安全缓存**
  - 使用摘要键、版本、过期和吊销状态。
  - 轮换/吊销/删除后立即失效。
  - 不增加明文凭据驻留和日志暴露。
  - 测试：命中、过期、轮换、吊销、并发失效、恒定行为、安全回归、race。

- [ ] **K-203 Session 活跃信息合并与节流写回**
  - latest_online 定期写回；IP/UA 变化立即写入。
  - shutdown 前有界 drain。
  - 增加 expires/uuid 必要索引。
  - 测试：写入次数、状态变化、过期、退出 drain、数据库故障、race。

## Phase K3：SQLite、压缩和保留

- [ ] **K-301 SQLite 连接治理、schema 版本与热路径索引**
  - writer/reader 连接策略和每连接 PRAGMA。
  - 增加 Record、GPU、Ping、Session 复合/唯一索引。
  - 引入可验证 schema version migration。
  - 测试：旧库升级、重复迁移、索引查询计划、WAL 并发、回滚演练。

- [ ] **K-302 增量、分块、幂等的 Record/GPU 历史压缩**
  - high watermark、稳定时间边界和 chunk。
  - 结构化 bucket key、选择算法、批量 UPSERT。
  - 删除分块，限制事务时间。
  - 测试：边界、重复执行、crash 恢复、迟到数据、GPU 多设备、百万行 benchmark。

- [ ] **K-303 多级聚合与保留策略**
  - 建立 raw/1m/15m/1h 粒度选择。
  - 每层独立保留和增量构建。
  - 保持 counter reset、峰值和总量语义。
  - 测试：跨层一致性、迟到数据、删除安全、长范围查询性能。

## Phase K4：查询与报表

- [ ] **K-401 统一查询预算、字段投影和峰值保真降采样**
  - 为公开 API、JSON-RPC、管理 API 设置窗口/节点/点数上限。
  - 按 load_type 投影列。
  - 使用 LTTB 或 min/max envelope。
  - 测试：所有边界、权限差异、尖峰/首尾保留、超限错误、benchmark。

- [ ] **K-402 recent/long-term/多级表无重叠查询规划器**
  - 明确切分范围，禁止重复、乱序和错误吞掉。
  - 根据窗口和 MaxCount 选择数据粒度。
  - 测试：4/5 小时边界、空表、错误传播、排序、DST、查询计划。

- [ ] **K-403 流量接口集合查询与流式聚合**
  - 消灭逐节点查询和完整 Record 加载。
  - 仅读取 counter 字段，按 `(client,time)` 扫描。
  - 限制 include_node_series 预算。
  - 测试：counter reset、缺口、时区、节点过滤、SQL 次数、10k 节点 benchmark。

- [ ] **K-404 舰队报告、负载/流量通知和 Ping 统计集合化**
  - 消灭 `2N+1` 查询和 task × records 重复扫描。
  - 使用一次/常数次查询完成分组统计。
  - 测试：结果与旧实现对照、空节点、隐藏节点、SQL 次数、benchmark。

- [ ] **K-405 权限感知的历史响应缓存与流式 JSON**
  - 缓存键包含权限、查询参数和数据 generation。
  - 新数据、权限、隐藏状态变化立即失效。
  - 大响应流式编码并支持取消。
  - 测试：权限隔离、失效、取消、慢客户端、内存上限、安全回归。

## Phase K5：调度、WebSocket、HTTP 与静态资源

- [ ] **K-501 context 化调度器、稳定抖动和有界 worker**
  - 修复通知 reload goroutine 泄漏。
  - Ping/通知按 next-run 调度，避免同秒风暴。
  - 测试：reload 退出、取消、抖动稳定性、队列上限、race、goroutine 泄漏检测。

- [ ] **K-502 WebSocket 生命周期、deadline 和慢消费者隔离**
  - read limit、pong、write deadline、连接关闭状态统一。
  - 每连接有界发送队列，慢客户端不阻塞其他连接。
  - 测试：慢读、半开连接、超大消息、重连替换、并发关闭、race。

- [ ] **K-503 Dashboard snapshot/delta/sequence 与快照所有权**
  - 修复内部报告被调用方修改。
  - 初始 snapshot 后仅发送 delta。
  - 丢 sequence 可请求重新同步。
  - 测试：不可变性、顺序、丢包恢复、权限过滤、10k 节点 benchmark。

- [ ] **K-504 HTTP Server 资源超时与生产日志策略**
  - header/read/write/idle timeout 和 MaxHeaderBytes。
  - 高频成功上报采样；错误、安全、审计日志保留。
  - URL query 和认证材料脱敏。
  - 测试：slowloris、超时、日志采样/脱敏、优雅关闭。

- [ ] **K-505 静态资源 manifest、ETag、immutable 与预压缩**
  - 主题加载时构建不可变 manifest。
  - 哈希资源长期缓存，HTML generation cache。
  - 支持预压缩 Brotli/Gzip 和条件请求。
  - 保持路径穿越保护。
  - 测试：ETag/304、主题切换失效、编码协商、路径安全、benchmark。

## Phase K6：规模化后端与发布工程

- [ ] **K-601 遥测/控制数据存储接口与 SQLite 适配器**
  - 领域层不直接依赖 GORM 全局实例。
  - 定义 batch write、range query、aggregate、retention、health 接口。
  - SQLite 适配器通过完整兼容测试。
  - 测试：contract suite、故障注入、取消、并发。

- [ ] **K-602 可选 PostgreSQL 控制面与 ClickHouse 遥测适配器**
  - 默认构建仍可只运行 SQLite。
  - 批次幂等、连接池、TLS、迁移和健康检查。
  - 鉴权状态以强一致控制存储为真相源。
  - 测试：容器集成、契约对照、断线恢复、TLS、迁移、压力测试。

- [ ] **K-603 可复现构建、PGO 与性能 CI 门禁**
  - Go 版本与 `go.mod` 对齐。
  - 前端固定 commit/lockfile 并使用 `npm ci`。
  - 增加代表性 PGO profile 生成和发布构建。
  - benchmark 采用受控基线比较。
  - 测试：重复构建哈希/元数据、无网络漂移、PGO on/off smoke、安全供应链门禁。

- [ ] **K-604 全量压力、安全回归、升级回滚和发布验收**
  - 完成全部单元、集成、race、vet、benchmark 和虚拟 Agent 压测。
  - 完成 SQLite 旧库升级/回滚和 v1/v2 Agent 兼容矩阵。
  - 检查全部 Todo、提交、工作树和发布资产。
  - 推送分支，创建新 SemVer Release，等待 GitHub Actions 成功并验证资产。
