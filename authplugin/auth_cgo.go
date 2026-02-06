package main

/*
#cgo darwin pkg-config: libmosquitto libcjson
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto libcjson
#include <stdlib.h>
#include <mosquitto.h>

typedef void* pvoid;

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int basic_auth_cb_c(int event, void *event_data, void *userdata);
int acl_check_cb_c(int event, void *event_data, void *userdata);

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

// clientInfoFromBasicAuth 提取 BASIC_AUTH 事件中的客户端信息。
func clientInfoFromBasicAuth(ed *C.struct_mosquitto_evt_basic_auth) clientInfo {
	info := clientInfo{
		username: cstr(ed.username),
	}
	if ed.client == nil {
		return info
	}
	info.clientID = cstr(C.mosquitto_client_id(ed.client))
	if info.username == "" {
		info.username = cstr(C.mosquitto_client_username(ed.client))
	}
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
			logKV(int(C.MOSQ_LOG_ERR), "auth-plugin: panic in plugin_init",
				"panic", r,
				"stack", string(debug.Stack()),
			)
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id

	// 先从环境变量读默认值
	if env := os.Getenv("PG_DSN"); env != "" {
		pgDSN = env
	}

	// 读取 plugin_opt_*
	for _, o := range unsafe.Slice(opts, int(optCount)) {
		k, v := cstr(o.key), cstr(o.value)
		switch k {
		case "pg_dsn":
			pgDSN = v
		case "timeout_ms":
			if dur, ok := pluginutil.ParseTimeoutMS(v); ok {
				timeout = dur
			} else {
				logKV(C.MOSQ_LOG_WARNING, "auth-plugin: invalid timeout_ms",
					"value", v,
					"timeout_ms", int(timeout/time.Millisecond),
				)
			}
		case "fail_open":
			if parsed, ok := pluginutil.ParseBoolOption(v); ok {
				failOpen = parsed
			} else {
				logKV(C.MOSQ_LOG_WARNING, "auth-plugin: invalid fail_open",
					"value", v,
					"fail_open", failOpen,
				)
			}
		}
	}
	if pgDSN == "" {
		logKV(int(C.MOSQ_LOG_ERR), "auth-plugin: pg_dsn must be set")
		return C.MOSQ_ERR_UNKNOWN
	}

	logKV(mosqLogInfo, "auth-plugin: initializing",
		"pg_dsn", pluginutil.SafeDSN(pgDSN),
		"timeout_ms", int(timeout/time.Millisecond),
		"fail_open", failOpen,
	)

	// 验证 PG 配置；数据库暂不可用时不阻塞插件加载
	if _, err := poolConfig(); err != nil {
		logKV(int(C.MOSQ_LOG_ERR), "auth-plugin: invalid pg_dsn",
			"pg_dsn", pluginutil.SafeDSN(pgDSN),
			"error", err.Error(),
		)
		return C.MOSQ_ERR_UNKNOWN
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if _, err := ensurePool(ctx); err != nil {
		logKV(int(C.MOSQ_LOG_WARNING), "auth-plugin: initial pg connection failed",
			"error", err.Error(),
		)
	}

	// 注册回调
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	logKV(mosqLogInfo, "auth-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

//export go_mosq_plugin_cleanup
// go_mosq_plugin_cleanup 注销回调并释放连接池。
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c))
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()
	logKV(mosqLogInfo, "auth-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

//export basic_auth_cb_c
// basic_auth_cb_c 执行认证逻辑并返回结果。
func basic_auth_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_basic_auth)(event_data)
	password := cstr(ed.password)
	info := clientInfoFromBasicAuth(ed)

	allow, reason, err := dbAuth(info.username, password, info.clientID)
	result := authResultFail
	finalReason := reason
	if err != nil {
		logKV(int(C.MOSQ_LOG_WARNING), "auth-plugin auth error", "error", err.Error())
		if failOpen {
			logKV(mosqLogInfo, "auth-plugin: fail_open allow auth", "reason", "db_error")
			allow = true
			result = authResultSuccess
			finalReason = authReasonDBErrorFailOpen
		} else {
			finalReason = authReasonDBError
		}
	} else if allow {
		result = authResultSuccess
	}
	if err := recordAuthEvent(info, result, finalReason); err != nil {
		logKV(int(C.MOSQ_LOG_WARNING), "auth-plugin auth event log failed", "error", err.Error())
	}
	if allow {
		return C.MOSQ_ERR_SUCCESS
	}
	return C.MOSQ_ERR_AUTH
}

//export acl_check_cb_c
// acl_check_cb_c 保持默认：交给后续插件/内置 ACL 处理。
func acl_check_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	return C.MOSQ_ERR_PLUGIN_DEFER
}

func main() {}
