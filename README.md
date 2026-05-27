# Komari

![Badge](https://hitscounter.dev/api/hit?url=https%3A%2F%2Fgithub.com%2Fshaolonger%2Fkomari&label=&icon=github&color=%23a370f7&message=&style=flat&tz=UTC)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/shaolonger/komari)

![komari](https://socialify.git.ci/shaolonger/komari/image?description=1&font=Inter&forks=1&issues=1&language=1&logo=https%3A%2F%2Fraw.githubusercontent.com%2Fkomari-monitor%2Fkomari-web%2Fd54ce1288df41ead08aa19f8700186e68028a889%2Fpublic%2Ffavicon.png&name=1&owner=1&pattern=Plus&pulls=1&stargazers=1&theme=Auto)

[简体中文](./docs/README_zh.md) | [繁體中文](./docs/README_zh-TW.md) | [日本語](./docs/README_ja.md)

Komari is a lightweight, self-hosted server monitoring tool designed to provide a simple and efficient solution for monitoring server performance. It supports viewing server status through a web interface and collects data through a lightweight agent.

If you are deploying Komari for the first time, start with the beginner-friendly step-by-step guide: [Beginner Guide (Simplified Chinese)](./docs/BEGINNER_GUIDE_zh.md)

[Documentation](https://komari-document.pages.dev/) | [文档(镜像站 By Geekertao)](https://www.komari.wiki) | [Telegram Group](https://t.me/komari_monitor)

## Features

- **Lightweight and Efficient**: Low resource consumption, suitable for servers of all sizes.
- **Self-hosted**: Complete control over data privacy, easy to deploy.
- **Web Interface**: Intuitive monitoring dashboard, easy to use.

## Quick Start

### 0. One-click Deployment with Cloud Hosting

- Rainyun - CNY 4.5/month

[![](https://rainyun-apps.cn-nb1.rains3.com/materials/deploy-on-rainyun-cn.svg)](https://app.rainyun.com/apps/rca/store/6780/NzYxNzAz_)

- 1Panel App Store

Available on 1Panel App Store. Install via **App Store > Utilities > Komari**.

### 1. Use the One-click Install Script

Suitable for distributions using systemd (Ubuntu, Debian...).

```bash
curl -fsSL https://raw.githubusercontent.com/shaolonger/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

If `sudo journalctl -u komari -f` shows `Exec format error` after using an older copy of the installer, re-download the latest `install-komari.sh` and run it again. That error usually means the previous script saved an invalid download instead of a real Linux binary.

### 2. Docker Deployment

For maximum security compliance, the official Docker container runs as a **non-root user** (`UID/GID 10001`).

#### Option A: Named Volume (Recommended)
Docker automatically manages the ownership of named volumes, ensuring correct permissions for non-root containers:
```bash
# 1. Run the container with a named volume
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 2. Retrieve the initial password from the secure local file (deleted automatically after first login):
docker exec komari cat /app/data/init_password.txt
```

#### Option B: Bind Mount
If you prefer a host directory bind mount, you **must** set correct ownership first:
```bash
# 1. Create directory and set non-root ownership
mkdir -p ./data
chown -R 10001:10001 ./data

# 2. Run the container
docker run -d \
  -p 25774:25774 \
  -v $(pwd)/data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 3. Retrieve the initial password:
cat ./data/init_password.txt
```

4. Access `http://<your_server_ip>:25774` in your browser.

> [!IMPORTANT]
> To comply with security guidelines, the default random administrator password is **never written to persistent container or system startup logs**. It is strictly written to a secure file `/app/data/init_password.txt` and is securely shredded immediately upon the first successful login. You can also bypass this by providing custom initial credentials through the `ADMIN_USERNAME` and `ADMIN_PASSWORD` environment variables.

### 3. Binary File Deployment

1. Visit Komari's [GitHub Release page](https://github.com/shaolonger/komari/releases) to download the latest binary for your operating system.
2. Run Komari:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
3. Access `http://<your_server_ip>:25774` in your browser. The default port is `25774`.
4. The default username is `admin`, and the random initial password is securely saved to `data/init_password.txt` under your running directory (never printed to startup logs or stdout). Verify this file is deleted automatically upon your first successful login, or customize them beforehand using the `ADMIN_USERNAME` and `ADMIN_PASSWORD` environment variables.

> [!NOTE]
> Ensure the binary has execute permissions (`chmod +x komari`). Data will be saved in the `data` folder in the running directory.

### Manual Build

#### Dependencies

- Go 1.18+ and Node.js 20+ (for manual build)

1. Build the frontend static files:
   ```bash
   git clone https://github.com/komari-monitor/komari-web
   cd komari-web
   npm install
   npm run build
   ```
2. Build the backend:
   ```bash
   git clone https://github.com/shaolonger/komari
   cd komari
   ```
   Copy the static files generated in step 1 to the `/public/defaultTheme/dist` folder in the root of the `komari` project, and copy `komari-theme.json` + `preview.png`/`perview.png` to `/public/defaultTheme`.
   ```bash
   go build -o komari
   ```
3. Run:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
   The default listening port is `25774`. Access `http://localhost:25774`.

## Frontend Development Guide

[Komari Theme Development Guide | Komari](https://komari-document.pages.dev/dev/theme.html)

## Client Agent Development Guide

[Komari Agent Information Reporting and Event Handling Documentation](https://komari-document.pages.dev/dev/agent.html)

## Security Notes For Agent Tokens

- Komari now expects client/agent control-plane authentication through the `Authorization: Bearer <token>` header instead of a `?token=` query string.
- Admin APIs now support client token lifecycle operations at `/api/admin/client/:uuid/token`, `/api/admin/client/:uuid/token/rotate`, `/api/admin/client/:uuid/token/revoke`, and `/api/admin/client/:uuid/token/reissue`.
- `rotate` and `reissue` accept an optional JSON body like `{"expires_in_hours": 24}` to issue a token that expires automatically; `revoke` invalidates the current token immediately.
- After rotating or reissuing a token, redeploy the agent with the new credential before expecting it to reconnect.

## Contributing

Issues and Pull Requests are welcome!

## Acknowledgements

### 破碎工坊云

[破碎工坊云 - 专业云计算服务平台，提供高效、稳定、安全的高防服务器与CDN解决方案](https://www.crash.work/)

### DreamCloud

[DreamCloud - 极高性价比解锁直连亚太高防](https://as211392.com/)

### 🚀 Sponsored by SharonNetworks

[![Sharon Networks](https://raw.githubusercontent.com/komari-monitor/public/refs/heads/main/images/sharon-networks.webp)](https://sharon.io)

SharonNetworks 为您的业务起飞保驾护航！

亚太数据中心提供顶级的中国优化网络接入 · 低延时&高带宽&提供Tbps级本地清洗高防服务, 为您的业务保驾护航, 为您的客户提供极致体验. 加入社区 [Telegram群组](https://t.me/SharonNetwork) 可参与公益募捐或群内抽奖免费使用

### The open source software community

All the developers who submitted PRs and created themes

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=shaolonger/komari&type=Date)](https://www.star-history.com/#shaolonger/komari&Date)
