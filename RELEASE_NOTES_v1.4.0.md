# Komari v1.4.0

v1.4.0 是面向 1 核、2.5 GiB 小型服务器到大规模节点集群的性能版本。它在不降低认证、权限、凭据保护、Ping SSRF 防护和查询预算的前提下，重构了遥测、Ping、历史查询、实时面板与 SQLite 生命周期。

## 极低资源运行

- 新增 `auto/nano/standard/scale` 运行档位；自动识别 cgroup CPU/内存限制，为 1 核或不超过 3 GiB 的部署选择 Nano 参数。
- SQLite 连接池、page cache、mmap、临时存储、busy timeout 和检查点按档位设定；写入继续单写者化，读取并发有硬上界。
- 保留、压缩和清理由 CPU 预算调度，不阻塞启动关键路径；安装器增加安全的应用内数据库备份和 systemd 资源参数。

## Ping 与遥测数据面

- Ping 热路径改为不可变任务索引、紧凑记录、受限微批和持久 ACK；Agent 使用有版本、可过期的租约计划与有序结果批次。
- 新增 Ping raw/1m/15m/1h 层级、崩溃安全压缩与按点数预算选层；30 节点、105 个任务的 6 小时 Nano 查询只扫描预算内行数。
- 遥测 v3 支持有界聚合、序列 ACK、断线重放和 v2/v1 安全回退；SQLite 与 ClickHouse 查询语义保持一致。

## 面板与 RPC

- 新增带 sequence 的可恢复实时 delta；面板不再每两秒拉取全量节点状态。
- Ping 概览和 Compare 历史支持授权节点集合查询，消除按任务和按节点的 N+1 请求。
- 发布 `komari.rpc.v2.3` 能力发现，包含 metric definitions/query/migration、Ping overview、租约、结果批次、实时 delta 与集合历史查询。
- 补齐管理端指标 RPC，修复“数据保留天数”页面的 `method not found`；未知设置、越界查询和存储密钥继续 fail closed。

## 数据安全与兼容

- 覆盖从全部历史 schema、孤儿通知/记录和损坏迁移状态升级的真实数据库矩阵；失败事务完整回滚。
- 删除客户端继续由数据库级联和显式清理共同保证，不再因历史外键残留返回 500。
- 旧 Agent、遥测 v1/v2、SQLite Lite 和可选 ClickHouse 均保留兼容路径。

## 验收

- 全量 Go 测试、race、vet、fuzz、故障注入、安全扫描、真实迁移矩阵、SQLite/ClickHouse parity 与官方前端构建通过。
- 30 节点/105 Ping 分配 Nano fixture、24h/72h 等效长稳回放和资源平坦性门禁通过。
- 详细设计和逐项状态见 `PERFORMANCE_V2_PLAN.md` 与 `PERFORMANCE_V2_TODOLIST.md`。
