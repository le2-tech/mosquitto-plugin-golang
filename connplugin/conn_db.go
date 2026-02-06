package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"mosquitto-plugin/internal/pluginutil"
)

// poolConfig 基于 pgDSN 构建连接池配置。
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

// ensurePool 延迟初始化连接池，必要时创建并复用。
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
	logKV(mosqLogInfo, "conn-plugin: connected to PostgreSQL successfully")
	return pool, nil
}

// ctxTimeout 根据配置的超时生成 context。
func ctxTimeout() (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), timeout)
}

// recordDisconnectEvent 写入断开事件与会话快照。
func recordDisconnectEvent(info clientInfo, reason int) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()

	usernameVal := pluginutil.OptionalString(info.username)
	peerVal := pluginutil.OptionalString(info.peer)
	protocolVal := pluginutil.OptionalString(info.protocol)
	reasonVal := reason
	var extraVal any

	var connectTS any
	disconnectTS := ts
	_, err = p.Exec(ctx, recordEventSQL, ts, connEventTypeDisconnect, info.clientID, usernameVal, peerVal, protocolVal, reasonVal, extraVal,
		connectTS, disconnectTS)
	if err != nil {
		return err
	}

	if debugEnabled {
		logKV(7, "conn-plugin: recorded event",
			"event", connEventTypeDisconnect,
			"client_id", info.clientID,
			"username", info.username,
			"peer", info.peer,
			"protocol", info.protocol,
		)
	}
	return nil
}

// recordConnectEvent 写入连接事件与会话快照。
func recordConnectEvent(info clientInfo) error {
	ctx, cancel := ctxTimeout()
	defer cancel()

	p, err := ensurePool(ctx)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()

	usernameVal := pluginutil.OptionalString(info.username)
	peerVal := pluginutil.OptionalString(info.peer)
	protocolVal := pluginutil.OptionalString(info.protocol)
	var reasonVal any
	var extraVal any

	connectTS := ts
	var disconnectTS any
	_, err = p.Exec(ctx, recordEventSQL, ts, connEventTypeConnect, info.clientID, usernameVal, peerVal, protocolVal, reasonVal, extraVal,
		connectTS, disconnectTS)
	if err != nil {
		return err
	}

	if debugEnabled {
		logKV(7, "conn-plugin: recorded event",
			"event", connEventTypeConnect,
			"client_id", info.clientID,
			"username", info.username,
			"peer", info.peer,
			"protocol", info.protocol,
		)
	}
	return nil
}
