# Komari Performance V3 Todo

状态：实现与测试完成，GitHub 发布步骤待执行。

## K3-0 设计与兼容

- [x] **K3-001** 固化三仓库 v3 数据契约与安全不变量
- [x] **K3-002** 保留 telemetry v1/v2 与旧主题降级兼容
- [x] **K3-003** 发布 `komari.rpc.v2.4` / `ping.overview=2` contract

## K3-1 遥测可靠性

- [x] **K3-101** 分离 accepted sequence 与 durable sequence
- [x] **K3-102** 将遥测历史行和 v3 checkpoint 纳入同一 writer 事务
- [x] **K3-103** 实现空库首序列基线与 checkpoint-only 安全重定位
- [x] **K3-104** 将每节点离线重放扩展为 16 个有界分钟窗口并分块 flush
- [x] **K3-105** 消费 v3 网络 counter delta 并处理 Agent counter reset
- [x] **K3-106** 删除节点时清理连接、最新值、序列和未落盘聚合状态

## K3-2 Ping 精确性与性能

- [x] **K3-201** 将 Ping 行、rollup 与 batch checkpoint 原子提交
- [x] **K3-202** 为 raw/rollup 查询返回 count/sum/min/max/last 精确元数据
- [x] **K3-203** 在 SQL 中按 series 公平分配点数并限制总扫描行数
- [x] **K3-204** 提供一小时、150 秒桶的集合 Ping overview series
- [x] **K3-205** 修复 rollup 统计中的部分丢包、加权平均、极值和 latest
- [x] **K3-206** 存储失败返回 RPC 错误且禁止缓存为空趋势
- [x] **K3-207** rollup 迁移完成前 raw fallback，idle/running 任务自动恢复

## K3-3 保留策略与升级

- [x] **K3-301** 让 metric retention 覆盖进入查询 TTL 与物理清理计划
- [x] **K3-302** 解耦 record/Ping 覆盖并实现 Ping 分层保留
- [x] **K3-303** 安装器识别 systemd 自定义数据库路径并生成一致备份
- [x] **K3-304** 在不扩大 home 暴露面的前提下支持自定义可写路径

## K3-4 验收与发布

- [x] **K3-401** unit、race、vet、fuzz 与跨平台构建门禁
- [x] **K3-402** SQLite 事务回滚、序列重放和删除回归矩阵
- [x] **K3-403** 迁移、保留、安装升级/回滚和 Nano 资源门禁
- [x] **K3-404** 与 Agent v1.4.1、LuminaPlus v1.22.1 交叉契约验收
- [ ] **K3-405** 版本、提交、推送、tag 与 GitHub Release
