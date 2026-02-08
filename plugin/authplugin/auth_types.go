package main

import (
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultTimeout = 1500 * time.Millisecond

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

// selectAuthAccountSQL 读取账户密文、盐和启用状态。
const selectAuthAccountSQL = `
SELECT password_hash, salt, enabled
FROM mqtt_accounts
WHERE user_name=$1
  AND (clientid=$2 OR clientid IS NULL)
`

// insertAuthEventSQL 写入认证结果事件。
const insertAuthEventSQL = `
INSERT INTO client_auth_events
  (ts, result, reason, client_id, username, peer, protocol)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

var (
	pool   *pgxpool.Pool
	poolMu sync.RWMutex

	pgDSN    string // postgres://user:pass@host:5432/db?sslmode=verify-full
	timeout  = defaultTimeout
	failOpen bool
)
