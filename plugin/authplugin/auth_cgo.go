package main

/*
#cgo darwin pkg-config: libmosquitto libcjson
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto libcjson
#include <stdlib.h>
#include <mosquitto.h>

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int basic_auth_cb_c(int event, void *event_data, void *userdata);

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
	mosqLogInfo    = int(C.MOSQ_LOG_INFO)
	mosqLogWarning = int(C.MOSQ_LOG_WARNING)
	mosqLogError   = int(C.MOSQ_LOG_ERR)
)

var (
	dbAuthFn          = dbAuth
	recordAuthEventFn = recordAuthEvent
	infoLogger        = func(msg string, fields map[string]any) {
		log(mosqLogInfo, msg, fields)
	}
	warnLogger = func(msg string, fields map[string]any) {
		log(mosqLogWarning, msg, fields)
	}
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

// clientInfoFromBasicAuth 提取 BASIC_AUTH 事件中的客户端信息。
func clientInfoFromBasicAuth(ed *C.struct_mosquitto_evt_basic_auth) pluginutil.ClientInfo {
	info := pluginutil.ClientInfo{
		Username: cstr(ed.username),
	}
	if ed.client == nil {
		return info
	}
	info.ClientID = cstr(C.mosquitto_client_id(ed.client))
	if info.Username == "" {
		info.Username = cstr(C.mosquitto_client_username(ed.client))
	}
	info.Peer = cstr(C.mosquitto_client_address(ed.client))
	info.Protocol = pluginutil.ProtocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	return info
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
			log(mosqLogError, "auth-plugin: panic in plugin_init", map[string]any{"panic": r, "stack": string(debug.Stack())})
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id
	pgDSN = ""
	timeout = defaultTimeout
	failOpen = false
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()

	if env := os.Getenv("PG_DSN"); env != "" {
		pgDSN = env
	}
	for _, o := range unsafe.Slice(opts, int(optCount)) {
		key, value := cstr(o.key), cstr(o.value)
		switch key {
		case "pg_dsn":
			pgDSN = value
		case "timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(value); ok {
				timeout = dur
			} else {
				log(mosqLogWarning, "auth-plugin: invalid timeout_ms", map[string]any{"value": value, "timeout_ms": int(timeout / time.Millisecond)})
			}
		case "fail_open":
			if parsed, ok := pluginutil.ParseBoolOption(value); ok {
				failOpen = parsed
			} else {
				log(mosqLogWarning, "auth-plugin: invalid fail_open", map[string]any{"value": value, "fail_open": failOpen})
			}
		}
	}
	if pgDSN == "" {
		log(mosqLogError, "auth-plugin: pg_dsn must be set")
		return C.MOSQ_ERR_UNKNOWN
	}
	if _, err := pgxpool.ParseConfig(pgDSN); err != nil {
		log(mosqLogError, "auth-plugin: invalid pg_dsn", map[string]any{"pg_dsn": pluginutil.SafeDSN(pgDSN), "error": err.Error()})
		return C.MOSQ_ERR_UNKNOWN
	}

	log(mosqLogInfo, "auth-plugin: initializing", map[string]any{"pg_dsn": pluginutil.SafeDSN(pgDSN), "timeout_ms": int(timeout / time.Millisecond), "fail_open": failOpen})

	// 数据库暂不可用时不阻塞插件加载
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()
	_, err := ensureAuthPool(ctx)
	if err != nil {
		log(mosqLogWarning, "auth-plugin: initial pg connection failed", map[string]any{"error": err.Error()})
	}

	// 注册回调
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	log(mosqLogInfo, "auth-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

// go_mosq_plugin_cleanup 注销回调并释放连接池。
//
//export go_mosq_plugin_cleanup
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c))
	poolMu.Lock()
	defer poolMu.Unlock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	log(mosqLogInfo, "auth-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

func authResultCode(allow bool) C.int {
	if allow {
		return C.MOSQ_ERR_SUCCESS
	}
	return C.MOSQ_ERR_AUTH
}

func runBasicAuth(info pluginutil.ClientInfo, password string) C.int {
	dbAllow, dbReason, err := dbAuthFn(info.Username, password, info.ClientID)
	allow, result, reason := dbAllow, authResultFail, dbReason
	if err != nil {
		warnLogger("auth-plugin auth error", map[string]any{"error": err.Error()})
		reason = authReasonDBError
		if failOpen {
			infoLogger("auth-plugin: fail_open allow auth", map[string]any{"reason": authReasonDBError})
			allow = true
			result = authResultSuccess
			reason = authReasonDBErrorFailOpen
		}
	} else if allow {
		result = authResultSuccess
	}

	if err := recordAuthEventFn(info, result, reason); err != nil {
		warnLogger("auth-plugin auth event log failed", map[string]any{"error": err.Error()})
	}
	return authResultCode(allow)
}

// basic_auth_cb_c 执行认证逻辑并返回结果。
//
//export basic_auth_cb_c
func basic_auth_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	if event_data == nil {
		warnLogger("auth-plugin: nil basic auth event_data", map[string]any{"event": int(event)})
		return C.MOSQ_ERR_AUTH
	}
	ed := (*C.struct_mosquitto_evt_basic_auth)(event_data)
	password := cstr(ed.password)
	info := clientInfoFromBasicAuth(ed)
	return runBasicAuth(info, password)
}

func main() {}
