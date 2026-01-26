package main

/*
#cgo darwin pkg-config: libmosquitto
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto
#include <stdlib.h>
#include <mosquitto.h>
#include <mosquitto_plugin.h>
#include <mosquitto_broker.h>


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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pid         *C.mosquitto_plugin_id_t
	pool        *pgxpool.Pool
	poolMu      sync.RWMutex
	pgDSN       string // postgres://user:pass@host:5432/db?sslmode=verify-full
	timeout     = time.Duration(1500) * time.Millisecond
	failOpen    bool
	enforceBind bool
)

const (
	authResultSuccess = "success"
	authResultFail    = "fail"

	authReasonOK              = "ok"
	authReasonMissingCreds    = "missing_credentials"
	authReasonUserNotFound    = "user_not_found"
	authReasonUserDisabled    = "user_disabled"
	authReasonInvalidPassword = "invalid_password"
	authReasonClientNotBound  = "client_not_bound"
	authReasonDBError         = "db_error"
	authReasonDBErrorFailOpen = "db_error_fail_open"
)

const (
	connEventTypeConnect = "connect"
)

const insertAuthEventSQL = `
INSERT INTO mqtt_client_auth_events
  (ts, result, reason, client_id, username, peer, protocol)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

const recordConnEventSQL = `
WITH ins AS (
  INSERT INTO mqtt_client_events
    (ts, event_type, client_id, username, peer, protocol, reason_code, extra)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
  RETURNING 1
)
INSERT INTO mqtt_client_latest_events
  (client_id, username, last_event_ts, last_event_type, last_connect_ts, last_disconnect_ts,
   last_peer, last_protocol, last_reason_code, extra)
SELECT $3, $4, $1, $2, $9, $10, $5, $6, $7, $8
FROM ins
ON CONFLICT (client_id) DO UPDATE SET
  username = EXCLUDED.username,
  last_event_ts = EXCLUDED.last_event_ts,
  last_event_type = EXCLUDED.last_event_type,
  last_connect_ts = EXCLUDED.last_connect_ts,
  last_disconnect_ts = EXCLUDED.last_disconnect_ts,
  last_peer = EXCLUDED.last_peer,
  last_protocol = EXCLUDED.last_protocol,
  last_reason_code = EXCLUDED.last_reason_code,
  extra = EXCLUDED.extra
`

func mosqLog(level C.int, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	C.go_mosq_log(level, cs)
}

func cstr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

type clientInfo struct {
	clientID string
	username string
	peer     string
	protocol string
}

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
	info.protocol = protocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	return info
}

func envBool(name string) bool {
	if v, ok := parseBoolOption(os.Getenv(name)); ok {
		return v
	}
	return false
}
func safeDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}

func optionalString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseBoolOption(v string) (value bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "t", "yes", "y", "on":
		return true, true
	case "0", "false", "f", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

func parseTimeoutMS(v string) (time.Duration, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, false
	}
	return time.Duration(n) * time.Millisecond, true
}

func poolConfig() (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(pgDSN)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 60 * time.Second
	cfg.HealthCheckPeriod = 30 * time.Second
	return cfg, nil
}

func ensurePool(ctx context.Context) (*pgxpool.Pool, error) {
	poolMu.RLock()
	current := pool
	poolMu.RUnlock()
	if current != nil {
		return current, nil
	}

	poolMu.Lock()
	defer poolMu.Unlock()

	if pool != nil {
		return pool, nil
	}

	cfg, err := poolConfig()
	if err != nil {
		return nil, err
	}

	newPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := newPool.Ping(ctx); err != nil {
		newPool.Close()
		return nil, err
	}
	pool = newPool
	mosqLog(C.MOSQ_LOG_INFO, "auth-plugin: connected to PostgreSQL successfully")
	return pool, nil
}

func sha256PwdSalt(pwd, salt string) string {
	sum := sha256.Sum256([]byte(pwd + salt))
	return hex.EncodeToString(sum[:])
}

func protocolString(version int) string {
	switch version {
	case 3:
		return "MQTT/3.1"
	case 4:
		return "MQTT/3.1.1"
	case 5:
		return "MQTT/5.0"
	default:
		return ""
	}
}

// --- Version negotiation ---
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

// --- Init （注意：userdata 是 void**，这里用 **C.pvoid 对应）---
//
//export go_mosq_plugin_init
func go_mosq_plugin_init(id *C.mosquitto_plugin_id_t, userdata *unsafe.Pointer,
	opts *C.struct_mosquitto_opt, optCount C.int) (rc C.int) {

	defer func() {
		if r := recover(); r != nil {
			mosqLog(C.MOSQ_LOG_ERR, "auth-plugin: panic in plugin_init: %v\n%s", r, string(debug.Stack()))
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
			if dur, ok := parseTimeoutMS(v); ok {
				timeout = dur
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin: invalid timeout_ms=%q, keeping existing value %dms",
					v, int(timeout/time.Millisecond))
			}
		case "fail_open":
			if parsed, ok := parseBoolOption(v); ok {
				failOpen = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin: invalid fail_open=%q, keeping existing value %t",
					v, failOpen)
			}
		case "enforce_bind":
			if parsed, ok := parseBoolOption(v); ok {
				enforceBind = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin: invalid enforce_bind=%q, keeping existing value %t",
					v, enforceBind)
			}
		}
	}
	if pgDSN == "" {
		mosqLog(C.MOSQ_LOG_ERR, "auth-plugin: pg_dsn must be set")
		return C.MOSQ_ERR_UNKNOWN
	}

	mosqLog(C.MOSQ_LOG_INFO, "auth-plugin: initializing pg_dsn=%s timeout_ms=%d fail_open=%t enforce_bind=%t",
		safeDSN(pgDSN), int(timeout/time.Millisecond), failOpen, enforceBind)

	// 验证 PG 配置；数据库暂不可用时不阻塞插件加载
	if _, err := poolConfig(); err != nil {
		mosqLog(C.MOSQ_LOG_ERR, "auth-plugin: invalid pg_dsn (%s): %v", safeDSN(pgDSN), err)
		return C.MOSQ_ERR_UNKNOWN
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if _, err := ensurePool(ctx); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin: initial pg connection failed: %v (will retry lazily)", err)
	}

	// 注册回调
	if rc := C.register_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	mosqLog(C.MOSQ_LOG_INFO, "auth-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

// --- Cleanup （void** 对应 **C.pvoid）---
//
// --- Cleanup: 头文件是 void *userdata —— 在 Go 里用 unsafe.Pointer 承接 ---
//
//export go_mosq_plugin_cleanup
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_BASIC_AUTH, C.mosq_event_cb(C.basic_auth_cb_c))
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()
	mosqLog(C.MOSQ_LOG_INFO, "auth-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

// -------- BASIC_AUTH / ACL_CHECK 回调保持不变 --------

//export basic_auth_cb_c
func basic_auth_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_basic_auth)(event_data)
	password := cstr(ed.password)
	info := clientInfoFromBasicAuth(ed)

	allow, reason, err := dbAuth(info.username, password, info.clientID)
	result := authResultFail
	finalReason := reason
	if err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin auth error: "+err.Error())
		if failOpen {
			mosqLog(C.MOSQ_LOG_INFO, "auth-plugin: fail_open=true, allowing auth despite error")
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
		mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin auth event log failed: %v", err)
	}
	if allow {
		if err := recordConnectEvent(info); err != nil {
			mosqLog(C.MOSQ_LOG_WARNING, "auth-plugin connect event log failed: %v", err)
		}
		return C.MOSQ_ERR_SUCCESS
	}
	return C.MOSQ_ERR_AUTH
}

//export acl_check_cb_c
func acl_check_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	return C.MOSQ_ERR_PLUGIN_DEFER
}

// ----------------- PostgreSQL 逻辑（与你现有一致） -----------------

func ctxTimeout() (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), timeout)
}

func dbAuth(username, password, clientID string) (bool, string, error) {
	if username == "" || password == "" {
		return false, authReasonMissingCreds, nil
	}
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return false, authReasonDBError, err
	}

	var hash string
	var salt string
	var enabledInt int16
	err = p.QueryRow(ctx,
		"SELECT password_hash, salt, enabled FROM mqtt_devices WHERE username=$1",
		username).Scan(&hash, &salt, &enabledInt)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, authReasonUserNotFound, nil
	}
	if err != nil {
		return false, authReasonDBError, err
	}
	if enabledInt == 0 {
		return false, authReasonUserDisabled, nil
	}
	if hash != sha256PwdSalt(password, salt) {
		return false, authReasonInvalidPassword, nil
	}

	if enforceBind {
		var ok int
		err = p.QueryRow(ctx,
			"SELECT 1 FROM client_bindings WHERE username=$1 AND client_id=$2",
			username, clientID).Scan(&ok)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, authReasonClientNotBound, nil
		}
		if err != nil {
			return false, authReasonDBError, err
		}
	}
	return true, authReasonOK, nil
}

func recordAuthEvent(info clientInfo, result, reason string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()

	_, err = p.Exec(ctx, insertAuthEventSQL, ts, result, reason,
		optionalString(info.clientID), optionalString(info.username), optionalString(info.peer), optionalString(info.protocol))
	return err
}

func recordConnectEvent(info clientInfo) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()
	eventType := connEventTypeConnect
	userVal := optionalString(info.username)
	peerVal := optionalString(info.peer)
	protocolVal := optionalString(info.protocol)
	var reasonVal any
	var extraVal any

	connectTS := ts
	var disconnectTS any
	_, err = p.Exec(ctx, recordConnEventSQL, ts, eventType, info.clientID, userVal, peerVal, protocolVal, reasonVal, extraVal,
		connectTS, disconnectTS)
	return err
}

func main() {
	println("hit! pid:", os.Getpid())
}
