# Komari 可复现发布、PGO 与性能门禁

本文档定义 Komari 服务端从源码、前端静态资源到 Release 二进制和容器镜像的完整构建合同。目标不是“在某台机器上成功编译”，而是让同一源码输入得到可验证、可复现、可回滚且持续受性能门禁保护的产物。

## 1. 不可变输入

所有需要联网获取的构建输入集中在 [`build/versions.env`](build/versions.env)：

| 输入 | 固定方式 | 目的 |
|---|---|---|
| Go | 精确补丁版本，并与 `go.mod` 完全一致 | 获得当前安全修复，消除编译器漂移 |
| Node.js | 精确 LTS 补丁版本 | 避免已 EOL 的 Node 23 和 major tag 漂移 |
| `komari-web` | 完整 40 位 commit | 禁止默认分支 HEAD 漂移 |
| 前端原始 lockfile | SHA-256 | 验证该 commit 的依赖图未被替换 |
| 前端有效 lockfile | 安全补丁 + 完整结果 SHA-256 | 在不移动前端 commit 的情况下固定已修复依赖图 |
| 前端源码纪元 | commit Unix 时间 | 删除 Vite 注入当前时间造成的 chunk hash 漂移 |
| Zig | 精确版本 + 官方归档 SHA-256 | 防止交叉编译器归档被替换 |
| `govulncheck` | 精确 module 版本 | 安全扫描器自身也不可漂移 |
| Alpine | OCI manifest digest | Docker 基础镜像不可被同名 tag 替换 |
| GitHub Actions | 完整 40 位 commit | 防止 action major tag 被移动或供应链劫持 |

修改任意版本时，必须在同一个提交中更新对应摘要、验证记录和本文档。禁止把 `latest`、分支名、浮动 major tag 或无摘要下载 URL 加回发布链。

## 2. 前端构建

入口：

```bash
./scripts/build-frontend.sh
```

构建器按以下顺序 fail closed：

1. 只 fetch `FRONTEND_COMMIT`，不读取默认分支；
2. 验证 checkout 的完整 commit；
3. 验证 commit timestamp 与 `FRONTEND_SOURCE_DATE_EPOCH`；
4. 从 Git object archive 创建干净构建目录，不信任调用方工作树中的未跟踪文件；
5. 验证上游 `package-lock.json` SHA-256；
6. 应用仓库内审阅过的 [`build/frontend-build-security.patch`](build/frontend-build-security.patch) 与 [`build/frontend-security.patch`](build/frontend-security.patch)；
7. 验证安全更新后的完整 lockfile SHA-256；
8. 将上游 `new Date()` 构建时间替换成固定源码纪元；匹配不是恰好一次就拒绝构建；
9. 执行 `npm ci --ignore-scripts`，禁止 install lifecycle 脚本；
10. 对生产依赖和构建依赖执行 high 级 `npm audit`；
11. 构建后再次验证 lockfile 未被 npm 修改；
12. 把 commit、有效 lockfile 摘要和源码纪元写入静态产物 provenance。

安全补丁只更新 lockfile 中受到已公开高危通告影响的 `http-proxy-middleware` 和 Vite，保持 `package.json` 的既有 semver 合同。每次升级前端 commit 时，应优先删除已被上游吸收的补丁；如果补丁不再精确适用，构建会自动失败。

完全离线的第二次安装/构建：

```bash
FRONTEND_SOURCE_DIR=/path/to/pinned-checkout \
KOMARI_FRONTEND_OFFLINE=1 \
./scripts/build-frontend.sh /tmp/theme
```

综合验证：

```bash
./scripts/test-frontend-reproducible.sh
```

该测试先进行一次联网的锁定安装，再使用 npm cache 完全离线安装；两个输出目录按相对路径逐文件比较 SHA-256。任何当前时间、依赖解析、构建顺序或未锁定输入造成的差异都会失败。

## 3. Go Release 构建

唯一入口：

```bash
VERSION=v1.3.0 \
VERSION_HASH="$(git rev-parse HEAD)" \
./scripts/build-release.sh ./komari
```

固定属性：

- `-mod=readonly`：构建不得修改 `go.mod`/`go.sum`；
- `-trimpath`：删除本机绝对源码路径；
- `-buildvcs=false`：不混入随工作树变化的隐式 VCS 元数据；
- `-buildid=`：删除 Go 随构建生成的 build ID；
- `-s -w`：发布产物不包含调试符号和本机 DWARF 路径；
- `VERSION` 和完整 `VERSION_HASH` 是唯一显式版本输入；
- 默认显式使用仓库内固定的 PGO profile；
- `KOMARI_REQUIRE_EXACT_GO=1` 可强制编译器与版本清单逐补丁一致。

同一平台、编译器、源码、前端和环境参数是复现边界。跨 OS、跨 libc 或跨交叉链接器的二进制不承诺逐字节相同，但每个目标自身必须可重复。

验证：

```bash
GOTOOLCHAIN=go1.25.12 \
KOMARI_REQUIRE_EXACT_GO=1 \
./scripts/test-reproducible-build.sh
```

测试会连续进行两次 PGO 构建并比较 SHA-256 与 `go version -m`，检查绝对路径、VCS 元数据和 build ID 均不存在，再分别启动 PGO on/off 二进制执行 CLI smoke test。

## 4. PGO

发布 profile 位于 `build/pgo/default.pgo`。它来自真实遥测接收热路径，而不是微型空循环：

- 已认证 JSON v1 解码；
- WebSocket JSON v1 解码；
- 二进制 Telemetry v2 解码。

重新生成：

```bash
GOTOOLCHAIN=go1.25.12 \
PGO_BENCHTIME=5s \
./scripts/generate-pgo.sh
```

生成脚本先用 `go tool pprof` 验证 profile 可读，再原子地安装到目标路径。profile 是发布输入，必须随生成它的源码一起提交。PGO 不改变协议、安全检查、大小限制和鉴权路径；它只向 Go 编译器提供生产热函数权重。

`PGO_PROFILE=off ./scripts/build-release.sh ...` 仅用于对照和紧急回滚。正常 Release 不允许关闭 PGO。

## 5. 受控性能基线

性能门禁不把某一台开发机的绝对 `ns/op` 写死，因为 CPU 型号、频率、虚拟化和系统负载会造成错误告警。CI 会在同一个 GitHub Runner 上顺序构建 base revision 和 candidate revision，再比较每个 benchmark 的中位数：

- `ns/op`：允许最多 20% 噪声/回退；
- `B/op`：允许最多 2%；
- `allocs/op`：不允许回退；
- 每个指标至少 5 个样本；
- candidate 缺 benchmark、base 缺 benchmark或样本不足都失败。

覆盖的代表性热路径：

- Telemetry v2 解码；
- 分片分钟聚合；
- 512 KiB 历史响应缓存命中；
- 凭据摘要缓存命中；
- 10,000 定时任务稳定 next-run；
- 静态资源 manifest 查找；
- 10,000 节点 dashboard 单 delta。

本地执行：

```bash
./scripts/run-performance-gate.sh /path/to/base-checkout
```

比较器 [`tools/benchguard`](tools/benchguard) 只读取标准 Go benchmark 输出，不需要临时下载 `benchstat` 或第三方解析器。CI 配置在 [`.github/workflows/performance.yml`](.github/workflows/performance.yml)。

## 6. 供应链与 CI 门禁

```bash
./scripts/verify-supply-chain.sh
KOMARI_RUN_VULN_CHECK=1 ./scripts/verify-supply-chain.sh
```

门禁检查：

- Go 与版本清单对齐；
- 所有 commit/digest 格式完整；
- Docker `FROM` 使用 digest；
- PGO profile 存在且非空；
- 所有 workflow action 使用 commit SHA；
- workflow 中不存在 `npm install`、前端默认分支 clone、Go 1.23 或 Node 23；
- `go mod verify`；
- `go list -mod=readonly -m all`；
- 可选的固定版本 `govulncheck`。

CI 的质量工作流还执行 default/scale 两种构建测试、全量 race、vet、前端在线/离线复现、PGO 再生成验证和二进制重复哈希。

## 7. Release 资产

tag/Release 触发 [`.github/workflows/release.yml`](.github/workflows/release.yml)，为以下目标生成 PGO + CGO 静态产物：

- Linux: `386`, `amd64`, `arm64`, `riscv64`;
- Windows: `386`, `amd64`, `arm64`.

每个二进制都附带独立 `.sha256`。工作流在开始前检查全部 14 个资产；同一 tag 的 tag/release 双事件通过 concurrency 串行化，后启动者会在资产齐全时安全跳过。

容器 Release 使用同一批固定输入和 PGO 二进制，输出 amd64/arm64 镜像，启用 BuildKit provenance 和 SBOM。镜像仍以非 root UID 10001 运行，安全模型不因构建优化改变。

## 8. 升级与回滚

版本升级流程：

1. 在隔离目录检查新版本和官方安全公告；
2. 更新 `build/versions.env`；
3. 更新并验证相关 SHA-256；
4. 对前端执行在线/离线双构建和 high 级 audit；
5. 重新生成 PGO；
6. 执行 Go default/scale、race、vet、reproducible build 和性能 base/candidate 对照；
7. 独立提交，保留旧 commit 作为可直接回滚点。

紧急回滚只需回滚版本提交并重新创建新 SemVer patch Release；不得覆盖既有 tag 或用 `--clobber` 改写历史 Release 的源码指向。
