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
	"os"
	"runtime/debug"
	"time"
	"unsafe"

	"github.com/jackc/pgx/v5/pgxpool"

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

var (
	debugLogger = func(msg string, fields map[string]any) {
		log(mosqLogDebug, msg, fields)
	}
	warnLogger = func(msg string, fields map[string]any) {
		log(mosqLogWarning, msg, fields)
	}
)

func cstr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

func connKey(client *C.struct_mosquitto) (uintptr, bool) {
	if client == nil {
		return 0, false
	}
	return uintptr(unsafe.Pointer(client)), true
}

func setConnectedByKey(key uintptr, on bool) {
	activeConnMu.Lock()
	if on {
		activeConn[key] = struct{}{}
	} else {
		delete(activeConn, key)
	}
	activeConnMu.Unlock()
}

func connectedByKey(key uintptr) bool {
	activeConnMu.Lock()
	_, ok := activeConn[key]
	activeConnMu.Unlock()
	return ok
}

func takeConnectedByKey(key uintptr) bool {
	activeConnMu.Lock()
	_, ok := activeConn[key]
	if ok {
		delete(activeConn, key)
	}
	activeConnMu.Unlock()
	return ok
}

func setConnected(client *C.struct_mosquitto, on bool) {
	key, ok := connKey(client)
	if !ok {
		return
	}
	setConnectedByKey(key, on)
}

func handleDisconnectByKey(key uintptr, record func() error) {
	// 原子地 “检查并清除” 连接状态，避免并发下重复记录 disconnect。
	if !takeConnectedByKey(key) {
		if pluginutil.ShouldSample(&debugSkipCounter, debugSampleEvery) {
			debugLogger("conn-plugin: skip disconnect record", map[string]any{"client_ptr": key})
		}
		return
	}

	if err := record(); err != nil {
		warnLogger("conn-plugin: record disconnect event failed", map[string]any{"error": err.Error()})
	}
}

func clientInfoFromClient(client *C.struct_mosquitto) pluginutil.ClientInfo {
	info := pluginutil.ClientInfo{}
	if client == nil {
		return info
	}
	info.ClientID = cstr(C.mosquitto_client_id(client))
	info.Username = cstr(C.mosquitto_client_username(client))
	info.Peer = cstr(C.mosquitto_client_address(client))
	info.Protocol = pluginutil.ProtocolString(int(C.mosquitto_client_protocol_version(client)))
	return info
}

//export go_mosq_plugin_version
func go_mosq_plugin_version(count C.int, versions *C.int) C.int {
	for _, v := range unsafe.Slice(versions, int(count)) {
		if v == 5 {
			return 5
		}
	}
	return -1
}

//export go_mosq_plugin_init
func go_mosq_plugin_init(id *C.mosquitto_plugin_id_t, userdata *unsafe.Pointer,
	opts *C.struct_mosquitto_opt, optCount C.int) (rc C.int) {

	defer func() {
		if r := recover(); r != nil {
			log(mosqLogError, "conn-plugin: panic in plugin_init", map[string]any{"panic": r, "stack": string(debug.Stack())})
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id
	pgDSN = ""
	timeout = defaultTimeout
	debugSkipCounter = 0
	debugRecordCounter = 0
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()
	activeConnMu.Lock()
	activeConn = map[uintptr]struct{}{}
	activeConnMu.Unlock()

	if env := os.Getenv("PG_DSN"); env != "" {
		pgDSN = env
	}
	for _, o := range unsafe.Slice(opts, int(optCount)) {
		key, value := cstr(o.key), cstr(o.value)
		switch key {
		case "conn_pg_dsn":
			pgDSN = value
		case "conn_timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(value); ok {
				timeout = dur
			} else {
				log(mosqLogWarning, "conn-plugin: invalid conn_timeout_ms", map[string]any{"value": value, "timeout_ms": int(timeout / time.Millisecond)})
			}
		}
	}

	if pgDSN == "" {
		log(mosqLogError, "conn-plugin: conn_pg_dsn must be set")
		return C.MOSQ_ERR_UNKNOWN
	}
	if _, err := pgxpool.ParseConfig(pgDSN); err != nil {
		log(mosqLogError, "conn-plugin: invalid pg_dsn", map[string]any{"pg_dsn": pluginutil.SafeDSN(pgDSN), "error": err.Error()})
		return C.MOSQ_ERR_UNKNOWN
	}

	log(mosqLogInfo, "conn-plugin: initializing", map[string]any{"pg_dsn": pluginutil.SafeDSN(pgDSN), "timeout_ms": int(timeout / time.Millisecond)})

	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()
	_, err := ensureConnPool(ctx)
	if err != nil {
		log(mosqLogWarning, "conn-plugin: initial pg connection failed", map[string]any{"error": err.Error()})
	}

	if rc := C.register_event_callback(pid, C.MOSQ_EVT_CONNECT, C.mosq_event_cb(C.connect_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_DISCONNECT, C.mosq_event_cb(C.disconnect_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		C.unregister_event_callback(pid, C.MOSQ_EVT_CONNECT, C.mosq_event_cb(C.connect_cb_c))
		return rc
	}

	log(mosqLogInfo, "conn-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

//export go_mosq_plugin_cleanup
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

	log(mosqLogInfo, "conn-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

//export connect_cb_c
func connect_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_connect)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	setConnected(ed.client, true)
	if err := recordEvent(clientInfoFromClient(ed.client), connEventTypeConnect, nil); err != nil {
		log(mosqLogWarning, "conn-plugin: record connect event failed", map[string]any{"error": err.Error()})
	}
	return C.MOSQ_ERR_SUCCESS
}

//export disconnect_cb_c
func disconnect_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_disconnect)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}
	key, ok := connKey(ed.client)
	if !ok {
		return C.MOSQ_ERR_SUCCESS
	}
	handleDisconnectByKey(key, func() error {
		return recordEvent(clientInfoFromClient(ed.client), connEventTypeDisconnect, int(ed.reason))
	})
	return C.MOSQ_ERR_SUCCESS
}

func main() {}
