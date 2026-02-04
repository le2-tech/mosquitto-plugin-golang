package main

import (
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// recordEventSQL 同时写入事件表并更新会话表。
const recordEventSQL = `
WITH ins AS (
  INSERT INTO client_conn_events
    (ts, event_type, client_id, username, peer, protocol, reason_code, extra)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
  RETURNING 1
)
INSERT INTO client_sessions
  (client_id, username, last_event_ts, last_event_type, last_connect_ts, last_disconnect_ts,
   last_peer, last_protocol, last_reason_code, extra)
SELECT $3, $4, $1, $2, $9, $10, $5, $6, $7, $8
FROM ins
ON CONFLICT (client_id) DO UPDATE SET
  username = EXCLUDED.username,
  last_event_ts = EXCLUDED.last_event_ts,
  last_event_type = EXCLUDED.last_event_type,
  last_connect_ts = COALESCE(EXCLUDED.last_connect_ts, client_sessions.last_connect_ts),
  last_disconnect_ts = EXCLUDED.last_disconnect_ts,
  last_peer = EXCLUDED.last_peer,
  last_protocol = EXCLUDED.last_protocol,
  last_reason_code = EXCLUDED.last_reason_code,
  extra = EXCLUDED.extra
`

const (
	connEventTypeConnect    = "connect"
	connEventTypeDisconnect = "disconnect"
)

// clientInfo 保存连接/断开事件中关键信息。
type clientInfo struct {
	clientID string
	username string
	peer     string
	protocol string
}

var (
	// 连接池与配置
	pool         *pgxpool.Pool
	poolMu       sync.RWMutex
	pgDSN        string
	timeout      = 1000 * time.Millisecond
	debugEnabled bool

	// 已建立连接的连接句柄（用于过滤认证失败导致的断开事件）
	activeConnMu sync.Mutex
	activeConn   = map[uintptr]struct{}{}
)
