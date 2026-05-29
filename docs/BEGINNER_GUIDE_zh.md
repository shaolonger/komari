# Komari 新手一步步部署与使用指南

这份文档是写给第一次接触 Komari、第一次自托管监控面板、或者对 Docker / Linux 运维还不太熟的人。

如果你只想知道一件事：

1. 新手最推荐用 Docker 命名卷方案。
2. 面板先跑起来，再考虑 HTTPS、域名、反向代理。
3. 首次登录后第一时间改密码、开 2FA、确认备份方式。
4. 节点接入时优先使用面板生成的客户端 Token，不要手写旧的 URL Token 方式。

这篇文档会带你完成以下事情：

1. 选一种适合你的部署方式。
2. 把 Komari 面板跑起来。
3. 找到初始管理员密码并完成首次登录。
4. 做最基本但重要的安全设置。
5. 接入第一台节点。
6. 学会升级、备份和排错。

## 1. 先理解 Komari 是什么

Komari 是一个自托管的服务器监控面板。你可以把它理解成两部分：

1. 面板服务端：提供 Web 管理界面，保存数据，展示节点状态。
2. 被控节点 Agent：部署在你的服务器上，把状态、流量、负载等信息上报到面板。

你最终会得到一个网页面板，在里面查看各台服务器的 CPU、内存、磁盘、网络、在线状态等信息。

## 2. 部署前先做三个判断

### 2.1 你只是本地测试，还是要长期公网使用

如果你只是先在内网或测试机上体验：

1. 可以先用 HTTP。
2. 可以先用 IP + 端口访问。
3. 先把面板和第一台节点跑通。

如果你准备长期公网使用：

1. 建议准备域名。
2. 建议放在反向代理后面，用 HTTPS。
3. 建议限制管理面访问范围，不要把后台无防护暴露到公网。

### 2.2 你该选哪种部署方式

对新手来说，推荐顺序如下：

| 部署方式 | 适合谁 | 推荐程度 |
| --- | --- | --- |
| Docker 命名卷 | 大多数新手、想少踩权限坑的人 | 最推荐 |
| Docker 绑定宿主机目录 | 想直接看到本地数据目录的人 | 推荐 |
| 一键安装脚本 | 有 Linux 服务器、使用 systemd 的人 | 推荐 |
| 手工编译 | 开发者、想自己构建的人 | 仅开发场景 |

### 2.3 你至少需要准备什么

开始前请准备：

1. 一台运行 Komari 面板的服务器或虚拟机。
2. 该机器可以访问外网，或者至少能被你的节点访问到。
3. 一种运行方式：Docker，或 Linux + systemd，或手工二进制。
4. 一个浏览器。

如果你打算长期公网使用，再额外准备：

1. 一个域名。
2. 一套反向代理，例如 Caddy、Nginx、Nginx Proxy Manager、Traefik 等。
3. HTTPS 证书。

## 3. 最推荐的方案：Docker 命名卷一步步部署

这是最适合新手的方案。原因很简单：

1. 不需要自己处理 systemd。
2. 不需要关心程序文件放哪里。
3. 命名卷通常比手动绑定目录更不容易踩权限坑。

### 第 1 步：确认 Docker 已安装

在服务器上执行：

```bash
docker --version
```

如果能看到版本号，说明 Docker 已安装。

如果没有安装，请先安装 Docker，再继续下面的步骤。

### 第 2 步：启动 Komari 容器

执行：

```bash
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest
```

说明：

1. `-p 25774:25774` 表示把面板暴露在宿主机的 `25774` 端口。
2. `-v komari-data:/app/data` 表示使用 Docker 命名卷保存数据。
3. `--name komari` 给容器起一个容易记的名字。

### 第 3 步：确认容器已经启动

执行：

```bash
docker ps
```

你应该能看到一个名字叫 `komari` 的容器，状态类似 `Up ...`。

如果没看到，执行：

```bash
docker logs komari
```

注意：现在默认管理员密码不会写进容器持久日志里，但如果容器启动失败，普通错误信息仍然会出现在这里。

### 第 4 步：读取初始管理员密码

首次启动且没有提前自定义密码时，Komari 会把随机初始密码写到容器内部的临时文件：

```bash
docker exec komari cat /app/data/init_password.txt
```

记下这个密码。

默认用户名是：

```text
admin
```

### 第 5 步：在浏览器打开面板

浏览器访问：

```text
http://你的服务器IP:25774
```

例如：

```text
http://203.0.113.10:25774
```

### 第 6 步：首次登录

使用下面的信息登录：

1. 用户名：`admin`
2. 密码：刚才从 `init_password.txt` 读到的密码

### 第 7 步：确认密码临时文件已自动删除

首次登录成功后，Komari 会自动删除初始密码文件。

你可以再次执行：

```bash
docker exec komari ls /app/data
```

正常情况下，`init_password.txt` 应该已经不存在。

### 第 8 步：如果你想自己指定初始账号密码

你也可以在第一次启动容器时直接指定：

```bash
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD='请替换成一个强密码' \
  --name komari \
  ghcr.io/shaolonger/komari:latest
```

这样就不需要再去读取 `init_password.txt`。

## 4. Docker 绑定宿主机目录方案

如果你更喜欢把数据直接放在宿主机当前目录，也可以这样做。

### 第 1 步：创建数据目录

```bash
mkdir -p ./data
chown -R 10001:10001 ./data
```

这一步很重要。

Komari 的官方容器现在默认以非 root 用户运行。如果你不把目录权限给对，容器可能没有权限写入数据库和数据文件。

### 第 2 步：启动容器

```bash
docker run -d \
  -p 25774:25774 \
  -v $(pwd)/data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest
```

### 第 3 步：读取初始密码

```bash
cat ./data/init_password.txt
```

后续登录流程和上一节完全一样。

## 5. Linux 一键安装脚本方案

如果你有一台 Ubuntu / Debian 一类使用 systemd 的机器，也可以用项目自带的一键安装脚本。

### 第 1 步：下载脚本并运行

```bash
curl -fsSL https://raw.githubusercontent.com/shaolonger/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

当前这份安装脚本默认会从 `shaolonger/komari` 的 GitHub Releases 下载服务端二进制。如果你维护的是别的 fork，可以先设置 `KOMARI_RELEASE_REPO=<owner>/<repo>` 再执行脚本。

如果你安装完成后执行 `sudo journalctl -u komari -f` 看到 `Exec format error`，大概率是你本地拿到的是旧版安装脚本，之前它可能把错误页面当成二进制写进了 `/opt/komari/komari`。重新下载最新脚本并再次执行即可。

### 第 2 步：根据提示输入监听端口

默认端口是 `25774`。

如果你不确定，就直接回车使用默认值。

### 第 3 步：安装完成后记下脚本输出的初始密码

当前安装脚本会尽量在安装结束时把初始密码直接显示给你；如果因为启动时序等原因没有直接显示出来，你也可以手动读取：

```bash
sudo cat /opt/komari/data/init_password.txt
```

这个文件不会在安装阶段被脚本删除，而是在你第一次成功登录后台后由服务端自动删除。

所以你要做的事情是：

1. 安装成功后马上把密码记下来。
2. 如果脚本没有直接显示，就手动读取 `/opt/komari/data/init_password.txt`。
3. 立刻登录后台。
4. 登录后马上改成你自己的强密码。

### 第 4 步：常用服务命令

脚本安装完成后，常用命令如下：

```bash
systemctl status komari
systemctl restart komari
journalctl -u komari -f --no-pager
```

### 第 5 步：数据保存在哪里

脚本默认安装目录和工作目录是：

```text
/opt/komari
```

数据库、主题、初始密码临时文件等数据都在这个目录下的 `data` 子目录中。

## 6. 二进制直接运行方案

如果你不想用 Docker，也不想用一键安装脚本，可以直接运行二进制。

### 第 1 步：下载二进制

从 Release 页面下载对应系统的可执行文件：

```text
https://github.com/shaolonger/komari/releases
```

### 第 2 步：赋予执行权限

```bash
chmod +x komari
```

### 第 3 步：启动服务

```bash
./komari server -l 0.0.0.0:25774
```

### 第 4 步：读取初始密码

第一次启动后，如果你没有指定 `ADMIN_PASSWORD`，初始密码会写入：

```text
./data/init_password.txt
```

读取后用 `admin` 登录。

### 第 5 步：如果你想重置密码

如果你忘记了管理员密码，可以使用内置命令强制重置：

```bash
./komari chpasswd -p '你的新密码'
```

重置后需要重启服务。

如果你是通过 systemd 或 Docker 运行的，请根据实际方式重启。

### 第 6 步：如果你被 2FA 锁在门外

你也可以强制关闭 2FA：

```bash
./komari disable-2fa
```

执行后重启服务，再重新登录并按正确方式配置 2FA。

## 7. 首次登录后必须马上做的事情

很多新手到这里就停了，但这其实只是开始。

### 7.1 先改管理员密码

无论你是用随机初始密码，还是自己设置的临时密码，第一次登录后都建议立刻修改。

原则很简单：

1. 长度尽量够长。
2. 不要和别的网站共用。
3. 不要保存在聊天记录里。

### 7.2 开启 2FA

如果你准备长期使用这个面板，建议开启 TOTP 2FA。

原因：

1. 即使密码泄露，也不至于立即被接管。
2. 这是对公网管理面最划算的一层保护。

### 7.3 想清楚管理面是否要暴露到公网

最稳妥的做法是：

1. 仅自己访问后台。
2. 用 VPN、内网穿透白名单或反向代理访问控制。
3. 不要把 `/api/admin/*` 毫无防护地暴露给所有人。

### 7.4 长期公网使用请上 HTTPS

如果你只是临时内网测试，HTTP 没问题。

如果要公网长期使用，请做这几件事：

1. 给面板配域名。
2. 放到反向代理后面。
3. 启用 HTTPS。
4. 如果你希望 HTTP 自动跳转 HTTPS，可以为 Komari 增加：

```bash
KOMARI_ENFORCE_HTTPS=true
```

Docker 例子：

```bash
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  -e KOMARI_ENFORCE_HTTPS=true \
  --name komari \
  ghcr.io/shaolonger/komari:latest
```

如果你使用反向代理，请确保它向后端传递正确的 `X-Forwarded-Proto: https`。

### 7.5 不要把面板和敏感代理节点混布在同一台机器上

如果你的服务器同时承担以下角色中的任意一种：

1. 代理落地机
2. 中转节点
3. 高敏感业务入口

那就不建议在同机直接部署 Komari 面板。

更好的做法是：

1. 单独放一台管理平面机器跑 Komari。
2. 受控节点只跑 Agent。

## 8. 如何接入第一台节点

这一节是新手最容易卡住的地方。

先说结论：

1. 最简单的是先在面板里创建一个节点，拿到 `Token`，再把这个 `Token` 填给 Agent。
2. 如果你以后要批量注册很多机器，再考虑 `Auto Discovery Key`。

### 8.1 方式 A：先在面板里创建节点

Komari 服务端支持创建客户端记录，并为它生成唯一 Token。

你可以在后台的客户端管理页面新增节点；从后端接口看，对应能力是：

1. 新增节点
2. 查看节点列表
3. 获取指定节点 Token

对新手来说，实际操作建议是：

1. 登录后台。
2. 进入客户端管理页面。
3. 点击新增节点。
4. 给节点起一个容易识别的名字，例如 `hk-vps-01`、`home-nas`、`office-gateway`。
5. 记下生成的 `Token`。

然后在目标机器上安装 Agent 时，把以下信息填进去：

1. 面板地址，例如 `https://monitor.example.com` 或 `http://你的IP:25774`
2. 节点 Token

### 8.2 方式 B：使用 Auto Discovery Key 自动注册

如果你准备批量接入节点，可以使用自动注册。

原理是：

1. 你先在面板配置 `Auto Discovery Key`。
2. Agent 首次启动时用它请求：

```text
POST /api/clients/register
```

并通过请求头传：

```text
Authorization: Bearer <你的AutoDiscoveryKey>
```

服务端会返回一个新的 `uuid` 和 `token`。

如果你要自己测试这个流程，可以直接这样请求：

```bash
curl -X POST \
  -H "Authorization: Bearer <你的AutoDiscoveryKey>" \
  "http://你的面板地址/api/clients/register?name=test-node"
```

成功后会返回类似：

```json
{
  "status": "success",
  "data": {
    "uuid": "...",
    "token": "..."
  }
}
```

### 8.3 Agent 的具体安装命令去哪里看

官方文档里其实已经给出了比较完整的 Agent 接入方式，只是信息分散在 `Agent 自动发现`、`非 Root 运行 Agent` 和 `Agent 开发` 等页面里。

对大多数新手来说，实际只需要记住三种接入方式：

1. 你已经在面板里手动创建了节点，并拿到了 `Token`。
2. 你在面板里配置了 `Auto Discovery Key`，想批量自动注册。
3. 你没有 root 权限，只能手动下载二进制并在用户空间运行。

#### 方式 A：你已经有节点 Token，直接安装 Agent

这是单机接入最直观的方式。

你先在 Komari 面板里新增节点，记下这个节点的 `Token`，然后在目标机器上执行 Agent 安装脚本。

注意：当前这套文档默认配套的是服务端仓库 `shaolonger/komari` 和 Agent 仓库 `shaolonger/komari-agent`。如果你维护的是别的 Agent fork，请把下面的安装脚本和 Release 链接统一替换成你自己的仓库地址。

Linux / macOS：

```bash
bash <(curl -sL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
  -e http://你的面板地址:25774 \
  -t 你的节点Token
```

如果你的面板已经启用了 HTTPS，把 `http://你的面板地址:25774` 改成正式的 HTTPS 地址，例如：

```bash
bash <(curl -sL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
  -e https://monitor.example.com \
  -t 你的节点Token
```

Windows PowerShell：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "iwr 'https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.ps1' -UseBasicParsing -OutFile 'install.ps1'; & '.\install.ps1' '-e' 'https://monitor.example.com' '-t' '你的节点Token'"
```

安全提示：上面的 `-t 你的节点Token` 更适合测试环境或一次性快速接入。正式生产部署建议先把 token 写入仅 owner / 服务账户可读的 `komari-agent.json`，再通过 `--config /path/to/komari-agent.json` 安装或启动，避免 token 进入 shell history、进程参数和运维审计日志。

安装脚本会自动：

1. 下载对应平台的 `komari-agent` 二进制。
2. 安装为系统服务。
3. 直接启动 Agent。

默认服务名通常是：

```text
komari-agent
```

#### 当前 `shaolonger/komari-agent` fork 必须额外记住的 5 件事

1. 默认只开启基础监控；远程终端、远程命令执行和 ping 探测都需要显式开启。
2. 要使用面板里的“延迟监测”，必须在 Agent 配置里设置 `enable_ping=true`，或者启动时传 `--enable-ping`。
3. `--ignore-unsafe-cert` 会禁用远程控制和 ping，所以它不能作为“为了让延迟监测跑起来而长期打开”的生产配置。
4. ping 默认只允许 `tcp,http` 两种类型、只允许 `80,443` 端口，并默认拒绝私有 / 环回 / 链路本地目标；如果你的探测目标不在这个范围内，需要显式放宽对应参数。
5. 如果同一台节点要同时执行多条相同 `interval` 的延迟监测任务，请提高 `max_concurrent_pings`，并把 `ping_min_interval_millis` 设为 `0`。当前 fork 中，这个值现在会真正关闭最小接收间隔，不会再回退到默认 `500ms`。另外，`max_control_requests` / `control_request_window` 现在只影响远程终端和远程命令执行，不再影响 ping。

#### 方式 B：使用 Auto Discovery Key 自动注册

这适合一口气接入很多机器，不想手动一个个创建设备的场景。

你先在面板里配置 `Auto Discovery Key`，然后在目标机器执行：

Linux / macOS：

```bash
bash <(curl -sL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
  -e https://your-komari-server.com \
  --auto-discovery your-ad-key
```

Windows PowerShell：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "iwr 'https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.ps1' -UseBasicParsing -OutFile 'install.ps1'; & '.\install.ps1' '-e' 'https://your-komari-server.com' '--auto-discovery' 'your-ad-key'"
```

这种方式下，Agent 会自动向面板注册自己，不需要你提前手动创建节点。

适合：

1. 批量部署多台 VPS。
2. 用脚本批量初始化机器。
3. 想统一下发一套安装命令。

#### 方式 C：没有 root 权限，手动下载二进制运行

如果你的环境不能用 root，或者不能创建 systemd 服务，可以按官方 `非 Root 环境下安装和运行 Komari Agent` 文档手动运行。

第 1 步：确认系统架构

```bash
uname -m
```

常见架构和文件名对应关系：

1. `x86_64` / `amd64` -> `komari-agent-linux-amd64`
2. `aarch64` / `arm64` -> `komari-agent-linux-arm64`
3. `i386` / `i686` -> `komari-agent-linux-386`
4. `armv7l` -> `komari-agent-linux-arm`

第 2 步：去当前 Agent 仓库的 Release 下载 Agent

```text
https://github.com/shaolonger/komari-agent/releases
```

第 3 步：给文件执行权限

```bash
chmod +x komari-agent
```

第 4 步：直接前台运行

```bash
./komari-agent -e https://monitor.example.com -t 你的节点Token
```

如果你还没上 HTTPS，也可以先临时使用：

```bash
./komari-agent -e http://你的IP:25774 -t 你的节点Token
```

第 5 步：放到后台保活

最简单的方式是 `nohup`：

```bash
nohup ./komari-agent -e https://monitor.example.com -t 你的节点Token > komari.log 2>&1 &
```

如果你经常用 SSH 登录机器，官方文档更推荐 `screen`：

```bash
screen -S komari-agent
./komari-agent -e https://monitor.example.com -t 你的节点Token
```

运行后按 `Ctrl+A` 再按 `D`，就能把会话挂到后台。

#### 生产环境常用参数

官方文档里比较实用的参数有这些：

1. `-e, --endpoint`：Komari 面板地址。
2. `-t, --token`：节点 Token。
3. `--auto-discovery`：自动发现密钥。
4. `--disable-web-ssh`：禁用远程控制功能，生产环境很常用。
5. `--interval`：监控数据上报间隔，单位秒，默认 `1.0`。
6. `--info-report-interval`：基础信息上报间隔，单位分钟，默认 `5`。
7. `--reconnect-interval`：断线重连间隔，单位秒，默认 `5`。
8. `--max-retries`：最大重试次数，默认 `3`。
9. `-u, --ignore-unsafe-cert`：忽略不安全证书，只建议测试环境临时使用；当前 fork 中它会直接禁用远程控制和 ping。
10. `--enable-ping`：显式开启面板“延迟监测”用到的 ping 探测能力。
11. `--allowed-ping-types`：允许的 ping 类型，默认只有 `tcp,http`。
12. `--allowed-ping-tcp-ports`：允许的 TCP / HTTP 探测端口，默认只有 `80,443`。
13. `--max-concurrent-pings`：单机允许的最大并发 ping 任务数。
14. `--ping-min-interval-millis`：同一台 Agent 接受两条 ping 任务之间的最小间隔。当前 fork 中，显式设为 `0` 就是关闭这个最小间隔。

一个更适合生产环境的 Linux / macOS 示例（假设你已经准备好受限权限的配置文件）：

```bash
bash <(curl -sL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
  --config /etc/komari/komari-agent.json \
  --disable-web-ssh \
  --enable-ping \
  --interval 5.0 \
  --max-concurrent-pings 24 \
  --ping-min-interval-millis 0 \
  --max-retries 5 \
  --reconnect-interval 10 \
  --info-report-interval 15
```

如果你准备在同一台节点上批量执行多条相同 `interval` 的 TCP / HTTP 延迟任务，推荐至少准备一份类似下面的配置文件：

```json
{
  "endpoint": "https://monitor.example.com",
  "token": "replace-with-real-token",
  "enable_ping": true,
  "allowed_ping_types": "tcp,http",
  "allowed_ping_tcp_ports": "80,443",
  "max_concurrent_pings": 24,
  "ping_min_interval_millis": 0
}
```

如果你的延迟任务目标是内网地址、私有负载均衡器或环回地址，还需要额外设置：

```json
{
  "allow_private_ping_targets": true
}
```

只有在你明确知道自己在探测什么、并且能接受相应的安全风险时，才建议放开这个选项。

#### 安装完成后怎么看状态和日志

如果你用了官方安装脚本并创建了 systemd 服务：

```bash
sudo systemctl status komari-agent
sudo journalctl -u komari-agent -f
```

如果你是手动非 root 运行：

```bash
ps aux | grep komari-agent
tail -f komari.log
```

#### 特殊场景去哪里看

如果你的环境比较特殊，可以继续看这些官方页面：

1. Auto Discovery 批量部署：`https://komari-document.pages.dev/install/agent-ad.html`
2. 非 root 运行 Agent：`https://komari-document.pages.dev/faq/agent-no-root.html`
3. NAS 场景：`https://komari-document.pages.dev/faq/agent-nas.html`
4. 卸载 Agent：`https://komari-document.pages.dev/faq/uninstall.html`
5. 社区维护的特殊平台 Agent：`https://komari-document.pages.dev/community/agent.html`

### 8.4 一个非常重要的兼容点

对普通用户来说，使用官方 Agent 时只需要传启动参数：

1. `-e/--endpoint`
2. `-t/--token` 或 `--auto-discovery`

你不需要自己手写 `ws://.../api/clients/report?...` 或 `POST /api/clients/...` 这类底层请求。

只有在你自己编写兼容 Agent、脚本或 SDK 时，才需要关心底层认证格式。对这种自定义实现，请优先按当前服务端要求使用：

```text
Authorization: Bearer <token>
```

## 9. 如何确认节点已经接入成功

当第一台节点配置完成后，回到面板检查以下几件事：

1. 节点是否出现在列表中。
2. 在线状态是否正常。
3. CPU、内存、磁盘、网络是否开始刷新。
4. 节点名称是不是你预期的那台机器。

如果节点已创建但一直不在线，优先检查：

1. 面板地址是否填错。
2. 端口是否被防火墙拦截。
3. Token / Auto Discovery Key 是否填错。
4. 如果走 HTTPS，证书是否有效。
5. 如果走反向代理，是否正确转发到 Komari。

## 10. 公网部署推荐做法

如果你想把 Komari 长期放到公网，建议采用下面这个思路：

1. Komari 只监听在内网地址或宿主机本地端口。
2. 外面放一个反向代理负责 HTTPS。
3. 后台只给自己访问，不对所有来源开放。
4. 打开 2FA。

一个很典型的部署结构是：

```text
浏览器 -> HTTPS 反向代理 -> Komari:25774
节点 Agent -> 你的域名 -> HTTPS 反向代理 -> Komari:25774
```

## 11. 升级、重启和备份

### 11.1 Docker 升级

最稳妥的流程：

```bash
docker pull ghcr.io/shaolonger/komari:latest
docker rm -f komari
```

然后用你原来的 `docker run` 命令重新启动。

如果你用了命名卷或绑定目录，原有数据会保留。

### 11.2 一键脚本安装的升级

安装脚本本身带升级菜单。

你可以再次运行：

```bash
sudo ./install-komari.sh
```

然后选择：

```text
2) 升级 Komari
```

### 11.3 二进制升级

步骤通常是：

1. 先备份 `data` 目录。
2. 下载新版二进制。
3. 覆盖旧程序。
4. 重启服务。

### 11.4 已安装 Agent 如何升级到当前修复版

如果你的 Agent 已经装在 VPS 上，推荐按下面的方式升级：

Linux / macOS：

1. 先确认现有配置文件确实存在；如果存在，再备份，例如：`sudo cp /opt/komari/komari-agent.json /opt/komari/komari-agent.json.bak`
2. 如果 `/opt/komari/komari-agent.json` 已存在，优先重新执行当前 fork 的安装脚本，并直接复用已有配置文件，而不是再次把 token 写进命令行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/shaolonger/komari-agent/refs/heads/main/install.sh) \
  --config /opt/komari/komari-agent.json \
  --enable-ping \
  --max-concurrent-pings 24 \
  --ping-min-interval-millis 0
```

3. 如果这个配置文件根本不存在，不要把这个路径直接传给 `--config`；请先恢复备份，或者改为传入你当前面板的 `--endpoint` 和新 token，让安装脚本重新生成配置文件
4. 安装脚本会替换二进制、重建服务定义并重新启动 `komari-agent`
5. 升级后马上检查：

```bash
sudo systemctl status komari-agent
sudo journalctl -u komari-agent -n 100 -f
```

Windows：

1. 以管理员身份备份 `%ProgramFiles%\Komari\komari-agent.json`
2. 重新执行 `install.ps1`，优先继续用 `--config` 指向现有配置文件
3. 安装脚本会通过 NSSM 重建并重启 `komari-agent` 服务
4. 升级后确认配置文件里已经包含你要的 `enable_ping`、`max_concurrent_pings`、`ping_min_interval_millis` 等参数

如果你是手工管理二进制，则直接替换旧 Agent 二进制并重启服务即可，但同样建议先备份配置文件。

### 11.5 备份什么最重要

最重要的是你的数据目录。

不同部署方式下通常分别是：

1. Docker 命名卷：`komari-data`
2. Docker 绑定目录：你自己的 `./data`
3. 脚本安装：`/opt/komari/data`
4. 二进制直跑：运行目录下的 `./data`

如果你只想做最朴素的备份，直接备份这个目录就够了。

## 12. 常见问题排查

### 12.1 打不开面板

先检查：

1. 服务是否在运行。
2. 端口 `25774` 是否监听。
3. 防火墙和云安全组是否放行。
4. 你访问的 IP 和端口是否写对。

### 12.2 Docker 容器起不来，提示权限问题

大概率是你用了绑定目录，但没有给 `10001:10001` 权限。

重新执行：

```bash
chown -R 10001:10001 ./data
```

然后重启容器。

### 12.3 我找不到 `init_password.txt`

常见原因有四种：

1. 你已经成功登录过一次，文件被自动删除了。
2. 你启动时已经通过 `ADMIN_PASSWORD` 指定了密码。
3. 你使用的是一键安装脚本，但这是旧版本文档或旧脚本留下的印象；当前行为是首次成功登录后才自动删除。
4. 容器或程序根本没有成功完成第一次初始化。

### 12.4 开了 HTTPS 之后出现跳转循环

优先检查：

1. 反向代理有没有正确设置 `X-Forwarded-Proto: https`。
2. 你是不是同时在代理和后端做了互相冲突的跳转。
3. 只有在确定已经走 HTTPS 代理时，再开启 `KOMARI_ENFORCE_HTTPS=true`。

### 12.5 我忘记管理员密码了

如果你还能登录，就在后台直接修改。

如果你已经完全进不去，可以用：

```bash
./komari chpasswd -p '你的新密码'
```

然后重启服务。

### 12.6 我忘记了 2FA

使用：

```bash
./komari disable-2fa
```

然后重启服务，再重新配置 2FA。

### 12.7 延迟监测任务加了但没结果，或者只出来一部分结果

先在 Agent 所在机器上看日志：

```bash
sudo journalctl -u komari-agent -n 100 -f
```

如果你看到这些日志，通常分别代表：

1. `ping capability is disabled`
说明你没有显式开启 ping。把 Agent 配置里的 `enable_ping` 设为 `true`，或启动参数加上 `--enable-ping`。

2. `ping port ... is not allowed`
说明目标端口不在白名单里。把 `allowed_ping_tcp_ports` 加上你要探测的端口。

3. `ping target ... resolves to a restricted address`
说明目标是私有 / 环回 / 链路本地地址。只有在你明确需要探测这些目标时，才把 `allow_private_ping_targets` 设为 `true`。

4. `ping rate limit reached`
说明同一台节点在同一轮里收到了多条 ping 任务，而 `ping_min_interval_millis` 还不合适。当前 fork 中，显式设为 `0` 才是关闭这个最小间隔。

5. `concurrent ping limit reached`
说明同一轮同时执行的 ping 数已经超过 `max_concurrent_pings`。把它调大到不小于同一台节点同一时刻会收到的任务数。

如果你批量添加的是“同一台节点、多个相同 interval 的 TCP-Ping 任务”，最常见的最小可用配置通常是：

```json
{
  "enable_ping": true,
  "max_concurrent_pings": 24,
  "ping_min_interval_millis": 0
}
```

升级到当前修复版后，`max_control_requests` / `control_request_window` 不再影响 ping，所以如果你仍然看到问题，优先检查的就应该是 `enable_ping`、端口白名单、私网目标限制和并发 / 最小间隔这几项。

## 13. 给新手的最终建议

如果你还在犹豫怎么开始，直接照下面做：

1. 用 Docker 命名卷把面板跑起来。
2. 用 `admin + init_password.txt` 登录。
3. 马上改密码。
4. 马上开 2FA。
5. 先接入第一台测试节点。
6. 等确认流程跑通，再去做 HTTPS、域名和公网访问。

如果你只是为了尽快上手，不要一开始就同时折腾：

1. Docker
2. 反向代理
3. 域名
4. HTTPS
5. 自动注册
6. 批量接入

把这些事情分成两阶段做，成功率会高很多：

1. 第一阶段：先在内网或测试环境跑通。
2. 第二阶段：再做长期公网部署与安全加固。

## 14. 继续往下看什么

当你已经完成本文步骤后，下一步建议看：

1. 官方文档首页：<https://komari-document.pages.dev/>
2. Agent 文档：<https://komari-document.pages.dev/dev/agent.html>
3. 主题开发文档：<https://komari-document.pages.dev/dev/theme.html>

如果你准备把它用于长期公网或高敏感环境，再额外关注：

1. HTTPS 和反向代理配置
2. 2FA
3. 节点与面板分机部署
4. 备份策略
5. 管理面访问控制