# Traffic Report Notification Todo

本清单用于补齐 Komari JavaScript 通知与流量周期报告能力。核心目标是让后台配置的日报、周报、月报真正触发通知，并让 Telegram JavaScript 示例脚本能够以更适合 VPS 运维的方式展示报告内容。

## Scope

- 代码仓库：`/Users/shaolong/Code/personal/komari`
- 关键原因：当前 `traffic_report_notifications` 只保存配置，后端缺少周期调度和发送闭环。
- 实现原则：后端负责可靠触发和生成报告；JavaScript provider 只负责把事件渲染成易读通知。

## Todo

- [x] T0 保存本地执行清单，记录背景、范围、验收方式和断点恢复信息。
- [ ] T1 为 `TrafficReportNotification` 增加日/周/月最后发送时间，避免同一周期重复发送。
- [ ] T2 增加流量周期报告生成与调度逻辑：读取已启用配置，按日/周/月判断到期，汇总历史流量记录并发送 `TrafficReport` 事件。
- [ ] T3 将流量报告调度接入服务定时任务启动流程，并确保通知总开关关闭时不会消耗发送周期。
- [ ] T4 增加后端单元测试，覆盖日/周/月到期判断、窗口计算、重复发送保护和报告内容生成。
- [ ] T5 优化 Telegram JavaScript 通知示例：为 `TrafficReport` 增加专用报告视图、增强时间/失败处理、保持普通告警兼容。
- [ ] T6 运行相关测试与格式化检查，确认后端调度逻辑和 JavaScript provider 入口都能通过。
- [ ] T7 更新版本号，创建 tag，push 到 GitHub，并通过 `gh` 创建 release。

## Acceptance

- 勾选日报/周报/月报后，后端存在独立调度器读取配置并调用 `messageSender.SendEvent`。
- 同一 VPS 在同一日/周/月周期内不会重复发送同类报告。
- 通知总开关关闭时，不更新最后发送时间，避免用户重新开启通知后错过周期。
- 报告正文包含周期、时间范围、上行、下行、总量、平均速率、峰值速率、样本数、覆盖率、质量说明。
- Telegram JavaScript 示例能识别 `TrafficReport` 或“流量报告”消息，并以报告专用结构展示。
- Go 测试通过，相关文件完成 `gofmt`。

## Resume Notes

- 当前后端版本文件：`utils/version.go`
- 当前发现：`api/admin/notification/traffic_report.go` 只保存配置；`cmd/server.go` 的定时任务没有 traffic report 调度入口。
- 关键候选文件：
  - `database/models/notification.go`
  - `utils/notifier/traffic_report.go`
  - `cmd/server.go`
  - `examples/komari-javascript-notification-telegram.js`
  - `api/admin/notification/traffic_report_test.go`
  - `utils/notifier/traffic_report_test.go`

