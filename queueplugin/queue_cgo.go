package main

/*
#cgo darwin pkg-config: libmosquitto libcjson
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto libcjson
#include <stdlib.h>
#include <mosquitto.h>

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int message_cb_c(int event, void *event_data, void *userdata);

int register_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
int unregister_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
void go_mosq_log(int level, const char* msg);
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"
	"unsafe"

	"mosquitto-plugin/internal/pluginutil"
)

var pid *C.mosquitto_plugin_id_t

// mosqLog 写入 Mosquitto 的日志系统。
func mosqLog(level C.int, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	C.go_mosq_log(level, cs)
}

// infoLog 以 info 级别输出日志。
func infoLog(msg string, args ...any) {
	mosqLog(C.MOSQ_LOG_INFO, msg, args...)
}

// debugLog 在 queue_debug=true 时才输出。
func debugLog(msg string, args ...any) {
	if !cfg.debug {
		return
	}
	mosqLog(C.MOSQ_LOG_DEBUG, msg, args...)
}

func cstr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

// extractUserProperties 从事件中读取 MQTT v5 用户属性。
func extractUserProperties(props *C.mosquitto_property) []userProperty {
	if props == nil {
		return nil
	}

	var out []userProperty
	var name *C.char
	var value *C.char

	prop := C.mosquitto_property_read_string_pair(props, C.MQTT_PROP_USER_PROPERTY, &name, &value, C.bool(false))
	for prop != nil {
		out = append(out, userProperty{
			Key:   cstr(name),
			Value: cstr(value),
		})
		prop = C.mosquitto_property_read_string_pair(prop, C.MQTT_PROP_USER_PROPERTY, &name, &value, C.bool(true))
	}

	return out
}

// failResult 将发布错误映射为 Mosquitto 返回码。
func failResult(err error) C.int {
	if err == nil {
		return C.MOSQ_ERR_SUCCESS
	}
	mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin publish failed: %v", err)
	switch cfg.failMode {
	case failModeDrop:
		return C.MOSQ_ERR_SUCCESS
	case failModeBlock:
		return C.MOSQ_ERR_ACL_DENIED
	case failModeDisconnect:
		return C.MOSQ_ERR_CONN_LOST
	default:
		return C.MOSQ_ERR_SUCCESS
	}
}

//export go_mosq_plugin_version
// go_mosq_plugin_version 选择最高支持的插件 API 版本。
func go_mosq_plugin_version(count C.int, versions *C.int) C.int {
	for _, v := range unsafe.Slice(versions, int(count)) {
		if v == 5 {
			return 5
		}
	}
	return -1
}

//export go_mosq_plugin_init
// go_mosq_plugin_init 解析配置、校验参数并注册回调。
func go_mosq_plugin_init(id *C.mosquitto_plugin_id_t, userdata *unsafe.Pointer,
	opts *C.struct_mosquitto_opt, optCount C.int) (rc C.int) {

	defer func() {
		if r := recover(); r != nil {
			mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: panic in plugin_init: %v\n%s", r, string(debug.Stack()))
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id

	cfg = config{
		backend:         "rabbitmq",
		exchangeType:    "direct",
		timeout:         1000 * time.Millisecond,
		failMode:        failModeDrop,
		includeRetained: false,
		debug:           false,
	}

	if env := os.Getenv("QUEUE_DSN"); env != "" {
		cfg.dsn = env
	}

	seenExcludeTopics := false

	for _, o := range unsafe.Slice(opts, int(optCount)) {
		k, v := cstr(o.key), cstr(o.value)
		switch k {
		case "queue_backend":
			if v != "" {
				cfg.backend = strings.ToLower(strings.TrimSpace(v))
			}
		case "queue_dsn":
			cfg.dsn = v
		case "queue_exchange":
			cfg.exchange = v
		case "queue_exchange_type":
			if v != "" {
				cfg.exchangeType = strings.ToLower(strings.TrimSpace(v))
			}
		case "queue_routing_key":
			cfg.routingKey = v
		case "queue_queue":
			cfg.queueName = v
		case "queue_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				cfg.timeout = dur
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_timeout_ms=%q, keeping %dms", v, int(cfg.timeout/time.Millisecond))
			}
		case "queue_fail_mode":
			if mode, ok := parseFailMode(v); ok {
				cfg.failMode = mode
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_fail_mode=%q, keeping %s", v, failModeString(cfg.failMode))
			}
		case "queue_debug":
			if parsed, ok := pluginutil.ParseBoolOption(v); ok {
				cfg.debug = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_debug=%q, keeping %t", v, cfg.debug)
			}
		case "payload_encoding":
			if strings.TrimSpace(strings.ToLower(v)) != "base64" {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: payload_encoding=%q not supported, forcing base64", v)
			}
		case "include_topics":
			cfg.includeTopics = parseList(v)
		case "exclude_topics":
			cfg.excludeTopics = parseList(v)
			seenExcludeTopics = true
		case "include_users":
			cfg.includeUsers = parseSet(v)
		case "exclude_users":
			cfg.excludeUsers = parseSet(v)
		case "include_clients":
			cfg.includeClients = parseSet(v)
		case "exclude_clients":
			cfg.excludeClients = parseSet(v)
		case "include_retained":
			if parsed, ok := pluginutil.ParseBoolOption(v); ok {
				cfg.includeRetained = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid include_retained=%q, keeping %t", v, cfg.includeRetained)
			}
		}
	}

	if !seenExcludeTopics {
		cfg.excludeTopics = []string{"$SYS/#"}
	}

	if cfg.backend != "rabbitmq" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: unsupported backend %q (expected rabbitmq)", cfg.backend)
		return C.MOSQ_ERR_INVAL
	}
	if cfg.exchangeType != "direct" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: exchange_type must be direct")
		return C.MOSQ_ERR_INVAL
	}
	if cfg.dsn == "" || cfg.exchange == "" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: queue_dsn and queue_exchange must be set")
		return C.MOSQ_ERR_INVAL
	}

	mosqLog(C.MOSQ_LOG_INFO,
		"queue-plugin: init backend=%s dsn=%s exchange=%s exchange_type=%s routing_key=%s queue=%s timeout_ms=%d fail_mode=%s",
		cfg.backend, pluginutil.SafeDSN(cfg.dsn), cfg.exchange, cfg.exchangeType, cfg.routingKey, cfg.queueName, int(cfg.timeout/time.Millisecond), failModeString(cfg.failMode),
	)

	publisher.mu.Lock()
	if err := publisher.ensureLocked(); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: initial connect failed: %v", err)
		publisher.closeLocked()
	}
	publisher.mu.Unlock()

	if rc := C.register_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	mosqLog(C.MOSQ_LOG_INFO, "queue-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

//export go_mosq_plugin_cleanup
// go_mosq_plugin_cleanup 注销回调并释放 RabbitMQ 资源。
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c))
	publisher.mu.Lock()
	publisher.closeLocked()
	publisher.mu.Unlock()
	mosqLog(C.MOSQ_LOG_INFO, "queue-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

//export message_cb_c
// message_cb_c 在每次发布事件时被 Mosquitto 调用。
func message_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_message)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	topic := cstr(ed.topic)
	var clientID string
	var username string
	var peer string
	var protocol string
	if ed.client != nil {
		clientID = cstr(C.mosquitto_client_id(ed.client))
		username = cstr(C.mosquitto_client_username(ed.client))
		peer = cstr(C.mosquitto_client_address(ed.client))
		protocol = pluginutil.ProtocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	}

	allow, reason := allowMessage(topic, username, clientID, bool(ed.retain))
	if !allow {
		debugLog("queue-plugin: filtered topic=%q client_id=%q username=%q reason=%s", topic, clientID, username, reason)
		return C.MOSQ_ERR_SUCCESS
	}

	const maxPayloadLen = int(^uint32(0) >> 1)
	payloadLen := int(ed.payloadlen)
	if ed.payloadlen > C.uint32_t(maxPayloadLen) {
		return failResult(errors.New("payload too large"))
	}
	var payload []byte
	if payloadLen > 0 {
		if ed.payload == nil {
			return failResult(errors.New("payload is nil"))
		}
		payload = C.GoBytes(ed.payload, C.int(payloadLen))
	}

	msg := queueMessage{
		QueueTS:    time.Now().UTC().Format(time.RFC3339),
		Topic:      topic,
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
		QoS:        uint8(ed.qos),
		Retain:     bool(ed.retain),
		ClientID:   clientID,
		Username:   username,
		Peer:       peer,
		Protocol:   protocol,
	}
	msg.UserProperties = extractUserProperties(ed.properties)

	debugLog("queue-plugin: publish topic=%q qos=%d retain=%t len=%d client_id=%q username=%q user_props=%d",
		topic, ed.qos, bool(ed.retain), payloadLen, clientID, username, len(msg.UserProperties))

	body, err := json.Marshal(msg)
	if err != nil {
		return failResult(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	return failResult(publisher.Publish(ctx, body))
}

func main() {}
