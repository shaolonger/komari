# Komari v1.3.2

v1.3.2 是 v1.3.1 的紧急升级修复版本，解决部分历史 SQLite
数据库在首次执行 schema v4 迁移时因孤儿通知记录而无法启动的问题。

## 启动迁移修复

- schema v4 现在会在同一个数据库事务中识别并清理
  `offline_notifications` 与 `traffic_report_notifications` 中已经失去父
  `clients` 记录的无效配置，然后再重建级联外键。
- 旧版本未全面启用 SQLite 外键校验时可能产生这些孤儿记录；它们不再
  对应任何服务器，也无法形成有效通知。
- 清理严格限制在上述两个通知表，不会删除服务器、监控记录、用户配置
  或仍有关联客户端的有效通知设置。
- 如果后续表重建、索引恢复或完整性检查失败，孤儿清理也会随整个迁移
  一起回滚，数据库不会停留在半迁移状态。
- 迁移成功后继续保证 `ON DELETE CASCADE ON UPDATE CASCADE`、schema
  `user_version=4` 以及可重复启动的幂等性。

## 安装与升级可靠性

- 安装器会下载并强制校验 Release 提供的 SHA-256 文件，拒绝安装损坏或
  无法验证的二进制。
- 升级前自动保存当前二进制以及停止服务后的 SQLite 主库、WAL 和 SHM
  文件，备份目录权限采用保守的 `077` umask。
- 服务启动验证从瞬时 `systemctl is-active` 改为等待
  `/api/version` 真正可访问，能够识别数据库迁移阶段的延迟失败。
- 新版本未通过健康检查时自动显示完整状态和最近日志，并恢复升级前的
  二进制后重新启动；不再使用可能误选多个文件的通配符回滚。
- 新安装同样使用应用级健康检查，避免服务刚进入 `active` 就错误报告
  安装成功。

## 升级说明

已经安装 v1.3.1 且因以下日志无法启动的用户，无需安装 `sqlite3`，也
无需手工修改数据库：

```text
Failed to migrate database schema: migration 4
(client_notification_cascade): migrate offline_notifications cascade:
FOREIGN KEY constraint failed
```

直接使用现有 `install-komari.sh` 再次选择升级即可。v1.3.2 首次启动会
自动修复历史孤儿通知记录并完成 schema v4。

## 验收摘要

- 使用官方 v1.3.0 Linux ARM64 二进制生成真实 schema v3 数据库，加入
  两类历史孤儿通知记录后，v1.3.1 可稳定复现启动失败。
- 同一数据库由 v1.3.2 启动后成功进入 HTTP 就绪状态；两类孤儿记录归零，
  两个外键均为 `CASCADE/CASCADE`，`user_version=4`，
  `foreign_key_check` 无输出，`integrity_check=ok`。
- 安装器自动备份、成功升级、健康检查失败和自动恢复路径均有独立回归
  测试覆盖。
