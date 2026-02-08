package main

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"mosquitto-plugin/internal/pluginutil"
)

// dbAuth 执行认证逻辑并返回结果/原因。
func dbAuth(username, password, clientID string) (bool, string, error) {
	if username == "" || password == "" {
		return false, authReasonMissingCreds, nil
	}
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()

	p, err := pluginutil.EnsureSharedPGPool(ctx, &poolMu, &pool, pgDSN)
	if err != nil {
		return false, authReasonDBError, err
	}

	var hash string
	var salt string
	var enabledInt int16
	err = p.QueryRow(ctx, selectAuthAccountSQL, username, clientID).
		Scan(&hash, &salt, &enabledInt)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, authReasonUserNotFound, nil
	}
	if err != nil {
		return false, authReasonDBError, err
	}
	if enabledInt == 0 {
		return false, authReasonUserDisabled, nil
	}
	if hash != pluginutil.SHA256PwdSalt(password, salt) {
		return false, authReasonInvalidPassword, nil
	}

	return true, authReasonOK, nil
}

// recordAuthEvent 写入认证事件。
func recordAuthEvent(info pluginutil.ClientInfo, result, reason string) error {
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()

	p, err := pluginutil.EnsureSharedPGPool(ctx, &poolMu, &pool, pgDSN)
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
