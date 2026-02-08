# 插件调用流程图

本文件描述本项目插件从 Mosquitto 到 Go 业务逻辑的调用链，并给出更细的注册与事件流转图。

## 总览：从 Mosquitto 到 Go 业务逻辑

```mermaid
flowchart LR
  A["Mosquitto Broker / 加载插件 .so"] --> B["C Bridge (.c)"]
  B --> C["Go 导出函数 / go_mosq_plugin_init"]
  C --> D["Go 业务初始化 / 解析配置/连库/注册回调"]

  A --> E["触发事件 / CONNECT / DISCONNECT / MESSAGE..."]
  E --> F["C 回调函数 / *_cb_c"]
  F --> G["Go 回调函数 / *_cb_c (Go)"]
  G --> H["业务逻辑处理 / 写库/转发/记录日志"]
```

## 细化：注册回调与事件路径

```mermaid
flowchart TB
  subgraph Load[加载阶段]
    L1["Mosquitto: 调用 mosquitto_plugin_init"] --> L2["C 层入口 / .c"]
    L2 --> L3["Go: go_mosq_plugin_init"]
    L3 --> L4["Go: register_event_callback(...)"]
    L4 --> L5["C: register_event_callback / 桥接"]
    L5 --> L6["C: mosquitto_callback_register"]
  end

  subgraph Event[事件触发阶段]
    E1["Mosquitto: 触发事件"] --> E2["C 回调 / *_cb_c"]
    E2 --> E3["Go 回调 / *_cb_c"]
    E3 --> E4["业务逻辑 / DB / 队列 / 日志"]
  end
```

## 各插件事件差异

```mermaid
flowchart LR
  subgraph AuthPlugin[authplugin]
    A1["MOSQ_EVT_BASIC_AUTH"] --> A2["auth_cgo.go: basic_auth_cb_c"]
    A2 --> A3["鉴权 / 记录 auth 事件"]
  end

  subgraph ConnPlugin[connplugin]
    C1["MOSQ_EVT_CONNECT"] --> C2["conn_cgo.go: connect_cb_c"]
    C2 --> C3["记录 connect 事件"]
    D1["MOSQ_EVT_DISCONNECT"] --> D2["conn_cgo.go: disconnect_cb_c"]
    D2 --> D3["记录 disconnect 事件"]
  end

  subgraph QueuePlugin[queueplugin]
    Q1["MOSQ_EVT_MESSAGE"] --> Q2["queue_cgo.go: message_cb_c"]
    Q2 --> Q3["转发到 RabbitMQ / 编码处理"]
  end

  subgraph MsgStorePlugin["msgstoreplugin（规划）"]
    P1["MOSQ_EVT_MESSAGE"] --> P2["msgstore_cgo.go: message_cb_c"]
    P2 --> P3["快速入内存队列"]
    P3 --> P4["后台 worker 写 PostgreSQL"]
  end
```

## 关键桥接点说明

- `.c` 文件负责：
  - 提供 Mosquitto 期望的 C ABI 入口（`mosquitto_plugin_version/init/cleanup`）
  - 统一封装 `register_event_callback`，内部调用 `mosquitto_callback_register`
  - 提供 `go_mosq_log` 等包装，避免 Go 直接调用 C 可变参函数
- `.go` 文件负责：
  - 业务逻辑与配置解析
  - 事件回调实现
  - 与外部系统交互（数据库、队列）
