# Komari 规模化存储运行手册

本手册描述 K-601/K-602 存储架构、SQLite 默认模式、可选 PostgreSQL 控制面与 ClickHouse 遥测面的安全启用、迁移、验证和回滚。完整设计见 `PERFORMANCE_REFACTOR_PLAN.md`，执行状态见 `PERFORMANCE_REFACTOR_TODOLIST.md`。

## 1. 默认行为

未设置任何新环境变量时：

- `KOMARI_CONTROL_STORE=sqlite`；
- `KOMARI_TELEMETRY_STORE=sqlite`；
- 不连接 PostgreSQL 或 ClickHouse；
- 不改变现有数据库文件、备份、迁移和单机部署方式。

规模化后端是显式 opt-in。配置错误、TLS 降级、迁移失败、空 PostgreSQL 与已有 SQLite 用户冲突、健康检查失败都会阻止启动；系统不会静默退回另一个真相源。

默认 Lite 二进制不链接 pgx/ClickHouse driver。需要规模化后端时构建：

```sh
go build -tags scale -o komari-scale .
```

Lite 二进制收到 PostgreSQL/ClickHouse 配置会明确拒绝启动，不会忽略配置。

## 2. 数据职责

### 2.1 PostgreSQL 控制面

PostgreSQL 保存鉴权必须强一致的最小状态：

- User UUID、用户名、密码哈希、SSO、2FA；
- Client UUID、Token、签发/过期/吊销时间；
- Session 的 SHA-256 digest、User UUID、过期时间。

PostgreSQL 不保存 Session 明文。启用后，密码校验、2FA、SSO、Token、Session 回源都以 PostgreSQL 为真相源。创建、轮换、吊销、删除等安全变更先提交 PostgreSQL，随后镜像到 SQLite 低频元数据；缓存只在权威提交后失效。SQLite 镜像使控制面可以执行受控回滚，但不会反过来覆盖非空 PostgreSQL。

### 2.2 ClickHouse 遥测面

ClickHouse 保存 Record、GPURecord 和 PingRecord。每次批写有稳定 batch ID：

- 调用方可传不超过 128 字节的显式 ID；
- 未传时按实际持久化批次计算 SHA-256；
- 重试对每张表使用固定 `insert_deduplication_token`；
- `ReplacingMergeTree` 以 `(业务主键,batch_id)` 长期去重；
- 查询使用 `FINAL`，即使重试超出 ClickHouse 的短期 deduplication window 也不会返回重复数据。

跨表写入不伪装成数据库事务。任一表失败时整批返回失败，调用方用同一 ID 重试；已成功的表由幂等键消除重复。范围查询有 100,000 点硬上限；单节点长窗口会在 ClickHouse 内按预算自动聚合，请求指标继续保留峰值。

## 3. PostgreSQL 配置

```text
KOMARI_CONTROL_STORE=postgres
KOMARI_POSTGRES_URL=postgres://user:password@db.example:5432/komari?sslmode=verify-full&sslrootcert=/run/secrets/postgres-ca.pem
KOMARI_POSTGRES_MAX_CONNS=32
KOMARI_POSTGRES_MIN_CONNS=2
```

连接池硬限制 1～256，默认最大 32、最小 2；连接 lifetime、idle 和健康检查由适配器设置安全默认值。`application_name` 固定为 `komari-control`。

生产默认强制 TLS，且拒绝 pgx 的 plaintext fallback。只有本机隔离测试可显式设置：

```text
KOMARI_POSTGRES_ALLOW_INSECURE=true
```

不要在共享网络、容器集群或生产环境使用该开关。

### 3.1 首次迁移

PostgreSQL 为空而 SQLite 已有用户时，启动默认失败。确认目标库确实为空并完成备份后，仅第一次设置：

```text
KOMARI_POSTGRES_BOOTSTRAP_FROM_SQLITE=true
```

迁移在 PostgreSQL advisory transaction lock 下创建 versioned schema，并一次性复制 User、可认证 Client 和 Session digest。目标库非空时拒绝 bootstrap，避免旧 SQLite 覆盖权威数据。成功启动后应移除该变量。

## 4. ClickHouse 配置

```text
KOMARI_TELEMETRY_STORE=clickhouse
KOMARI_CLICKHOUSE_ADDRS=ch-1.example:9440,ch-2.example:9440
KOMARI_CLICKHOUSE_DATABASE=komari
KOMARI_CLICKHOUSE_USERNAME=komari
KOMARI_CLICKHOUSE_PASSWORD=<secret>
KOMARI_CLICKHOUSE_TABLE_PREFIX=komari_
KOMARI_CLICKHOUSE_MAX_CONNS=32
KOMARI_CLICKHOUSE_IDLE_CONNS=8
KOMARI_CLICKHOUSE_TLS_SERVER_NAME=clickhouse.example
KOMARI_CLICKHOUSE_TLS_CA=/run/secrets/clickhouse-ca.pem
KOMARI_CLICKHOUSE_TLS_CERT=/run/secrets/clickhouse-client.pem
KOMARI_CLICKHOUSE_TLS_KEY=/run/secrets/clickhouse-client-key.pem
```

地址必须是 `host:port`，禁止 URL path/query。表前缀只允许小写字母、数字和下划线。TLS 最低 1.2，使用系统根证书或指定 CA；客户端证书和私钥必须同时配置。生产默认不允许 plaintext，只有本机隔离测试可显式设置：

```text
KOMARI_CLICKHOUSE_ALLOW_INSECURE=true
```

迁移幂等创建 versioned migration、Record、GPU 和 Ping 表。连接使用 native protocol、LZ4、round-robin、多主机故障转移和有界连接池；密码、地址、证书路径不会进入健康响应。

## 5. 上线顺序

1. 备份 SQLite 和目标数据库。
2. 先在预生产运行 `scripts/test-scale-storage.sh`。
3. 只切 PostgreSQL 控制面，完成密码、2FA、OAuth、Session、Token 创建/轮换/吊销验证。
4. 删除一次性 bootstrap 变量并重启，验证不再依赖迁移源。
5. 再切 ClickHouse 遥测面，观察 batch retry、查询点数、p99 和存储增长。
6. 验证 `/health` 所依赖的内部存储检查，但不要把 DSN、Token 或证书信息放入监控标签。

不要在同一次不可回滚操作中同时切换两个后端。

## 6. 故障与回滚

- PostgreSQL 暂时断线：认证 cache hit 可继续到自身安全 TTL；cache miss 和安全变更失败关闭，不使用 SQLite 绕过权威状态。连接池恢复后自动重连。
- ClickHouse 暂时断线：批写返回错误并用同一 batch ID 有界重试；不静默丢弃。恢复后驱动连接池自动重连。
- 控制面回滚：停止写流量，确认 SQLite 镜像已完成，移除 PostgreSQL 配置并设置 `KOMARI_CONTROL_STORE=sqlite`，再启动和验证全部凭据生命周期。
- 遥测面回滚：设置 `KOMARI_TELEMETRY_STORE=sqlite` 可恢复采集，但切换期间只写入 ClickHouse 的历史不会自动出现在 SQLite。需要历史连续性时，应先导出/回灌或保留 ClickHouse 只读查询窗口。

迁移失败不得手工删除 migration 表后重试。先保存数据库日志和 schema，修复根因后重复运行幂等迁移。

## 7. 测试

本机需要 Docker/OrbStack：

```sh
./scripts/test-scale-storage.sh
```

脚本使用临时证书和 tmpfs 容器，测试结束自动删除容器、卷、网络和证书。覆盖：

- PostgreSQL/ClickHouse 共用 contract suite；
- TLS verify-full/CA 校验；
- schema 重复/并发迁移；
- PostgreSQL backend termination 与连接恢复；
- ClickHouse 容器停止、健康失败、重启和连接恢复；
- batch ID 重复写入、事务/部分失败语义和查询硬上限；
- 128×20 次并发凭据读取、32 路写契约和 10,000 行遥测批次；
- 应用层密码、2FA、Session、Token 轮换/吊销完整生命周期。
