package main

/*
#cgo darwin pkg-config: libmosquitto
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto
#include <stdlib.h>
#include <mosquitto.h>
#include <mosquitto_plugin.h>
#include <mosquitto_broker.h>

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int disconnect_cb_c(int event, void *event_data, void *userdata);

int register_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
int unregister_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
void go_mosq_log(int level, const char* msg);
*/
import "C"

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jackc/pgx/v5/pgxpool"
)

const recordEventSQL = `
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
  last_connect_ts = COALESCE(EXCLUDED.last_connect_ts, mqtt_client_latest_events.last_connect_ts),
  last_disconnect_ts = COALESCE(EXCLUDED.last_disconnect_ts, mqtt_client_latest_events.last_disconnect_ts),
  last_peer = EXCLUDED.last_peer,
  last_protocol = EXCLUDED.last_protocol,
  last_reason_code = EXCLUDED.last_reason_code,
  extra = EXCLUDED.extra
`

const (
	connEventTypeDisconnect = "disconnect"
)

var (
	pid          *C.mosquitto_plugin_id_t
	pool         *pgxpool.Pool
	poolMu       sync.RWMutex
	pgDSN        string
	timeout      = 1000 * time.Millisecond
	debugEnabled bool
)

func mosqLog(level C.int, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	C.go_mosq_log(level, cs)
}

func debugLog(msg string, args ...any) {
	if !debugEnabled {
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

type clientInfo struct {
	clientID string
	username string
	peer     string
	protocol string
}

func clientInfoFromDisconnect(ed *C.struct_mosquitto_evt_disconnect) (clientInfo, int) {
	info := clientInfo{}
	if ed.client == nil {
		return info, int(ed.reason)
	}
	info.clientID = cstr(C.mosquitto_client_id(ed.client))
	info.username = cstr(C.mosquitto_client_username(ed.client))
	info.peer = cstr(C.mosquitto_client_address(ed.client))
	info.protocol = protocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	return info, int(ed.reason)
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
	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: connected to PostgreSQL successfully")
	return pool, nil
}

func ctxTimeout() (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), timeout)
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

// --- Init ---
//
//export go_mosq_plugin_init
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
	if env := os.Getenv("CONN_PG_DSN"); env != "" {
		pgDSN = env
	}

	for _, o := range unsafe.Slice(opts, int(optCount)) {
		k, v := cstr(o.key), cstr(o.value)
		switch k {
		case "conn_pg_dsn":
			pgDSN = v
		case "conn_timeout_ms":
			if dur, ok := parseTimeoutMS(v); ok {
				timeout = dur
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: invalid conn_timeout_ms=%q, keeping existing value %dms",
					v, int(timeout/time.Millisecond))
			}
		case "conn_debug":
			if parsed, ok := parseBoolOption(v); ok {
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

	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: initializing pg_dsn=%s timeout_ms=%d debug=%t",
		safeDSN(pgDSN), int(timeout/time.Millisecond), debugEnabled)

	if _, err := poolConfig(); err != nil {
		mosqLog(C.MOSQ_LOG_ERR, "conn-plugin: invalid pg_dsn (%s): %v", safeDSN(pgDSN), err)
		return C.MOSQ_ERR_UNKNOWN
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if _, err := ensurePool(ctx); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: initial pg connection failed: %v (will retry lazily)", err)
	}

	if rc := C.register_event_callback(pid, C.MOSQ_EVT_DISCONNECT, C.mosq_event_cb(C.disconnect_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

// --- Cleanup ---
//
//export go_mosq_plugin_cleanup
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_DISCONNECT, C.mosq_event_cb(C.disconnect_cb_c))
	poolMu.Lock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolMu.Unlock()
	mosqLog(C.MOSQ_LOG_INFO, "conn-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

// --- Event callbacks ---

//export disconnect_cb_c
func disconnect_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_disconnect)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	info, reason := clientInfoFromDisconnect(ed)
	if err := recordDisconnectEvent(info, reason); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "conn-plugin: record disconnect event failed: %v", err)
	}

	return C.MOSQ_ERR_SUCCESS
}

func recordDisconnectEvent(info clientInfo, reason int) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()

	usernameVal := optionalString(info.username)
	peerVal := optionalString(info.peer)
	protocolVal := optionalString(info.protocol)
	reasonVal := reason
	var extraVal any

	var connectTS any
	disconnectTS := ts
	_, err = p.Exec(ctx, recordEventSQL, ts, connEventTypeDisconnect, info.clientID, usernameVal, peerVal, protocolVal, reasonVal, extraVal,
		connectTS, disconnectTS)
	if err != nil {
		return err
	}

	debugLog("conn-plugin: recorded event=%s client_id=%q username=%q peer=%q protocol=%q",
		connEventTypeDisconnect, info.clientID, info.username, info.peer, info.protocol)
	return nil
}

func main() {}
