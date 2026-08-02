# Komari v1.3.1

v1.3.1 是 v1.3.0 的数据库完整性与安全补丁版本，修复管理员面板删除已配置通知的服务器时返回 `500 Internal Server Error` 的问题。

## 修复内容

- 修正 `OfflineNotification` 与 `TrafficReportNotification` 的 GORM 关系定义。此前级联约束错误地标注在标量字段上，SQLite 实际生成的外键为 `ON DELETE NO ACTION`。
- 新增 SQLite schema v4 `client_notification_cascade` 迁移，将 `offline_notifications.client` 和 `traffic_report_notifications.client` 的外键原位升级为 `ON DELETE CASCADE ON UPDATE CASCADE`。
- 迁移过程保留现有表的全部列（包括旧版本遗留列）、显式索引、触发器和有效数据，并在单一事务内完成；可重复启动不会重复执行。
- 客户端删除事务显式清理两类通知配置，兼容尚未迁移或历史 schema 异常的数据库，并确保任一步骤失败时整体回滚。
- 管理接口错误信息增加清晰的分隔符，成功与失败状态码改用标准 HTTP 常量。

## 安全与供应链

- gRPC 从 `v1.79.3` 升级到修复 `GO-2026-6061` 的 `v1.82.1`，同步更新其受约束的 protobuf、genproto、OpenTelemetry 与 Go `x/*` 依赖。
- 更新固定前端 lock 安全补丁：将 `minimatch` 固定为 `10.2.6`、`postcss` 固定为 `8.5.23`、`react-router` 固定为 `8.3.0`，同时满足 React Router 的 React 运行时要求。
- 前端仍使用固定 commit、完整 lockfile SHA-256、`npm ci --ignore-scripts` 和固定构建时间；联网/离线构建产物逐文件一致。
- `npm audit --audit-level=high` 为 0 漏洞，`govulncheck` 为 0 个代码可达漏洞。

## 升级说明

- 升级前仍建议备份 `komari.db`。
- 首次启动 v1.3.1 时会自动把 SQLite `user_version` 从 3 升到 4，无需手工执行 SQL。
- 迁移完成后可使用以下命令检查：

```sql
PRAGMA user_version;
SELECT version, name FROM schema_migrations ORDER BY version;
PRAGMA foreign_key_check;
PRAGMA integrity_check;
```

预期包含：

```text
4|client_notification_cascade
```

并且 `foreign_key_check` 无输出、`integrity_check` 返回 `ok`。

## 验收摘要

- 使用带有离线通知和流量报告配置的 v1.3.0 schema v3 数据库完成真实启动迁移；目标数据、索引和触发器均保留。
- 迁移后删除同一客户端，`clients`、`offline_notifications` 与 `traffic_report_notifications` 对应行全部归零，数据库完整性检查通过。
- default/scale 全量测试、完整 race、两套 vet、PostgreSQL/ClickHouse 集成及 race、前端审计与可复现构建、供应链与 Go 漏洞扫描、PGO 二进制可复现构建全部通过。
