# 连接事件记录插件（PostgreSQL 设计说明）

本文档描述连接事件记录方案：连接与断开事件由连接插件写入。

## 1. 概述

- 触发事件：`MOSQ_EVT_CONNECT` 与 `MOSQ_EVT_DISCONNECT`。
- 写入策略：回调内直接写库，best-effort，失败仅记录日志。
- 数据模型：事件明细表 `client_conn_events` + 最近事件表 `client_sessions`（每设备一行）。
- 表名在代码中固定，不提供配置项。

## 2. 事件语义

- `connect` 记录于客户端连接事件回调。
- `disconnect` 记录于 `DISCONNECT` 阶段，表示连接结束。

## 3. 数据库表设计（固定表名）

### 3.1 事件明细表（追加写入）

```sql
CREATE TABLE IF NOT EXISTS client_conn_events (
  id          BIGSERIAL PRIMARY KEY,
  ts          TIMESTAMPTZ NOT NULL,
  event_type  TEXT NOT NULL CHECK (event_type IN ('connect', 'disconnect')),
  client_id   TEXT NOT NULL,
  username    TEXT,
  peer        TEXT,
  protocol    TEXT,
  reason_code INTEGER,
  extra       JSONB
);

CREATE INDEX IF NOT EXISTS client_conn_events_client_ts_idx
  ON client_conn_events (client_id, ts DESC);

CREATE INDEX IF NOT EXISTS client_conn_events_ts_idx
  ON client_conn_events (ts DESC);
```

### 3.2 最近事件表（每设备一行）

```sql
CREATE TABLE IF NOT EXISTS client_sessions (
  client_id           TEXT PRIMARY KEY,
  username            TEXT,
  last_event_ts       TIMESTAMPTZ NOT NULL,
  last_event_type     TEXT NOT NULL CHECK (last_event_type IN ('connect', 'disconnect')),
  last_connect_ts     TIMESTAMPTZ,
  last_disconnect_ts  TIMESTAMPTZ,
  last_peer           TEXT,
  last_protocol       TEXT,
  last_reason_code    INTEGER,
  extra               JSONB
);

CREATE INDEX IF NOT EXISTS client_sessions_ts_idx
  ON client_sessions (last_event_ts DESC);
```

## 4. 写入规则

- 每次事件：先 `INSERT` 到 `client_conn_events`。
- 同步 `UPSERT` 到 `client_sessions`：
  - `last_event_*` 总是更新。
  - `connect` 时更新 `last_connect_ts`，并清空 `last_disconnect_ts`。
- `disconnect` 时更新 `last_disconnect_ts`，并保留 `last_connect_ts`。
- `reason_code` 仅断开事件有值（无则为 `NULL`）。
- `extra` 当前未写入内容，保留用于后续扩展。
- 未经过 `MOSQ_EVT_CONNECT` 的连接不会写入断开事件（用于过滤认证失败的断开）。

写入来源：

- `connect`：本插件写入。
- `disconnect`：本插件写入。

## 5. 字段来源（建议映射）

- `client_id`：`mosquitto_client_id(ed.client)`
- `username`：`mosquitto_client_username(ed.client)` 或 `ed.username`
- `peer`：`mosquitto_client_address(ed.client)`
- `protocol`：`mosquitto_client_protocol_version(ed.client)` -> `MQTT/3.1` / `MQTT/3.1.1` / `MQTT/5.0`
- `reason_code`：`struct mosquitto_evt_disconnect.reason`

## 6. 配置项

- `plugin_opt_conn_pg_dsn`：PostgreSQL DSN（最高优先级，必填）。
- `PG_DSN`：环境变量 DSN（兜底）。
- `plugin_opt_conn_timeout_ms`：写库超时（默认 1000）。
- `plugin_opt_conn_debug`：调试日志（默认 false）。

内网环境使用 `sslmode=disable`。

## 7. 运行配置示例

```conf
# 连接事件记录插件
plugin /absolute/path/to/plugins/conn-plugin
plugin_opt_conn_pg_dsn postgres://user:pass@127.0.0.1:5432/mqtt?sslmode=disable
plugin_opt_conn_timeout_ms 1000
plugin_opt_conn_debug false

```

## 8. 可靠性与日志

- 写库失败：记录 warning 日志并跳过写入，不影响连接/断开。
- 建议在 Mosquitto 中开启 `log_type debug` 以便排查配置问题。

## 9. 构建

```bash
make build-conn
```

产物：`plugins/conn-plugin` 与 `plugins/conn-plugin.h`。

## 10. 测试建议

- 当前无连接插件的单元测试（工具函数已迁移至 `internal/pluginutil` 测试）。
- 集成测试：本地 Postgres 插入与 UPSERT 校验。
- 压力测试：大量短连接下的写入延迟与丢弃率。
