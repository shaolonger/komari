# Fleet Report Settings Polish Todo

本清单用于修复全局运维报告配置页的可发现性和交互问题。范围只限 `/Users/shaolong/Code/personal/komari`，不修改 LuminaPlus 主题。

## Problem Analysis

- 菜单入口缺失：`/admin/notification/fleet-report-settings` 是后端原生页面，但左侧菜单来自构建后的 Komari Web 前端。当前仓库没有可编辑的 Komari Web 源码，Release 构建时会从 `komari-monitor/komari-web` 克隆前端，因此需要在后端对 `/admin` 页面做稳定的内置入口注入，避免依赖主题。
- 时区下拉不可用：页面使用了 `input list=datalist`，部分浏览器会按当前输入值过滤，只显示 `UTC`；同时后端只接受 IANA 时区名，`UTC+8` 这种用户直觉写法无法保存。
- 测试报告周期不清晰：该字段的目的只是决定“点击发送测试报告”时生成日报、周报还是月报，不会改变保存配置。当前页面没有说明，也没有随选择变化的即时反馈，容易被误认为 bug。

## Todo

- [x] T0 保存本地执行清单，明确菜单、时区、测试周期三个问题的原因和验收标准。
- [x] T1 为 Komari 后台左侧菜单增加全局运维报告入口：后端对 `/admin` 页面注入稳定菜单项，指向 `/admin/notification/fleet-report-settings`。
- [x] T2 优化报告时区配置：改为真实下拉选项，并让后端支持 `UTC+8`、`GMT+8`、`UTC-05:00` 等固定偏移写法。
- [x] T3 优化测试报告周期交互：明确说明它只影响测试发送，并在选择变化时更新说明和按钮文案。
- [ ] T4 增加并运行测试：菜单注入、UTC 偏移解析、页面内容、配置 API、全量 Go 测试。
- [ ] T5 更新版本号，打 tag，push 到远程 GitHub，并创建新的 GitHub Release。

## Acceptance

- 用户在 Komari 后台通知菜单中可以看到“Fleet Report”入口，并能点击进入配置页。
- 报告时区下拉能看到多个常用时区和固定 UTC 偏移选项。
- `UTC+8` 可以保存，并按固定 UTC+8 计算触发时刻和报告时间。
- 测试报告周期选择变化后，页面会显示对应说明，用户能理解它只影响测试发送。
- LuminaPlus 不增加任何非主题配置入口。
- 所有相关测试和 `go test ./...` 通过。
