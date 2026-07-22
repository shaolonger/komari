# Komari 极致性能重构设计

状态：执行中  
基线：`v1.2.13` / `aea0d7e`；实测结果见 [`PERFORMANCE_RESULTS.md`](PERFORMANCE_RESULTS.md#k-001-服务端性能基线与虚拟-agent-回放器)
配套清单：[`PERFORMANCE_REFACTOR_TODOLIST.md`](PERFORMANCE_REFACTOR_TODOLIST.md)

## 1. 文档目标

本文定义 Komari 服务端在不降低现有安全性、数据正确性和轻量部署能力的前提下，重构为高吞吐、低延迟、可观测、可水平扩展监控平台的完整方案。

本轮重构覆盖：

- Agent 遥测接收、实时快照和分钟聚合；
- SQLite 连接、批量写入、索引、保留和历史压缩；
- 历史记录、流量、Ping、舰队报告和通知查询；
- 配置、API Key、客户端 Token 和 Session 热路径；
- WebSocket 连接、Dashboard 实时推送和调度器；
- HTTP Server、静态资源、日志和资源上限；
- 存储抽象、可选规模化时序后端和协议 v2；
- benchmark、压测、race、性能回归、构建与发布门禁。

不在当前仓库直接实现的内容：生产前端 `komari-web` 的组件级和 bundle 级优化。当前 checkout 只有占位页，前端必须在其独立仓库审计。本仓库负责静态资源服务、缓存、压缩和可复现构建。

## 2. 不可退让的设计原则

### 2.1 安全不回退

- 客户端 Token 继续通过 Header 传输，禁止重新引入查询串凭据；
- API Key、Token、Session 缓存使用摘要键，不新增明文凭据副本；
- Token 轮换、主动吊销、Session 过期和权限修改必须立即使缓存失效；
- 所有 HTTP/WS 请求体、消息、时间窗口、点数、并发和队列必须有硬上限；
- 主题路径校验、上传/解压限制、Webhook/SSRF 约束和管理面边界保持不变或加强；
- 不以 `synchronous=OFF`、丢弃安全日志或延迟吊销换取吞吐；
- pprof、trace 和内部指标不得默认暴露到公网；
- 数据库迁移必须可回滚，升级失败不得破坏现有数据。

### 2.2 兼容与渐进迁移

- 保留当前 JSON 协议 v1；协议 v2 通过显式协商启用；
- 保留 SQLite 作为默认 Lite 后端；规模化后端必须是可选能力；
- 管理和低频控制数据可继续使用 GORM；遥测热路径允许使用 `database/sql`；
- API 默认返回结构保持兼容；新增上限必须返回明确、稳定的错误；
- 所有存储结构变更先 backfill/双读验证，再切换读取路径。

### 2.3 先测量，再优化

- 每个热点在修改前后都要有 benchmark、查询计数、分配数或压力测试证据；
- 性能提交不得夹带无关功能改动；
- 每项 Todo 必须通过其专项测试和仓库全量测试后才可提交；
- 关键 benchmark 回退超过既定阈值时阻断合并和发布。

## 3. 当前性能模型与主要问题

### 3.1 接收与分钟聚合

当前 HTTP 上报无界读取 body，并重复解析 JSON；WS 上报先解析消息类型再解析整个报告。报告按 UUID 放入通用缓存，采用 `Get -> append -> Set` 复合操作，同一节点并发上报可能发生覆盖。每分钟 flush 时，多个指标分别对相同报告切片排序并通过 GORM 写入。

结果：

- 热路径有重复分配和反射；
- 内存与节点数、上报频率、flush 间隔成乘积增长；
- 缓存复合更新不具备每节点原子性；
- flush 产生集中式 CPU、GC 和 SQLite 写入尖峰。

### 3.2 SQLite 与历史压缩

当前只配置启动连接上的 PRAGMA，没有显式治理 `database/sql` 池。SQLite 只有一个写者，但 ORM 小事务、逐行 Count/Update/Create 和后台清理共享同一写路径。

历史压缩每 30 分钟重新读取全部超过 4 小时的记录，却只删除超过 5 小时的记录；4～5 小时数据会被反复聚合。每个指标建立独立切片并排序，每个桶再发起多条 SQL。

### 3.3 查询放大

- recent 与 long-term 范围重叠后在 Go 内存中合并；
- JSON-RPC 可请求无限点数和无硬上限时间窗口；
- 流量接口按节点查询；
- 舰队报告按节点查询记录和 Ping；
- Ping 公开统计存在重复扫描；
- 查询先加载完整模型，再投影和降采样。

### 3.4 认证与配置

配置读取频繁访问 SQLite 并 JSON 反序列化。Session 每个已认证请求都执行一次数据库读取和一次 latest 信息写入。高流量 Dashboard 会把控制面数据库变成认证热锁。

### 3.5 调度、实时推送和静态资源

- Ping 按 interval 创建 goroutine，在同一时间复制全部连接并突发下发；
- 负载通知 reload 只停止 ticker，旧 goroutine 无法退出；
- Dashboard 拉取时复制全部在线节点和报告；
- 实时报告快照返回指针副本，调用方可修改内部对象；
- 静态资源每次请求重新读取配置、stat 和 read file；
- HTTP Server 缺少完整超时和 header 限制；
- 高频成功请求同步写日志。

## 4. 目标架构

```text
HTTP / WebSocket
       |
       v
限长读取 -> 一次鉴权 -> 类型化解码 -> UUID 分片
                                   |
                 +-----------------+-----------------+
                 |                                   |
                 v                                   v
          实时不可变快照                       分钟增量聚合器
                 |                                   |
                 v                                   v
       Dashboard snapshot/delta              有界批次 -> 单写器
                                                     |
                          +--------------------------+------------------+
                          |                                             |
                          v                                             v
                  SQLite Lite Store                         Scale Telemetry Store
                          |                                             |
                          +----------------------+----------------------+
                                                 v
                               分粒度查询 / 预聚合 / 降采样
```

### 4.1 遥测接收层

- HTTP 使用 `MaxBytesReader`，WS 使用 `SetReadLimit`；
- JSON v1 使用严格、类型化、一次解码；
- 协议 v2 使用长度受限的二进制消息；
- UUID 由已经认证的上下文决定，消息内 UUID 不能覆盖认证身份；
- 按 UUID 哈希到固定数量 shard；
- 每个 shard 串行更新节点 accumulator，避免每节点锁和全局锁；
- 实时快照使用值语义或不可变对象，禁止可变指针逃逸。

### 4.2 聚合与写入层

每个节点每分钟只保留：

- count、sum、min、max、latest；
- 为兼容既有 top-percentage 语义所需的有界选择结构；
- GPU 按 `(uuid,device_index)` 独立聚合；
- flush generation 和最后成功持久化位置。

flush 通过有界 channel 发送到 writer。writer 负责：

- 合并小批次；
- prepared statement；
- 一个 SQLite 写事务写完一批 Record/GPURecord/PingRecord；
- 暂时失败时有限重试并保留批次；
- 队列接近上限时产生可观测告警，不能静默丢数据。

### 4.3 SQLite Lite 后端

- writer handle：`MaxOpenConns(1)`；
- reader handle：按压测决定固定连接数；
- 连接初始化统一应用 WAL、busy timeout、foreign keys、synchronous、cache；
- 遥测表使用合适的复合/唯一索引；
- 热 SQL 使用 `database/sql`，控制面仍可通过 GORM；
- 删除和压缩按 chunk 运行，限制单事务时长；
- schema 迁移记录版本并在启动前验证。

### 4.4 增量历史压缩

- 以完整 15 分钟桶为单位维护 high watermark；
- 每次仅处理 watermark 之后、稳定时间边界之前的数据；
- 使用结构化 key，禁止 RFC3339 字符串作为分组键；
- 采用 selection/有界堆计算分位数，避免全排序和重复类型转换；
- 使用唯一索引和批量 UPSERT；
- 原始记录删除与聚合提交分批完成；
- crash 后重复执行相同桶必须幂等；
- 支持 1 分钟、15 分钟、1 小时多级聚合和明确保留策略。

### 4.5 查询层

- API 入口统一解析并验证时间窗口、节点数、点数和 group_by；
- 根据请求范围选择 raw/1m/15m/1h 表；
- SELECT 仅请求字段；
- 所有节点查询使用集合 SQL，不在 handler 中逐节点查询；
- 结果按 `(client,time)` 流式扫描聚合；
- 使用 LTTB 或 min/max envelope 保留尖峰；
- 缓存键包含权限范围、参数和数据 generation；
- 大结果使用流式 JSON 编码，禁止无界切片物化。

### 4.6 配置与认证快照

- 启动时加载完整配置为不可变 snapshot；
- Set/批量 Set 成功提交后构造新 snapshot 并原子替换；
- API Key、Token、Session 使用摘要键缓存；
- 每个缓存项携带版本、过期、吊销信息；
- 修改、轮换、吊销在事务成功后同步失效；
- Session latest_online 按固定窗口写回，IP/UA 真实变化立即写入；
- handler 复用 Identity 中间件结果，禁止再次查 Session。

### 4.7 调度和实时连接

- Ping、通知等周期任务统一使用 context 管理生命周期；
- 使用最小堆/时间轮计算 next run；
- 节点任务采用稳定哈希抖动；
- 下发走有界 worker pool，避免 goroutine 风暴；
- Dashboard 初次发送 snapshot，之后发送带 sequence 的 delta；
- 慢客户端有独立有界队列，不能阻塞采集端或其他客户端；
- read/write deadline、pong、消息限制和连接关闭状态统一处理。

### 4.8 Scale 后端

规模化后端采用接口隔离：

- PostgreSQL：用户、节点、Token/Session、任务、配置、审计元数据；
- ClickHouse：Record、GPURecord、PingRecord 和多级聚合；
- 消息总线只负责路由/通知，不作为鉴权真相源；
- 读取按租户/权限过滤，写入批次有幂等键；
- 默认构建和默认配置仍只需要 SQLite。

## 5. 性能与资源目标

所有指标在固定硬件和固定回放数据集上验收：

- 接收 handler 自身 p99 小于 5ms，不包含客户端网络；
- 每份 JSON v1 报告最多一次完整解码；
- minute flush 峰值内存与活跃节点数线性相关，与一分钟原始报告数不再线性相关；
- 10,000 节点流量/舰队查询的 SQL 次数为常数级；
- 历史压缩内存由 chunk size 限定；
- 压缩运行时 API p99 不超过空闲期两倍；
- Session 数据库写入不超过每活跃 Session 每 30～60 秒一次，安全状态变化除外；
- Dashboard 单节点更新不再序列化全量节点；
- 无 `database is locked`、无无界队列、无 silent drop；
- 关键 benchmark 相对已接受基线回退超过 5% 时失败。

绝对容量目标必须由 CI/专用压测机记录，不在缺少硬件定义时宣称固定 QPS。

## 6. 可观测性

需要记录但避免高基数敏感标签：

- 报告接收数、拒绝数、解码耗时和消息大小分布；
- shard accumulator 数量、flush 队列深度、批次大小和重试；
- SQLite busy 时间、事务耗时、查询行数和查询类别；
- 压缩 high watermark、chunk 耗时、处理/删除行数；
- 查询窗口、返回点数、降采样前后点数；
- WS 在线数、慢消费者、重连和写超时；
- 调度延迟、worker 队列和被限流任务；
- Session 缓存命中、失效、写回；
- GC、heap、goroutine、mutex/block profile。

禁止把 Token、Session、API Key、完整 IP、完整 URL query 或用户自定义脚本内容放入指标标签。

## 7. 测试策略

### 7.1 单元与性质测试

- accumulator 与旧聚合语义对照；
- 分位数、counter reset、时间桶边界和 DST；
- compaction 幂等、crash/retry 和 high watermark；
- 权限缓存主动失效与过期；
- 查询上限、投影、降采样首尾/尖峰保留；
- scheduler reload 后 goroutine 退出；
- snapshot 不可变性和 delta sequence。

### 7.2 数据库集成测试

- 新旧 schema 迁移；
- WAL 下并发读和单写；
- busy timeout、事务回滚、唯一约束和 UPSERT；
- 大批量压缩期间公开/管理查询；
- SQLite 重启恢复和重复 flush。

### 7.3 性能测试

- `go test -bench . -benchmem`；
- 1k/10k/100k 虚拟 Agent 上报回放；
- Dashboard 订阅和慢消费者；
- 1 天/30 天/1 年历史范围查询；
- 压缩、保留删除和舰队报告并行运行；
- benchmark 结果使用 benchstat 与受控基线比较。

### 7.4 发布前门禁

- `go test ./...`；
- `go test -race ./...`；
- `go vet ./...`；
- 安全回归脚本；
- benchmark 回归；
- release 模式全平台构建；
- 数据库升级/回滚演练；
- 旧 Agent JSON v1 兼容测试。

## 8. 提交、回滚与发布

- 每个 Todo 使用独立提交；
- 同一提交包含实现、测试、必要文档和 Todo 勾选；
- 未通过专项测试和全量测试不得勾选；
- schema 变更必须提供向前迁移和可执行回滚说明；
- 新数据路径先由 feature flag/配置控制，完成双读对比后切换；
- Release 使用新的 SemVer tag，不覆盖既有 tag；
- Release 创建后等待 GitHub Actions 完成并核对全部二进制资产。
