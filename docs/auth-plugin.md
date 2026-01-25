# 认证插件（PostgreSQL）当前实现说明

本文档描述 `auth-plugin` 的当前实现，内容以源码为准（`authplugin/bridge.c`、`authplugin/plugin.go`）。

当前功能范围（实现层面）：仅处理 CONNECT 认证（BASIC_AUTH），ACL 未启用；认证数据来源 PostgreSQL，不经 HTTP；每次认证结果写入 `mqtt_client_auth_events`，登录成功时写入连接事件表。

## 1. 组件与职责

### 1.1 C 桥接层（`authplugin/bridge.c`）

- 提供 Mosquitto 要求的三个入口函数：
  - `mosquitto_plugin_version`
  - `mosquitto_plugin_init`
  - `mosquitto_plugin_cleanup`
- 入口函数仅转发到 Go 侧（`go_mosq_*`），避免 Go 导出符号与官方符号冲突。
- C 侧包装函数：
  - `register_event_callback` / `unregister_event_callback`：封装 `mosquitto_callback_register` / `mosquitto_callback_unregister`。
  - `go_mosq_log`：封装 `mosquitto_log_printf`（避免 Go 直接处理 C 变参）。

### 1.2 Go 插件（`authplugin/plugin.go`）

- 实现 BASIC_AUTH 认证逻辑。
- 提供事件回调：`basic_auth_cb_c` 与 `acl_check_cb_c`（当前 **仅注册** BASIC_AUTH）。
- 管理 PostgreSQL 连接池（`pgxpool`），并处理连接与查询超时。
- 写入认证事件表 `mqtt_client_auth_events`。

### 1.3 CLI 工具（`cmd/bcryptgen`）

- 名称为 `bcryptgen`，但**实际算法是 sha256(password + salt)**，输出十六进制字符串。
- 参数：`-salt` 指定盐值；未提供密码时从 stdin 读取。

## 2. 运行时流程

### 2.1 版本协商

- `go_mosq_plugin_version` 只接受 Mosquitto 插件 API **v5**。
- 若 Mosquitto 不支持 v5，则返回 `-1`。

### 2.2 初始化（`go_mosq_plugin_init`）

1. 读取配置：
   - 先读取环境变量 `PG_DSN`。
   - 再读取 `plugin_opt_*` 选项：
     - `pg_dsn`
     - `timeout_ms`
     - `fail_open`
     - `enforce_bind`
2. 校验与日志：
   - `pg_dsn` 为空直接返回错误。
   - `pg_dsn` 写日志时遮盖密码（`xxxxx`）。
3. 连接池配置（见 3.1），并尝试首次连接：
   - 若首次连接失败：记录 warning，插件仍然继续加载（延迟重试）。
4. 注册事件回调：
   - 仅注册 `MOSQ_EVT_BASIC_AUTH`，**未注册 `MOSQ_EVT_ACL_CHECK`**。

### 2.3 清理（`go_mosq_plugin_cleanup`）

- 取消 `MOSQ_EVT_BASIC_AUTH` 回调注册。
- 关闭连接池。

## 3. PostgreSQL 相关实现

### 3.1 连接池配置（`poolConfig`）

- `MaxConns = 16`
- `MinConns = 2`
- `MaxConnIdleTime = 60s`
- `HealthCheckPeriod = 30s`

连接池创建时会 `Ping`；失败则关闭并返回错误。

### 3.2 超时策略（`timeout_ms`）

- 默认 `1500ms`。
- `timeout_ms <= 0` 时不设置超时（`context.Background()`）。
- 超时用于 **所有数据库访问**（含连接与查询）。

## 4. 认证逻辑（BASIC_AUTH）

### 4.1 回调入口

- `basic_auth_cb_c` 读取：
  - `username`
  - `password`
  - `client_id`（通过 `mosquitto_client_id`）
  - `peer` / `protocol`（通过 `mosquitto_client_*`）
- 调用 `dbAuth(username, password, clientID)`。

### 4.2 认证流程（`dbAuth`）

1. `username` 或 `password` 为空：拒绝（`missing_credentials`）。
2. `ensurePool` 确保连接池可用（必要时延迟创建）。
3. 查询用户：
   ```sql
   SELECT password_hash, salt, enabled
   FROM iot_devices
   WHERE username = $1
   ```
   - 无记录：拒绝（`user_not_found`）
   - `enabled == 0`：拒绝（`user_disabled`）
   - 密码校验：
     - 计算 `sha256(password + salt)`
     - 与 `password_hash` 比对，不一致则拒绝（`invalid_password`）
4. 如 `enforce_bind == true`，额外校验绑定：
   ```sql
   SELECT 1
   FROM client_bindings
   WHERE username = $1 AND client_id = $2
   ```
   - 无记录：拒绝（`client_not_bound`）

### 4.3 认证事件记录

认证完成后（允许/拒绝/DB 错误）会写入 `mqtt_client_auth_events`：

- `result`：`success` / `fail`
- `reason`：`ok` / `missing_credentials` / `user_not_found` / `user_disabled` / `invalid_password` / `client_not_bound` / `db_error` / `db_error_fail_open`

### 4.4 连接事件写入

登录成功后，会向连接事件表写入一条 `connect` 事件，并更新最近事件表：

- `mqtt_client_events`：追加 `event_type = 'connect'`
- `mqtt_client_latest_events`：更新 `last_event_*` 与 `last_connect_ts`，并清空 `last_disconnect_ts`

表结构见 `docs/connection-plugin.md`。

### 4.4 错误处理（`fail_open`）

- `dbAuth` 返回错误（例如连接失败、查询错误）时：
  - `fail_open == true`：放行（并记录 `db_error_fail_open`）。
  - `fail_open == false`：拒绝（并记录 `db_error`）。
- **注意**：密码错误、账号不存在等“正常拒绝”不受 `fail_open` 影响。

## 5. ACL 现状

- `acl_check_cb_c` 已实现，但 **未注册**。
- 当前 ACL 回调返回 `MOSQ_ERR_PLUGIN_DEFER`。
- 结论：**当前插件不负责 ACL 判定**，ACL 逻辑等同未启用。

## 6. 数据库表要求（以代码为准）

> 注意：仓库中的 `scripts/init_db.sql` 与此处不一致（详见第 8 节）。

### 6.1 iot_devices（认证主表）

必须存在字段（字段类型由代码读取方式决定）：
- `username`（文本）
- `password_hash`（文本，`sha256(password + salt)` 的十六进制）
- `salt`（文本）
- `enabled`（会被扫描为 `int16`，需支持 0/1）

### 6.2 client_bindings（可选绑定表）

- `username`
- `client_id`

当 `enforce_bind=true` 时，要求存在对应 `(username, client_id)` 记录。

### 6.3 mqtt_client_auth_events（认证事件表）

记录每次认证结果（success/fail）与原因：

```sql
CREATE TABLE IF NOT EXISTS mqtt_client_auth_events (
  id        BIGSERIAL PRIMARY KEY,
  ts        TIMESTAMPTZ NOT NULL,
  result    TEXT NOT NULL CHECK (result IN ('success', 'fail')),
  reason    TEXT NOT NULL,
  client_id TEXT,
  username  TEXT,
  peer      TEXT,
  protocol  TEXT
);

CREATE INDEX IF NOT EXISTS mqtt_client_auth_events_client_ts_idx
  ON mqtt_client_auth_events (client_id, ts DESC);

CREATE INDEX IF NOT EXISTS mqtt_client_auth_events_ts_idx
  ON mqtt_client_auth_events (ts DESC);
```

### 6.4 mqtt_client_events / mqtt_client_latest_events

登录成功会写入连接事件表，因此需要确保以下两张表存在（表结构见 `docs/connection-plugin.md`）：

- `mqtt_client_events`
- `mqtt_client_latest_events`

## 7. 关键配置项（运行时）

- `PG_DSN`（环境变量）：默认 DSN 来源。
- `plugin_opt_pg_dsn`：覆盖 `PG_DSN`。
- `plugin_opt_timeout_ms`：数据库访问超时（默认 1500）。
- `plugin_opt_fail_open`：数据库异常时放行（默认 false）。
- `plugin_opt_enforce_bind`：启用 client_id 绑定校验（默认 false）。

## 8. 与初始化脚本/历史文档的差异（需要注意）

当前实现与脚本/历史说明存在明显偏差，后续扩展前需要统一：

- 历史说明宣称支持 **ACL**，但当前代码未注册 ACL 回调。
- `scripts/init_db.sql` 使用表 `users` / `acls` / `client_bindings`，
  但当前代码实际查询 **`iot_devices`**。
- 历史说明/脚本描述 **bcrypt**，但 `cmd/bcryptgen` 与插件逻辑使用 **sha256(password + salt)**。

## 9. 构建与本地运行（示例流程）

1) 准备数据库：可先运行 `./scripts/init_db.sh` 创建数据库/角色（默认 `PGHOST=127.0.0.1`、`PGPORT=5432`、`PGUSER=postgres`、`PGDATABASE=mqtt`、`MQTT_DB_USER=mqtt_auth`、`MQTT_DB_PASS=StrongPass`）。该脚本会创建 `users/acls`，**不包含**当前实现需要的 `iot_devices`，需补充如下表结构：

```sql
CREATE TABLE IF NOT EXISTS iot_devices (
  username      TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  salt          TEXT NOT NULL,
  enabled       SMALLINT NOT NULL DEFAULT 1,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS client_bindings (
  username  TEXT NOT NULL,
  client_id TEXT NOT NULL,
  PRIMARY KEY (username, client_id)
);
```

脚本会输出 DSN，可用于 `plugin_opt_pg_dsn`。

2) 生成密码 hash（sha256 + salt）：

```bash
make bcryptgen
./plugins/bcryptgen -salt 'SALT' 'alice-password'
```

将输出值写入 `iot_devices.password_hash`。示例：

```sql
INSERT INTO iot_devices (username, password_hash, salt, enabled)
VALUES ('alice', '<hash>', 'SALT', 1)
ON CONFLICT (username) DO UPDATE
  SET password_hash = EXCLUDED.password_hash,
      salt = EXCLUDED.salt,
      enabled = EXCLUDED.enabled;
```

3) 构建插件：

```bash
make build-auth
```

构建需要 Mosquitto 开发头文件（Debian/Ubuntu：`sudo apt-get install -y libmosquitto-dev`）。

4) 配置并启动 Mosquitto（示例）：

```conf
allow_anonymous false
listener 1883
plugin /absolute/path/to/plugins/auth-plugin
plugin_opt_pg_dsn postgres://user:pass@127.0.0.1:5432/mqtt?sslmode=disable
plugin_opt_timeout_ms 1500
plugin_opt_fail_open false
plugin_opt_enforce_bind false
```

启动：

```bash
mosquitto -c /path/to/mosquitto.conf -v
```

5) 简单验证（ACL 未启用，仅验证认证流程）：

```bash
mosquitto_sub -h 127.0.0.1 -u alice -P 'alice-password' -t devices/alice/# -v
mosquitto_pub -h 127.0.0.1 -u alice -P 'alice-password' -t devices/alice/up -m hi
```

## 10. Docker 运行（可选）

`Dockerfile` 会编译 Mosquitto 与插件，并把 `plugins/` 拷贝到 `/mosquitto/plugins/`。容器内配置示例：

```conf
plugin /mosquitto/plugins/auth-plugin
```

## 11. 安全与运维建议

- 生产环境建议为 Postgres 启用 TLS（`sslmode=verify-full`）并配置 CA。
- DB 角色授予 `SELECT`（`iot_devices`、`client_bindings`）以及 `INSERT`（`mqtt_client_auth_events`、`mqtt_client_events`、`mqtt_client_latest_events`）。
- 保持 `auth_plugin_deny_special_chars true`，除非明确要关闭。
- 生产建议 `fail_open=false`，避免 DB 故障导致放行。

## 12. 现有测试

- `authplugin/plugin_test.go` 覆盖的仅是工具函数：
  - `parseBoolOption`
  - `parseTimeoutMS`
  - `safeDSN`
  - `sha256PwdSalt`
  - `envBool`
  - `ctxTimeout`
- 目前无数据库/插件回调的集成测试。
