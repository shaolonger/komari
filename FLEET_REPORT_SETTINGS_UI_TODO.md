# Fleet Report Settings UI Todo

本清单用于把全局 VPS 运维报告从“已有后端 API、但需要手动调用”的状态，补齐为 Komari 后台可视化配置能力。该配置属于 Komari 通知系统，不属于主题能力，因此本次不修改 LuminaPlus，也不在 `/?view=theme-manage` 中增加入口。

## Product Decisions

- 主入口放在 Komari 后台通知配置体系，由 Komari 后端提供受管理员权限保护的配置页。
- LuminaPlus 只管理主题相关设置，不承接全局通知报告配置，避免主题与后端通知系统耦合。
- 页面只调用现有 Komari 管理 API，不直接读写数据库。
- 配置项包括启用状态、日报/周报/月报、报告时区、发送小时、Top N、上次发送时间。
- 增加“发送测试报告”能力，避免用户等到下一次定时周期才知道 Telegram 展示是否正确。
- 保留现有 API 能力，未来官方前端 `komari-web` 可以直接复用同一组接口。

## Todo

- [x] T0 保存本地执行清单，明确配置归属、非主题耦合原则和验收标准。
- [x] T1 增加全局运维报告即时测试发送 API，支持按日报/周报/月报生成并投递测试报告。
- [ ] T2 增加 Komari 原生后台配置页，提供完整表单、状态展示、保存和测试发送交互。
- [ ] T3 将配置页路由接入服务端管理员路由，并保持 `/admin` 现有 SPA 路由不被破坏。
- [ ] T4 增加并运行测试：配置 API、测试发送 API、页面权限/内容、路由编译、全量测试。
- [ ] T5 更新版本号，打 tag，push 到远程 GitHub，并创建新的 GitHub Release。

## Acceptance

- 管理员可以访问 `/admin/notification/fleet-report-settings` 配置全局运维报告。
- 不需要在浏览器 Console 手动执行 JS 即可开启、关闭和调整报告。
- 测试发送按钮可以立即发送一份结构化 `FleetReport` 到当前通知渠道。
- 页面保存失败、时区无效、通知通道未配置等场景有清晰反馈。
- LuminaPlus 仓库没有新增通知系统配置入口。
- 所有相关 Go 测试和全量测试通过。
