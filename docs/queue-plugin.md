# 消息推送队列插件（RabbitMQ 设计说明）

本文档描述新增的 Mosquitto 插件：把 Broker 收到的消息推送到 RabbitMQ，用作实现依据。

说明：
- 本文仅覆盖 `queueplugin`（RabbitMQ）实现。
- 独立 PostgreSQL 插件方案见 `docs/msgstore-plugin-design.md`（提案）。

## 1. 决策摘要

- 后端：RabbitMQ（AMQP 0-9-1）。
- Exchange：`direct`，Routing key 由配置项指定。
- Queue：由运维预创建并绑定，插件不声明/不绑定。
- 消息格式：固定为 JSON（`payload` 优先按 JSON 原样内嵌）。
- MQTT v5 properties：仅携带 `user_properties`。
- 过滤策略：仅内置过滤 `$SYS/#` 主题。
- 发送策略：回调快速入内存队列，后台 worker 异步发送到 RabbitMQ。

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
    ├─(快速过滤/拷贝 payload/入队)
    │
    └─> queueplugin 内存队列 -> worker 异步发送 -> RabbitMQ
```

## 3. 消息格式

### 3.1 固定 JSON（payload 优先内嵌 JSON）

```json
{
  "ts": "2025-01-23T17:00:00Z",
  "topic": "devices/alice/up",
  "payload": {
    "event": "gps",
    "terminal_id": "013912345682",
    "ts": 1770602938
  },
  "qos": 1,
  "retain": false,
  "client_id": "client-1",
  "username": "alice",
  "peer": "192.168.1.10:52344",
  "protocol": "MQTT/3.1.1",
  "user_properties": [{ "k": "rr", "v": "bbb" }]
}
```

说明：

- `payload`：仅接受合法 JSON（对象/数组/标量均可），并按 JSON 原样写入。
- `payload`：若 MQTT payload 不是合法 JSON（含空 payload），本条消息按 `fail_mode` 进入失败处理路径。
- `ts`：UTC RFC3339。
- 部分字段取决于 Mosquitto 事件结构体是否提供，无法获取时可省略。

### 3.2 可选字段（后续可扩展）

- `listener_port`：Broker 监听端口（如可获得）。
- `msg_id`：消息追踪 ID（如需）。
- `user_properties`：MQTT v5 用户属性（键值对列表），仅在存在时输出。

## 4. 过滤与路由策略

默认策略：

- 内置排除 `$SYS/#`（内部系统主题）。
- 其余主题默认放行，不提供额外过滤配置项。

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

- `plugin_opt_queue_timeout_ms`：兼容旧参数，同时设置入队与发送超时（默认 1000ms）。
- `plugin_opt_queue_enqueue_timeout_ms`：`block` 模式下入队等待时长（默认 1000ms）。
- `plugin_opt_queue_publish_timeout_ms`：后台发送与 AMQP 拨号超时（默认 1000ms）。
- `plugin_opt_queue_fail_mode`：入队失败（队列满/停止）时处理策略，`drop`/`block`/`disconnect`（默认 `drop`）。
- 内部内存队列为固定大小（当前实现默认 4096）。
- 调试日志由 Mosquitto `log_type` 控制（例如启用 `log_type debug`）。

**注意：** DSN 等敏感信息需在日志中脱敏。

## 7. 运行配置示例

```conf

# 消息队列插件
plugin /absolute/path/to/plugins/queue-plugin
plugin_opt_queue_backend rabbitmq
plugin_opt_queue_dsn amqp://user:pass@127.0.0.1:5672/vhost
plugin_opt_queue_exchange mqtt_exchange
plugin_opt_queue_exchange_type direct
plugin_opt_queue_routing_key mqtt.messages
plugin_opt_queue_queue mqtt_queue
plugin_opt_queue_fail_mode drop
```

> 说明：每个 `plugin` 与其 `plugin_opt_*` 需要连在一起配置。

## 8. 可靠性与失败策略

- 回调阶段仅做入队，RabbitMQ 写入由后台 worker 异步完成。
- 入队失败按 `fail_mode` 执行，默认 `drop`。
- 后台发送失败只记录日志，不影响已经返回给 MQTT 客户端的回调结果。
- 插件停止时 worker 立即退出，内存队列中未发送消息可能丢失（以换取可预测的快速关停）。

## 9. 安全与合规

- DSN/密码日志脱敏。
- 支持 TLS 连接（由后端配置决定）。
- 不记录明文 payload（除非显式开启 debug）。

## 10. 文件结构（当前实现）

```
.
├── plugin/queueplugin/
│   ├── queue_bridge.c        # C 侧入口与包装函数
│   ├── queue_cgo.go          # Go 导出函数/回调与 C 交互
│   ├── queue_config.go       # 配置解析
│   ├── queue_dispatcher.go   # 内存队列与异步 worker
│   ├── queue_filters.go      # 过滤规则
│   ├── queue_publisher.go    # RabbitMQ 发布器
│   └── queue_types.go        # 类型与全局配置
```

构建目标示例：

```
make build-queue
# 产物：plugins/queue-plugin 与 plugins/queue-plugin.h
```

## 11. 测试计划（建议）

- 单元测试：配置解析、topic 匹配、消息封装格式。
- 集成测试：对接 RabbitMQ（本地容器），验证失败策略与超时行为。
- 压力测试：高并发 PUBLISH 时的 CPU/内存与丢弃率。
