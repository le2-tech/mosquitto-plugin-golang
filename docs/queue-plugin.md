# 消息推送队列插件（RabbitMQ 设计说明）

本文档描述新增的 Mosquitto 插件：把 Broker 收到的消息推送到 RabbitMQ，用作实现依据。

## 1. 决策摘要

- 后端：RabbitMQ（AMQP 0-9-1）。
- Exchange：`direct`，Routing key 由配置项指定。
- Queue：由运维预创建并绑定，插件不声明/不绑定。
- 消息格式：固定为 JSON + base64（payload 置于 `payload_b64`）。
- MQTT v5 properties：仅携带 `user_properties`。
- 过滤策略：支持 include/exclude；默认排除 `$SYS/#`；`exclude_*` 高于 `include_*`；默认不推送 retain。
- 发送策略：回调内直接发送，短超时，失败默认丢弃（`fail_mode=drop`）。

## 2. 触发点与处理流程

### 2.1 事件触发

- 使用 Mosquitto 插件事件中的消息回调（`MOSQ_EVT_MESSAGE`，以 `mosquitto_plugin.h` 为准）。
- 仅处理**客户端上行** PUBLISH，不处理 Broker 向订阅者的下行消息。

### 2.2 数据流

```
MQTT Client
    │ PUBLISH
    ▼
Mosquitto (MOSQ_EVT_MESSAGE)
    │
    ├─(快速过滤/拷贝 payload)
    │
    └─> 本插件：直接发送（带超时） -> RabbitMQ
```

## 3. 消息格式

### 3.1 固定 JSON + base64

```json
{
  "ts": "2025-01-23T17:00:00Z",
  "topic": "devices/alice/up",
  "payload_b64": "aGVsbG8=",
  "qos": 1,
  "retain": false,
  "dup": false,
  "client_id": "client-1",
  "username": "alice",
  "peer": "192.168.1.10:52344",
  "protocol": "MQTT/3.1.1",
  "user_properties": [{"k": "rr", "v": "bbb"}]
}
```

说明：
- `payload_b64`：base64 编码，保证二进制安全。
- `ts`：UTC RFC3339。
- 部分字段取决于 Mosquitto 事件结构体是否提供，无法获取时可省略。

### 3.2 可选字段（后续可扩展）

- `listener_port`：Broker 监听端口（如可获得）。
- `msg_id`：消息追踪 ID（如需）。
- `user_properties`：MQTT v5 用户属性（键值对列表），仅在存在时输出。

## 4. 过滤与路由策略

默认策略：

- 默认排除 `$SYS/#`（内部系统主题）。
- 未配置 include 条件时，允许所有 topic/user/client（排除项除外）。
- `exclude_*` 优先级高于 `include_*`。
- `include_retained=false`（默认不推送 retain）。

可配置项：
- `include_topics` / `exclude_topics`：MQTT 通配符 `+`/`#`。
- `include_users` / `exclude_users`：用户名过滤。
- `include_clients` / `exclude_clients`：client_id 过滤。
- `include_retained`：是否推送 retain。

## 5. RabbitMQ 对接规则

- Exchange 类型：`direct`。
- Routing key：由配置项指定（固定值）。
- Queue：由运维预创建并绑定；插件仅发布到 exchange。
- 连接串：`amqp://user:pass@host:port/vhost`（TLS 用 `amqps://`）。

## 6. 配置项

连接与路由：
- `plugin_opt_queue_backend`：固定 `rabbitmq`。
- `plugin_opt_queue_dsn`：AMQP 连接串（可由环境变量 `QUEUE_DSN` 提供默认值，`plugin_opt_*` 优先）。
- `plugin_opt_queue_exchange`：Exchange 名称。
- `plugin_opt_queue_exchange_type`：固定 `direct`。
- `plugin_opt_queue_routing_key`：Routing key（默认空）。
- `plugin_opt_queue_queue`：Queue 名称（可选，仅用于与运维约定，不参与绑定）。

发送与失败策略：
- `plugin_opt_queue_timeout_ms`：发送超时（默认 1000）。
- `plugin_opt_queue_fail_mode`：`drop`/`block`/`disconnect`（默认 `drop`）。
- `plugin_opt_payload_encoding`：固定 `base64`。
- `plugin_opt_queue_debug`：调试日志开关（默认 false，需配合 Mosquitto `log_type debug`）。

过滤：
- `plugin_opt_include_topics`：多个 topic 逗号分隔（默认空）。
- `plugin_opt_exclude_topics`：多个 topic 逗号分隔（默认 `$SYS/#`；显式配置则覆盖默认）。
- `plugin_opt_include_users`：多个用户名逗号分隔（默认空）。
- `plugin_opt_exclude_users`：多个用户名逗号分隔（默认空）。
- `plugin_opt_include_clients`：多个 client_id 逗号分隔（默认空）。
- `plugin_opt_exclude_clients`：多个 client_id 逗号分隔（默认空）。
- `plugin_opt_include_retained`：是否推送 retain（默认 false）。

**注意：** DSN 等敏感信息需在日志中脱敏。

## 7. 运行配置示例

```conf
# 认证插件
plugin /absolute/path/to/build/auth-plugin
plugin_opt_pg_dsn postgres://user:pass@127.0.0.1:5432/mqtt?sslmode=disable
plugin_opt_timeout_ms 1500
plugin_opt_fail_open false

# 消息队列插件
plugin /absolute/path/to/build/queue-plugin
plugin_opt_queue_backend rabbitmq
plugin_opt_queue_dsn amqp://user:pass@127.0.0.1:5672/vhost
plugin_opt_queue_exchange mqtt_exchange
plugin_opt_queue_exchange_type direct
plugin_opt_queue_routing_key mqtt.messages
plugin_opt_queue_queue mqtt_queue
plugin_opt_queue_fail_mode drop
plugin_opt_payload_encoding base64
```

> 说明：每个 `plugin` 与其 `plugin_opt_*` 需要连在一起配置。

## 8. 可靠性与失败策略

- 回调内直接发送（无内部缓冲/批量）。
- 通过短超时限制阻塞时长。
- 队列不可用时按 `fail_mode` 执行，默认 `drop`。

## 9. 安全与合规

- DSN/密码日志脱敏。
- 支持 TLS 连接（由后端配置决定）。
- 不记录明文 payload（除非显式开启 debug）。
- 可通过过滤规则限制敏感数据流出。

## 10. 文件结构与构建规划（草案）

```
.
├── queueplugin/
│   ├── queue_bridge.c        # C 侧入口与包装函数
│   └── queue_plugin.go       # Go 插件实现
```

构建目标示例：

```
make build-queue
# 产物：build/queue-plugin 与 build/queue-plugin.h
```

## 11. 测试计划（建议）

- 单元测试：配置解析、topic 匹配、消息封装格式。
- 集成测试：对接 RabbitMQ（本地容器），验证失败策略与超时行为。
- 压力测试：高并发 PUBLISH 时的 CPU/内存与丢弃率。
