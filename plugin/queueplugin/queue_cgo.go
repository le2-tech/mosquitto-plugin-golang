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
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"runtime/debug"
	"strings"
	"time"
	"unsafe"

	"mosquitto-plugin/internal/pluginutil"
)

var pid *C.mosquitto_plugin_id_t

const (
	mosqLogDebug   = int(C.MOSQ_LOG_DEBUG)
	mosqLogInfo    = int(C.MOSQ_LOG_INFO)
	mosqLogWarning = int(C.MOSQ_LOG_WARNING)
	mosqLogError   = int(C.MOSQ_LOG_ERR)
)

func log(level int, msg string, fields ...map[string]any) {
	cs := C.CString(pluginutil.FormatLogMessage(msg, fields...))
	defer C.free(unsafe.Pointer(cs))
	C.go_mosq_log(C.int(level), cs)
}

func cstr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

func isBackpressureError(err error) bool {
	return errors.Is(err, errQueueFull) || errors.Is(err, errEnqueueTimeout) || errors.Is(err, errDispatcherStopped)
}

func normalizePayloadJSON(payload []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, errors.New("payload is empty or whitespace, not valid JSON")
	}
	if json.Valid(trimmed) {
		return trimmed, nil
	}
	return nil, errors.New("payload is not valid JSON")
}

// extractUserProperties 从事件中读取 MQTT v5 用户属性。
func extractUserProperties(props *C.mosquitto_property) []userProperty {
	if props == nil {
		return nil
	}

	out := make([]userProperty, 0, 4)
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
	if isBackpressureError(err) {
		level := mosqLogWarning
		if cfg.failMode == failModeDrop {
			level = mosqLogDebug
		}
		if pluginutil.ShouldSample(&backpressureCounter, debugSampleEvery) {
			log(level, "queue-plugin publish backpressure", map[string]any{
				"error":     err,
				"fail_mode": failModeString(cfg.failMode),
			})
		}
	} else {
		log(mosqLogWarning, "queue-plugin publish failed", map[string]any{"error": err, "fail_mode": failModeString(cfg.failMode)})
	}
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

// go_mosq_plugin_version 选择最高支持的插件 API 版本。
//
//export go_mosq_plugin_version
func go_mosq_plugin_version(count C.int, versions *C.int) C.int {
	for _, v := range unsafe.Slice(versions, int(count)) {
		if v == 5 {
			return 5
		}
	}
	return -1
}

// go_mosq_plugin_init 解析配置、校验参数并注册回调。
//
//export go_mosq_plugin_init
func go_mosq_plugin_init(id *C.mosquitto_plugin_id_t, userdata *unsafe.Pointer,
	opts *C.struct_mosquitto_opt, optCount C.int) (rc C.int) {

	defer func() {
		if r := recover(); r != nil {
			log(mosqLogError, "queue-plugin: panic in plugin_init", map[string]any{"panic": r, "stack": string(debug.Stack())})
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id
	debugFilterCounter = 0
	debugPublishCounter = 0
	workerWarnCounter = 0
	backpressureCounter = 0
	publisher.mu.Lock()
	publisher.closeLocked()
	publisher.nextDial = time.Time{}
	publisher.mu.Unlock()
	stopDispatcher()

	cfg = config{
		backend:        "rabbitmq",
		exchangeType:   "direct",
		enqueueTimeout: 1000 * time.Millisecond,
		publishTimeout: 1000 * time.Millisecond,
		failMode:       failModeDrop,
	}

	if env := os.Getenv("QUEUE_DSN"); env != "" {
		cfg.dsn = env
	}

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
			// 仅用于与运维约定，不参与绑定/声明。
			cfg.queueName = v
		case "queue_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				// 兼容旧配置：同时设置入队与发送超时。
				cfg.enqueueTimeout = dur
				cfg.publishTimeout = dur
			} else {
				log(mosqLogWarning, "queue-plugin: invalid queue_timeout_ms", map[string]any{"value": v, "enqueue_timeout_ms": int(cfg.enqueueTimeout / time.Millisecond), "publish_timeout_ms": int(cfg.publishTimeout / time.Millisecond)})
			}
		case "queue_enqueue_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				cfg.enqueueTimeout = dur
			} else {
				log(mosqLogWarning, "queue-plugin: invalid queue_enqueue_timeout_ms", map[string]any{"value": v, "enqueue_timeout_ms": int(cfg.enqueueTimeout / time.Millisecond)})
			}
		case "queue_publish_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				cfg.publishTimeout = dur
			} else {
				log(mosqLogWarning, "queue-plugin: invalid queue_publish_timeout_ms", map[string]any{"value": v, "publish_timeout_ms": int(cfg.publishTimeout / time.Millisecond)})
			}
		case "queue_fail_mode":
			if mode, ok := parseFailMode(v); ok {
				cfg.failMode = mode
			} else {
				log(mosqLogWarning, "queue-plugin: invalid queue_fail_mode", map[string]any{"value": v, "fail_mode": failModeString(cfg.failMode)})
			}
		}
	}

	if cfg.backend != "rabbitmq" {
		log(mosqLogError, "queue-plugin: unsupported backend", map[string]any{"backend": cfg.backend, "expected": "rabbitmq"})
		return C.MOSQ_ERR_INVAL
	}
	if cfg.exchangeType != "direct" {
		log(mosqLogError, "queue-plugin: exchange_type must be direct")
		return C.MOSQ_ERR_INVAL
	}
	if cfg.dsn == "" || cfg.exchange == "" {
		log(mosqLogError, "queue-plugin: queue_dsn and queue_exchange must be set")
		return C.MOSQ_ERR_INVAL
	}

	log(mosqLogInfo, "queue-plugin: init", map[string]any{
		"backend":            cfg.backend,
		"dsn":                pluginutil.SafeDSN(cfg.dsn),
		"exchange":           cfg.exchange,
		"exchange_type":      cfg.exchangeType,
		"routing_key":        cfg.routingKey,
		"queue":              cfg.queueName,
		"enqueue_timeout_ms": int(cfg.enqueueTimeout / time.Millisecond),
		"publish_timeout_ms": int(cfg.publishTimeout / time.Millisecond),
		"fail_mode":          failModeString(cfg.failMode),
	})

	publisher.mu.Lock()
	if err := publisher.ensureLocked(); err != nil {
		log(mosqLogWarning, "queue-plugin: initial connect failed", map[string]any{"error": err})
		publisher.closeLocked()
	}
	publisher.mu.Unlock()

	startDispatcher(defaultDispatchBuffer)
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		stopDispatcher()
		return rc
	}

	log(mosqLogInfo, "queue-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

// go_mosq_plugin_cleanup 注销回调并释放 RabbitMQ 资源。
//
//export go_mosq_plugin_cleanup
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c))
	stopDispatcher()
	publisher.mu.Lock()
	publisher.closeLocked()
	publisher.nextDial = time.Time{}
	publisher.mu.Unlock()
	log(mosqLogInfo, "queue-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

// message_cb_c 在每次发布事件时被 Mosquitto 调用。
//
//export message_cb_c
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

	allow, reason := allowMessage(topic)
	if !allow {
		if pluginutil.ShouldSample(&debugFilterCounter, debugSampleEvery) {
			log(mosqLogDebug, "queue-plugin: filtered", map[string]any{"topic": topic, "reason": reason})
		}
		return C.MOSQ_ERR_SUCCESS
	}

	const maxPayloadLen = int(^uint32(0) >> 1)
	payloadLen := int(ed.payloadlen)
	if ed.payloadlen > C.uint32_t(maxPayloadLen) {
		return failResult(errors.New("payload too large"))
	}
	var payload json.RawMessage
	if payloadLen > 0 {
		if ed.payload == nil {
			return failResult(errors.New("payload is nil"))
		}
		var err error
		payload, err = normalizePayloadJSON(C.GoBytes(ed.payload, C.int(payloadLen)))
		if err != nil {
			return failResult(err)
		}
	} else {
		return failResult(errors.New("payload is empty, valid JSON required"))
	}

	msg := queueMessage{
		TS:       time.Now().UTC().Format(time.RFC3339),
		Topic:    topic,
		Payload:  payload,
		QoS:      uint8(ed.qos),
		Retain:   bool(ed.retain),
		ClientID: clientID,
		Username: username,
		Peer:     peer,
		Protocol: protocol,
	}
	msg.UserProperties = extractUserProperties(ed.properties)
	if pluginutil.ShouldSample(&debugPublishCounter, debugSampleEvery) {
		log(mosqLogDebug, "queue-plugin: publish", map[string]any{"topic": topic, "qos": ed.qos, "retain": bool(ed.retain), "len": payloadLen, "client_id": clientID, "username": username, "user_props": len(msg.UserProperties)})
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return failResult(err)
	}
	return failResult(enqueueMessage(body))
}

func main() {}
