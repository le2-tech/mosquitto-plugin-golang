# msgstore-plugin 独立设计（异步入库，保持 queueplugin 不变）

本文设计一个**独立的新插件**用于异步写 PostgreSQL，目标是：

- 不修改现有 `plugin/queueplugin/` 代码和行为。
- 新增目录实现独立插件（建议目录名：`msgstoreplugin/`）。
- 采用“回调快速入队 + 后台 worker 写库”的异步模型，降低对 MQTT 主链路的影响。

参考来源（设计思路）：

- `/Users/sujiemei/rvms/gps-worker-golang/docs/worker-queue2db-design.md`
- `/Users/sujiemei/rvms/gps-worker-golang/cmd/worker-queue2db/worker.go`
- `/Users/sujiemei/rvms/gps-worker-golang/cmd/worker-queue2db/processor.go`
- `/Users/sujiemei/rvms/gps-worker-golang/cmd/worker-queue2db/db_writer.go`

## 1. 决策摘要

- 现有 `queueplugin`（RabbitMQ）保持原样，不做重构。
- 新增独立插件 `msgstore-plugin`，处理 `MOSQ_EVT_MESSAGE`，异步入库 PostgreSQL。
- 生产建议二选一加载，避免同一消息被两个采集插件重复处理。

## 2. 范围与非目标

范围：

- 提供与 `queueplugin` 同类的消息采集入口（`MOSQ_EVT_MESSAGE`）。
- 提供异步 PostgreSQL 入库能力（内部内存队列 + worker）。
- 提供独立构建产物与独立配置项前缀。

非目标：

- 不替换或删除现有 RabbitMQ 路径。
- 不在本阶段引入城市边界计算。
- `connect/disconnect` 不在本插件处理范围（继续由 `connplugin` 处理）。

## 3. 插件拓扑与加载方式

建议拓扑：

```text
方案 A（现状）:
MQTT -> queueplugin -> RabbitMQ -> worker -> PostgreSQL

方案 B（新增）:
MQTT -> msgstore-plugin (回调快速入队) -> 内存队列 -> worker -> PostgreSQL
```

加载建议：

- 一次只加载一个“消息采集插件”。
- 若要双写验证，应显式隔离（不同 topic 过滤或不同落库目标），避免重复写。

## 4. 目录与文件规划

建议新增：

```text
msgstoreplugin/
├── msgstore_bridge.c      # C 侧入口与包装函数
├── msgstore_cgo.go        # Go 导出函数/回调与 C 交互
├── msgstore_config.go     # 配置解析
├── msgstore_filters.go    # 过滤规则
├── msgstore_queue.go      # 内存队列与入队策略
├── msgstore_worker.go     # 后台 worker 与重试
├── msgstore_db.go         # PostgreSQL 写入与连接池
└── msgstore_types.go      # 类型与全局配置
```

构建产物建议：

- `plugins/msgstore-plugin`
- `plugins/msgstore-plugin.h`

Make 目标建议：

- `make build-msgstore`

## 5. 配置设计（独立前缀）

为避免与 `queueplugin` 混淆，建议新插件使用独立前缀 `msgstore_`。

核心配置：

- `plugin_opt_msgstore_dsn`
- `plugin_opt_msgstore_debug`
- `plugin_opt_msgstore_write_mode`（例如 `gps_v1`）
- `plugin_opt_msgstore_notify`（是否 `pg_notify`）

入队与回调阶段配置：

- `plugin_opt_msgstore_queue_size`（默认 10000）
- `plugin_opt_msgstore_enqueue_mode`（`drop`/`block`/`disconnect`）
- `plugin_opt_msgstore_enqueue_wait_ms`（仅 `block` 模式生效，默认 50）

worker 与写库阶段配置：

- `plugin_opt_msgstore_workers`（默认 4）
- `plugin_opt_msgstore_db_timeout_ms`（默认 300）
- `plugin_opt_msgstore_retry_max`（默认 3）
- `plugin_opt_msgstore_retry_backoff_ms`（默认 200）
- `plugin_opt_msgstore_retry_backoff_max_ms`（默认 5000）

过滤配置：

- `plugin_opt_msgstore_include_topics`
- `plugin_opt_msgstore_exclude_topics`（默认 `$SYS/#`）
- `plugin_opt_msgstore_include_users`
- `plugin_opt_msgstore_exclude_users`
- `plugin_opt_msgstore_include_clients`
- `plugin_opt_msgstore_exclude_clients`
- `plugin_opt_msgstore_include_retained`

## 6. 异步处理模型

### 6.1 回调阶段（主链路）

`MOSQ_EVT_MESSAGE` 回调仅做轻量操作：

- 基础过滤（topic/user/client/retain）。
- 拷贝必要元数据和 payload。
- 尝试入队。

入队失败按 `enqueue_mode` 处理：

- `drop`：直接丢弃并返回成功。
- `block`：在 `enqueue_wait_ms` 内等待队列可写，超时后丢弃。
- `disconnect`：返回错误码触发客户端断开。

说明：

- 回调阶段不做重写库逻辑，不等待 DB 结果。

### 6.2 worker 阶段（后台）

worker 从内存队列消费并写 PostgreSQL：

- 解析 topic、`payload`、envelope（参考 gps-worker）。
- 事件路由：
- `event=gps` 写 `gps_position_histories` 与 `gps_position_last`。
- `event=evt/cmd` 写 `client_cmd_events`。
- `event=connect/disconnect` 忽略或仅 debug 记录。
- 写库失败按重试策略退避重试，超过阈值后丢弃并记录错误。

### 6.3 幂等与一致性

建议参考 gps-worker：

- 历史表：`ON CONFLICT DO NOTHING`
- 最新状态表：`ON CONFLICT ... DO UPDATE ... WHERE excluded.gps_time >= current.gps_time`
- 需要多语句时用事务
- 如启用通知，事务提交后再 `pg_notify`

### 6.4 连接池与超时

建议复用现有公共工具：

- `internal/pluginutil.TimeoutContext`
- `internal/pluginutil.EnsureSharedPGPool`
- `internal/pluginutil.SafeDSN`

## 7. 失败语义说明

异步模型下要区分两类失败：

- 入队失败：影响当前消息回调返回，由 `enqueue_mode` 决定。
- 写库失败：发生在后台 worker，不会影响已返回的回调结果。

因此：

- `enqueue_mode` 负责主链路行为。
- `retry_max/backoff` 负责最终入库成功率。

## 8. 性能与稳定性建议

建议默认参数：

- `queue_size=10000`
- `workers=4`
- `db_timeout_ms=300`
- `enqueue_mode=drop`

建议观测指标：

- `msgstore_queue_len`
- `msgstore_enqueue_drop_total`
- `msgstore_write_ok_total`
- `msgstore_write_fail_total`
- `msgstore_retry_total`
- `msgstore_db_write_latency_ms`

压测建议：

- 1k/3k/5k msg/s 吞吐
- PostgreSQL 故障注入（超时/连接断开）
- 对比不同 `enqueue_mode` 下 MQTT 侧行为

## 9. 迁移与回滚

迁移步骤：

1. 增加 `msgstoreplugin/` 并实现最小可用版本。
2. 测试环境仅加载 `msgstore-plugin` 验证入库。
3. 小流量灰度（topic 白名单）。
4. 稳定后再决定是否替代 RabbitMQ 链路。

回滚：

- Mosquitto 配置切回加载 `queueplugin` 即可。

## 10. 验收清单

- 功能：`gps/evt/cmd` 事件可入库。
- 稳定性：DB 故障下 MQTT 主链路不出现明显抖动或阻塞。
- 语义：`enqueue_mode` 行为符合预期。
- 一致性：幂等规则生效，重复消息不产生脏写。
- 兼容性：`queueplugin` 构建与运行行为不受影响。
- 可观测：日志与指标可定位队列积压和写库失败。

## 11. 当前状态

本文档为设计提案，当前仓库尚未开始 `msgstoreplugin` 代码实现。
