package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mosquitto-plugin/internal/pluginutil"
)

type authAccount struct {
	passwordHash string
	salt         string
	enabled      int16
}

var fetchAuthAccount = func(ctx context.Context, username, clientID string) (authAccount, error) {
	p, err := ensureAuthPool(ctx)
	if err != nil {
		return authAccount{}, err
	}

	var acc authAccount
	err = p.QueryRow(ctx, selectAuthAccountSQL, username, clientID).
		Scan(&acc.passwordHash, &acc.salt, &acc.enabled)
	if err != nil {
		return authAccount{}, err
	}
	return acc, nil
}

var insertAuthEvent = func(ctx context.Context, info pluginutil.ClientInfo, result, reason string) error {
	p, err := ensureAuthPool(ctx)
	if err != nil {
		return err
	}

	_, err = p.Exec(ctx, insertAuthEventSQL,
		time.Now().UTC(),
		result,
		reason,
		pluginutil.OptionalString(info.ClientID),
		pluginutil.OptionalString(info.Username),
		pluginutil.OptionalString(info.Peer),
		pluginutil.OptionalString(info.Protocol),
	)
	return err
}

func ensureAuthPool(ctx context.Context) (*pgxpool.Pool, error) {
	p, ev, err := pluginutil.EnsureSharedPGPool(ctx, &poolMu, &pool, pgDSN)
	if err != nil {
		return nil, err
	}
	if ev == pluginutil.PGPoolEventConnected {
		log(mosqLogInfo, "auth-plugin: postgres pool connected", map[string]any{"pg_dsn": pluginutil.SafeDSN(pgDSN)})
	}
	return p, nil
}

// dbAuth 执行认证逻辑并返回结果/原因。
func dbAuth(username, password, clientID string) (bool, string, error) {
	if username == "" || password == "" {
		return false, authReasonMissingCreds, nil
	}
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()

	acc, err := fetchAuthAccount(ctx, username, clientID)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, authReasonUserNotFound, nil
	}
	if err != nil {
		return false, authReasonDBError, err
	}
	if acc.enabled == 0 {
		return false, authReasonUserDisabled, nil
	}
	if acc.passwordHash != pluginutil.SHA256PwdSalt(password, acc.salt) {
		return false, authReasonInvalidPassword, nil
	}

	return true, authReasonOK, nil
}

// recordAuthEvent 写入认证事件。
func recordAuthEvent(info pluginutil.ClientInfo, result, reason string) error {
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()
	return insertAuthEvent(ctx, info, result, reason)
}
