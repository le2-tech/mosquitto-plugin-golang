# 当前实现说明（通用）

本文档描述仓库的通用实现与约定，插件细节请查阅对应文档。

## 1. 插件概览

- 认证插件（PostgreSQL）：`docs/auth-plugin.md`
- 消息队列插件（RabbitMQ）：`docs/queue-plugin.md`
- 独立 msgstore-plugin（PostgreSQL）设计（提案，暂不实现）：`docs/msgstore-plugin-design.md`
- 连接事件记录插件（PostgreSQL）：`docs/connection-plugin.md`

## 2. 构建与产物

- 构建认证插件：`make build-auth`
- 构建队列插件：`make build-queue`
- 构建连接事件插件：`make build-conn`
- 生成工具：`make bcryptgen`

产物默认输出到 `plugins/`：
- `plugins/auth-plugin` / `plugins/auth-plugin.h`
- `plugins/queue-plugin` / `plugins/queue-plugin.h`
- `plugins/conn-plugin` / `plugins/conn-plugin.h`
- `plugins/bcryptgen`

## 3. 目录结构（核心）

```
.
├── plugin/
│   ├── authplugin/        # 认证插件
│   ├── connplugin/        # 连接事件插件
│   └── queueplugin/       # 消息队列插件
├── cmd/bcryptgen/          # 密码 hash 工具
├── internal/pluginutil/    # 通用工具函数
├── docs/                  # 文档
├── plugins/               # 构建产物
├── mosquitto.conf          # 示例配置
└── Makefile
```

## 4. Mosquitto 配置约定

- 每个 `plugin` 与其 `plugin_opt_*` 需要连在一起配置。
- BASIC_AUTH 回调链在**首个非 `MOSQ_ERR_PLUGIN_DEFER`** 处终止，多个 BASIC_AUTH 插件时顺序会影响回调是否触发。
- `conn-plugin` 不使用 BASIC_AUTH 回调，顺序不会影响其断开记录。

示例：

```conf
plugin /absolute/path/to/plugins/auth-plugin
plugin_opt_pg_dsn postgres://user:pass@127.0.0.1:5432/mqtt?sslmode=disable

plugin /absolute/path/to/plugins/conn-plugin
plugin_opt_conn_pg_dsn postgres://user:pass@127.0.0.1:5432/mqtt?sslmode=disable
```

## 5. Docker

`Dockerfile` 会编译 Mosquitto 2.1.1 与插件，并把 `plugins/` 拷贝到 `/mosquitto/plugins/`。

```conf
plugin /mosquitto/plugins/auth-plugin
```

## 6. 测试

- 单元测试为主：`go test ./...`
- 集成测试需准备对应依赖（PostgreSQL/RabbitMQ）。
