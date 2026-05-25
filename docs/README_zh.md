# Komari

![Badge](https://hitscounter.dev/api/hit?url=https%3A%2F%2Fgithub.com%2Fshaolonger%2Fkomari&label=&icon=github&color=%23a370f7&message=&style=flat&tz=UTC)

![komari](https://socialify.git.ci/shaolonger/komari/image?description=1&font=Inter&forks=1&issues=1&language=1&logo=https%3A%2F%2Fraw.githubusercontent.com%2Fkomari-monitor%2Fkomari-web%2Fd54ce1288df41ead08aa19f8700186e68028a889%2Fpublic%2Ffavicon.png&name=1&owner=1&pattern=Plus&pulls=1&stargazers=1&theme=Auto)

Komari 是一款轻量级的自托管服务器监控工具，旨在提供简单、高效的服务器性能监控解决方案。它支持通过 Web 界面查看服务器状态，并通过轻量级 Agent 收集数据。

如果你是第一次部署 Komari，建议先阅读这份一步步的新手指南：[Komari 新手一步步部署与使用指南](./BEGINNER_GUIDE_zh.md)

[文档](https://komari-document.pages.dev/) | [文档(镜像站 By Geekertao)](https://www.komari.wiki) | [Telegram 群组](https://t.me/komari_monitor)

## 特性

- **轻量高效**：低资源占用，适合各种规模的服务器。
- **自托管**：完全掌控数据隐私，部署简单。
- **Web 界面**：直观的监控仪表盘，易于使用。

## 快速开始

### 0. 容器云一键部署

- 雨云云应用 - CNY 4.5/月

[![](https://rainyun-apps.cn-nb1.rains3.com/materials/deploy-on-rainyun-cn.svg)](https://app.rainyun.com/apps/rca/store/6780/NzYxNzAz_)

- 1Panel 应用商店

已上架1Panel应用商店，应用商店-实用工具-Komari 即可安装

### 1. 使用一键安装脚本

适用于使用了 systemd 的发行版（Ubuntu、Debian...）。

```bash
curl -fsSL https://raw.githubusercontent.com/shaolonger/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

### 2. Docker 部署

为了最大化安全合规性，官方 Docker 容器现在强制以 **非 root 用户** (`UID/GID 10001`) 身份运行。

#### 方案 A：命名卷 (推荐)
Docker 会自动管理命名卷的所有者，保证非 root 容器具有正确的读写权限：
```bash
# 1. 运行带命名卷的容器
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 2. 从本地安全临时文件中获取初始管理员密码（首次成功登录后会自动彻底销毁）：
docker exec komari cat /app/data/init_password.txt
```

#### 方案 B：挂载宿主机目录
如果你倾向于挂载本地目录，**必须**首先在宿主机设置正确的权限所有者：
```bash
# 1. 创建目录并设置为非 root 用户所有
mkdir -p ./data
chown -R 10001:10001 ./data

# 2. 运行容器
docker run -d \
  -p 25774:25774 \
  -v $(pwd)/data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 3. 读取初始管理员密码：
cat ./data/init_password.txt
```

4. 在浏览器中访问 `http://<your_server_ip>:25774`。

> [!IMPORTANT]
> 为了符合安全审计规范，随机生成的默认管理员密码**绝对不会被写入持久化的容器日志或系统服务启动日志中**。它被严格且安全地写入容器内部的 `/app/data/init_password.txt` 文件中，并在管理员首次登录成功后立刻被粉碎销毁。你也可以在启动时使用环境变量 `ADMIN_USERNAME` 与 `ADMIN_PASSWORD` 指定你自己的初始用户名和密码。

### 3. 二进制文件部署

1. 访问 Komari 的 [GitHub Release 页面](https://github.com/shaolonger/komari/releases) 下载适用于你操作系统的最新二进制文件。
2. 运行 Komari：
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
3. 在浏览器中访问 `http://<your_server_ip>:25774`，默认监听 `25774` 端口。
4. 默认账号为 `admin`，随机生成的初始密码会安全写入运行目录下的 `data/init_password.txt` 文件中（不会回显到启动终端或系统日志中）。请在首次成功登录后确认该文件已被自动粉碎擦除，或在启动前通过环境变量 `ADMIN_USERNAME` 与 `ADMIN_PASSWORD` 进行指定。

> [!NOTE]
> 确保二进制文件具有可执行权限（`chmod +x komari`）。数据将保存在运行目录下的 `data` 文件夹中。

### 手工构建

#### 依赖

- Go 1.18+ 和 Node.js 20+（手工构建）

1. 构建前端静态文件：
   ```bash
   git clone https://github.com/komari-monitor/komari-web
   cd komari-web
   npm install
   npm run build
   ```
2. 构建后端：
   ```bash
   git clone https://github.com/shaolonger/komari
   cd komari
   ```
   将步骤1中生成的静态文件复制到 `komari` 项目根目录下的 `/public/defaultTheme/dist` 文件夹，并将 `komari-theme.json` 与 `preview.png`/`perview.png` 复制到 `/public/defaultTheme`。
   ```bash
   go build -o komari
   ```
3. 运行：
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
   默认监听 `25774` 端口，访问 `http://localhost:25774`。

## 前端开发指南

[Komari 主题开发指南 | Komari](https://komari-document.pages.dev/dev/theme.html)

[在 Crowdin 上翻译 Komari](https://crowdin.com/project/komari/invite?h=cd051bf172c9a9f7f1360e87ffb521692507706)

## 客户端 Agent 开发指南

[Komari Agent 信息上报与事件处理文档](https://komari-document.pages.dev/dev/agent.html)

## 贡献

欢迎提交 Issue 或 Pull Request！

## 鸣谢

### 破碎工坊云

[破碎工坊云 - 专业云计算服务平台，提供高效、稳定、安全的高防服务器与CDN解决方案](https://www.crash.work/)

### DreamCloud

[DreamCloud - 极高性价比解锁直连亚太高防](https://as211392.com/)

### 🚀 由 SharonNetworks 赞助

[![Sharon Networks](https://raw.githubusercontent.com/komari-monitor/public/refs/heads/main/images/sharon-networks.webp)](https://sharon.io)

SharonNetworks 为您的业务起飞保驾护航！

亚太数据中心提供顶级的中国优化网络接入 · 低延时 & 高带宽 & 提供 Tbps 级本地清洗高防服务，为您的业务保驾护航，为您的客户提供极致体验。加入社区 [Telegram 群组](https://t.me/SharonNetwork) 可参与公益募捐或群内抽奖免费使用。

### 开源社区

提交 PR、制作主题的各位开发者

—— 以及：感谢我自己能这么闲

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=shaolonger/komari&type=Date)](https://www.star-history.com/#shaolonger/komari&Date)
