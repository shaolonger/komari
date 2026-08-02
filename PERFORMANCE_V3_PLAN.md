# Komari Performance V3：可靠、精确、可持续的极限性能数据面

状态：实现完成，目标版本 v1.4.2

本轮工作是 Performance V2 的可靠性收口，不替换 Go、SQLite、React 等
技术栈。现场的 1 核、2.5 GiB 主机承载约 30 个节点与 105 个 Ping 分配时，
资源异常并非由语言或框架本身造成，而是由写入确认、历史查询、迁移状态、
前端数据契约和升级路径之间的跨层缺口造成。V3 在保持现有认证、授权、SSRF
防护和最小权限策略的前提下修复这些缺口。

## 1. 不可妥协的不变量

- 遥测或 Ping 的 ACK 只确认已与对应历史数据一起持久化的连续序列。
- 数据库事务失败时，历史行与 checkpoint 必须同时回滚。
- Agent 重连、服务端重启、SQLite 短暂锁竞争和断电恢复不得产生序列空洞、
  重复流量累计或静默丢数。
- 每节点内存窗口、writer 队列、SQL 返回行、RPC UUID、前端请求和迁移批次
  都有硬上限；性能不能依赖无界缓存。
- 匿名调用继续过滤隐藏节点和 Ping target；Agent 探测权限、私网地址限制、
  DNS 固定、端口/超时/响应大小限制均不放宽。
- 旧 telemetry v1/v2 Agent 继续兼容；v3、Ping overview v2 通过显式能力发现。
- 历史 rollup 未完成前只读 raw 表，绝不因空 rollup 表存在而展示空数据。

## 2. 遥测数据面

### 2.1 原子持久化确认

服务端把“进程内已接受序列”和“数据库已持久序列”分离。每个 v3 frame
先进入有界分钟聚合器，完整窗口排入单 writer；历史 `records`、GPU 行和
`telemetry_v3_sequences` 在同一 SQL 事务中写入。WebSocket ACK 返回 durable
checkpoint，而不是仅在内存中接收成功的 sequence。

SQLite 使用同一 writer 事务完成原子提交。ClickHouse 等外部存储使用确定性
batch ID 保证重试幂等，外部批次成功后再推进嵌入式控制库 checkpoint；失败
重试不会制造重复逻辑批次。

### 2.2 有界离线重放

每节点可保留 16 个待提交分钟窗口。Agent 大量离线数据高速重放达到上限时，
服务端同步提交已经完成的有界块，然后重试当前原始 frame。该机制既避免原先
两窗口上限导致的重放失败，也不允许离线历史无限占用堆。

首次连接空数据库时，已认证 Agent 的任意首序列可以建立基线。存在旧版 Agent
不可恢复的 spool 头部空洞时，仅完整 checkpoint frame 可以安全重定位；普通
增量遇到空洞仍 NACK 并请求期望序列。

### 2.3 精确网络计数

v3 envelope 的上传/下载 counter delta 被纳入虚拟单调累计值与速率计算。
Agent 进程重启导致原始 counter 清零时，服务端累计总量不倒退；只有聚合器
成功接收 frame 后才提交网络状态，因此同一 sequence 重试不会重复加算。

### 2.4 删除一致性

删除节点时先关闭其 WebSocket，再清除最新状态、进程内序列状态和未持久化
聚合窗口，随后执行数据库级联删除。这样已删除节点不会被定时 flush 重新写入，
也不会残留在实时 delta 或历史缓存中。

## 3. Ping 数据面与查询计划

### 3.1 行与 checkpoint 同事务

`ping_records`、rollup 更新与 `ping_result_sequences` 在单 writer 事务提交。
重复 ACK、重试和乱序提交均只能单调推进 checkpoint。没有历史 checkpoint 的
新控制库允许已认证 Agent 的首批 sequence 建立基线。

### 3.2 精确 rollup 元数据

Ping 查询结果携带 sample/valid/loss count、sum、min、max、last value/time。
raw、1m、15m、1h 层使用同一结果模型，丢包比例和统计边界不会因下采样而被
“桶平均值”篡改。旧库自动启动可恢复迁移；迁移完成前读 raw，完成后才启用
rollup 查询。

### 3.3 公平且有界的集合查询

每个 `(client, task)` series 在 SQL 中使用 `ROW_NUMBER()` 获得公平点数预算，
再施加全局 LIMIT。30 节点首页只发一次集合请求，不再按节点/任务 N+1 查询。
一小时 overview 返回 150 秒桶（每序列约 24 个点）和精确丢包元数据，可直接
绘制趋势且保持响应有界。

RPC contract 升级为 `komari.rpc.v2.4`，`ping.overview` capability 升级为 2。
存储错误作为 RPC Internal Error 返回，不再缓存或伪装为“无数据”。

## 4. 指标保留与迁移

- 管理端 metric definition 的数据库覆盖同时约束查询可见范围和物理清理。
- 共享宽表按所有非 Ping 指标中最长的保留期物理保存，再按单指标 TTL 逻辑过滤，
  避免为了一个短 TTL 删除其他指标。
- Ping raw 的短期保留继续服从 legacy 设置；1m/15m/1h rollup 分别执行有界
  保留，最终 1h 层服从 Ping metric retention。
- 只覆盖 Ping retention 不改变 record retention；只覆盖 record 也不重置 Ping。
- 启动定时任务自动开始 idle 迁移、恢复 running 迁移，支持 checkpoint 续跑。

## 5. 安全升级路径

安装器从实际 systemd unit 与 drop-in 中解析 `ExecStart --database/-d`、
`Environment=KOMARI_DB_FILE` 和 `WorkingDirectory`，对真正使用的 SQLite 文件
执行内置一致性备份；fallback 复制时统一保存为 `data/komari.db[-wal|-shm]`，
并记录原路径以便回滚审计。

systemd 保持 `NoNewPrivileges`、`ProtectSystem=strict`、内核/控制组保护和资源
上限。`ProtectHome=tmpfs` 配合精确 `ReadWritePaths`/`BindPaths`，允许自定义数据库
位于 home 下时正常启动，同时不把整个 home 暴露给服务。

## 6. 验收与运行观测

本轮发布门禁包括：全仓 Go unit、race、vet、fuzz、真实 SQLite 迁移、writer
回滚、重放/空洞/删除、安装器升级/回滚、Nano 资源 fixture、跨平台构建和 RPC
contract 检查。生产升级后应继续观察 `systemctl status` 的 RSS/CPU、数据库
WAL 大小、Ping overview 延迟与错误率；这些现场值用于验证具体硬件与真实网络
条件，不用合成 fixture 代替生产结论。
