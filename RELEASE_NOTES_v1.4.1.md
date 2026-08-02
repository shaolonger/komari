# Komari v1.4.1

v1.4.1 是 Komari Performance V2 的最终稳定版本，包含 v1.4.0 的全部极低资源、遥测 v3、Ping 租约/批处理、分层历史、可恢复实时 delta 与 RPC 集合查询能力，并修复最终远端供应链门禁发现的 SQLite 短暂锁竞争。

## SQLite 迁移可靠性

- 指标迁移整页写入在 SQLite `BUSY`/`LOCKED` 时执行有上限、可取消的指数退避，不再把管理端并发读取造成的瞬时锁竞争记为永久迁移失败。
- 每次重试覆盖完整事务；Rollup 与 checkpoint 要么同时提交，要么同时回滚，不会重复累计或跳过数据。
- 新增真实 shared-cache 持锁/释放回归测试，并连续运行 50 轮验证。

## 完整性能升级

- `auto/nano/standard/scale` 运行档位与 cgroup 感知 SQLite/并发/缓存参数。
- 遥测 v3 有界聚合、序列 ACK、断线重放和 v2/v1 安全回退。
- Ping 不可变索引、Agent 租约、结果批次、raw/1m/15m/1h 分层与点数预算查询。
- 面板 snapshot + sequence delta、Ping 概览集合查询和 Compare 历史集合查询，消除周期全量轮询与 N+1 请求。
- `komari.rpc.v2.3` 能力发现和管理端指标 RPC，修复保留天数页面的 `method not found`。
- 客户端删除、历史数据库迁移、孤儿外键和失败回滚矩阵全部覆盖。

## 验收

- 本地全量/default/scale、race、vet、fuzz、故障注入、安全、真实迁移和 Nano/72h 等效资源门禁通过。
- GitHub Performance Regression、Build、Reproducibility and Supply Chain、可复现 PGO 多平台发布与 Docker 发布门禁通过。
