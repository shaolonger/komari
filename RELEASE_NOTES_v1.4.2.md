# Komari v1.4.2

v1.4.2 是 Performance V3 的可靠性与精确性版本，修复高负载遥测重放、Ping
趋势、分层统计、保留策略和自定义数据库升级路径中的跨层问题。安全策略、旧
Agent 协议和旧主题兼容性保持不变。

## 遥测与 Ping 持久化

- 遥测 v3 只 ACK 与历史行原子持久化的连续序列；服务重启前未落盘帧由 Agent
  spool 重放，不再出现“已确认但无历史”。
- 每节点使用 16 个有界分钟窗口，离线重放满额时分块提交，兼顾低内存与恢复吞吐。
- 网络 counter delta 正确转换为单调累计流量和速率，Agent 重启不使累计值倒退。
- Ping 批次行、rollup 和 checkpoint 同事务提交；重复重试不会重复写入或越过数据。
- 删除节点同步清理连接、缓存、序列和未持久化窗口，彻底避免外键错误与幽灵数据。

## Ping 趋势与查询

- `common:getPingOverview` 新增最近一小时 150 秒趋势 series，保留每桶样本数、
  丢包数和丢包率；RPC contract 升级到 `komari.rpc.v2.4`。
- raw/1m/15m/1h 查询使用 SQL 内每序列公平预算，并携带精确 count/sum/min/max/latest。
- 修复 rollup 模式下部分丢包、平均/极值/latest 统计错误；数据库错误不再伪装并
  缓存为“暂无趋势”。
- 历史 rollup 迁移自动启动/续跑，完成前始终读取 raw 数据。

## 保留策略与安装升级

- 管理端 metric retention 现在同时约束逻辑查询与物理分层清理，并解耦 record/Ping
  覆盖，避免一个设置意外重置另一类数据。
- 安装器识别 systemd 命令行、环境变量和 WorkingDirectory 中的真实数据库路径，
  备份/回滚自定义 SQLite 文件及 WAL/SHM。
- systemd 继续执行最小权限硬化，同时为明确的安装目录、工作目录和数据库父目录
  配置精确可写/绑定路径。

## 验收

全仓 unit、race、vet、fuzz、SQLite 事务/迁移、删除、离线重放、安装器升级回滚、
Nano 资源、RPC contract 与跨平台构建门禁通过。
