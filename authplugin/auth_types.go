package main

import (
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authResultSuccess = "success"
	authResultFail    = "fail"

	authReasonOK              = "ok"
	authReasonMissingCreds    = "missing_credentials"
	authReasonUserNotFound    = "user_not_found"
	authReasonUserDisabled    = "user_disabled"
	authReasonInvalidPassword = "invalid_password"
	authReasonDBError         = "db_error"
	authReasonDBErrorFailOpen = "db_error_fail_open"
)

// insertAuthEventSQL 写入认证结果事件。
const insertAuthEventSQL = `
INSERT INTO client_auth_events
  (ts, result, reason, client_id, username, peer, protocol)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

// clientInfo 保存认证/连接事件中的关键信息。
type clientInfo struct {
	clientID string
	username string
	peer     string
	protocol string
}

var (
	// 连接池与配置
	pool        *pgxpool.Pool
	poolMu      sync.RWMutex
	pgDSN       string // postgres://user:pass@host:5432/db?sslmode=verify-full
	timeout     = time.Duration(1500) * time.Millisecond
	failOpen    bool
)
