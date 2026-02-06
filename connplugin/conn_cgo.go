package main

/*
#cgo darwin pkg-config: libmosquitto libcjson
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto libcjson
#include <stdlib.h>
#include <mosquitto.h>

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int connect_cb_c(int event, void *event_data, void *userdata);
int disconnect_cb_c(int event, void *event_data, void *userdata);

int register_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
int unregister_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
void go_mosq_log(int level, const char* msg);
*/
import "C"

import (
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

const mosqLogInfo = int(C.MOSQ_LOG_INFO)

// logKV 以 key=value 形式输出结构化字段。
func logKV(level int, msg string, kv ...any) {
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(" ")
		b.WriteString(fmt.Sprintf("%v=%v", kv[i], kv[i+1]))
	}
	mosqLog(C.int(level), b.String())
}

func cstr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

func markConnected(client *C.struct_mosquitto) {
	if client == nil {
		return
	}
	key := uintptr(unsafe.Pointer(client))
	activeConnMu.Lock()
	activeConn[key] = struct{}{}
	activeConnMu.Unlock()
}

func connected(client *C.struct_mosquitto) bool {
	if client == nil {
		return false
	}
	key := uintptr(unsafe.Pointer(client))
	activeConnMu.Lock()
	_, ok := activeConn[key]
	activeConnMu.Unlock()
	return ok
}

func clearConnected(client *C.struct_mosquitto) {
	if client == nil {
		return
	}
	key := uintptr(unsafe.Pointer(client))
	activeConnMu.Lock()
	delete(activeConn, key)
	activeConnMu.Unlock()
}

// clientInfoFromDisconnect 提取断开事件信息与原因码。
func clientInfoFromDisconnect(ed *C.struct_mosquitto_evt_disconnect) (clientInfo, int) {
	info := clientInfo{}
	if ed.client == nil {
		return info, int(ed.reason)
	}
	info.clientID = cstr(C.mosquitto_client_id(ed.client))
	info.username = cstr(C.mosquitto_client_username(ed.client))
	info.peer = cstr(C.mosquitto_client_address(ed.client))
	info.protocol = pluginutil.ProtocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	return info, int(ed.reason)
}

// clientInfoFromConnect 提取连接事件信息。
func clientInfoFromConnect(ed *C.struct_mosquitto_evt_connect) clientInfo {
	info := clientInfo{}
	if ed == nil || ed.client == nil {
		return info
	}
	info.clientID = cstr(C.mosquitto_client_id(ed.client))
	info.username = cstr(C.mosquitto_client_username(ed.client))
	info.peer = cstr(C.mosquitto_client_address(ed.client))
	info.protocol = pluginutil.ProtocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	return info
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
			mosqLog(C.MOSQ_LOG_ERR, "conn-plugin: panic in plugin_init: %v\n%s", r, string(debug.Stack()))
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id

	if env := os.Getenv("PG_DSN"); env != "" {
		pgDSN = env
	}

	for _, o := range unsafe.Slice(opts, int(optCount)) {
		k, v := cstr(o.key), cstr(o.value)
		switch k {
		case "conn_pg_dsn":
			pgDSN = v
		case "conn_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				timeout = dur
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: invalid conn_timeout_ms=%q, keeping existing value %dms",
					v, int(timeout/time.Millisecond))
			}
		case "conn_debug":
			if parsed, ok := pluginutil.ParseBoolOption(v); ok {
				debugEnabled = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: invalid conn_debug=%q, keeping existing value %t",
					v, debugEnabled)
			}
		}
	}

	if pgDSN == "" {
		mosqLog(C.MOSQ_LOG_ERR, "conn-plugin: conn_pg_dsn must be set")
		return C.MOSQ_ERR_UNKNOWN
	}

	logKV(mosqLogInfo, "conn-plugin: initializing",
		"pg_dsn", pluginutil.SafeDSN(pgDSN),
		"timeout_ms", int(timeout/time.Millisecond),
		"debug", debugEnabled,
	)

	if _, err := poolConfig(); err != nil {
		mosqLog(C.MOSQ_LOG_ERR, "conn-plugin: invalid pg_dsn (%s): %v", pluginutil.SafeDSN(pgDSN), err)
		return C.MOSQ_ERR_UNKNOWN
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if _, err := ensurePool(ctx); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: initial pg connection failed: %v (will retry lazily)", err)
	}

	if rc := C.register_event_callback(pid, C.MOSQ_EVT_CONNECT, C.mosq_event_cb(C.connect_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_DISCONNECT, C.mosq_event_cb(C.disconnect_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		C.unregister_event_callback(pid, C.MOSQ_EVT_CONNECT, C.mosq_event_cb(C.connect_cb_c))
		return rc
	}

	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

//export go_mosq_plugin_cleanup
// go_mosq_plugin_cleanup 注销回调并释放连接池。
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_CONNECT, C.mosq_event_cb(C.connect_cb_c))
	C.unregister_event_callback(pid, C.MOSQ_EVT_DISCONNECT, C.mosq_event_cb(C.disconnect_cb_c))
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()
	activeConnMu.Lock()
	activeConn = map[uintptr]struct{}{}
	activeConnMu.Unlock()
	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

//export connect_cb_c
// connect_cb_c 处理客户端连接事件。
func connect_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_connect)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	markConnected(ed.client)
	info := clientInfoFromConnect(ed)
	if err := recordConnectEvent(info); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: record connect event failed: %v", err)
	}

	return C.MOSQ_ERR_SUCCESS
}

//export disconnect_cb_c
// disconnect_cb_c 处理客户端断开事件。
func disconnect_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_disconnect)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	if !connected(ed.client) {
		if debugEnabled {
			logKV(7, "conn-plugin: skip disconnect record", "client_ptr", fmt.Sprintf("%p", ed.client))
		}
		return C.MOSQ_ERR_SUCCESS
	}
	info, reason := clientInfoFromDisconnect(ed)
	if err := recordDisconnectEvent(info, reason); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: record disconnect event failed: %v", err)
		return C.MOSQ_ERR_SUCCESS
	}
	clearConnected(ed.client)

	return C.MOSQ_ERR_SUCCESS
}

func main() {}
