package main

import (
	"time"

	"mosquitto-plugin/internal/pluginutil"
)

func recordEvent(info pluginutil.ClientInfo, eventType string, reasonCode any) error {
	ctx, cancel := pluginutil.TimeoutContext(timeout)
	defer cancel()

	p, err := pluginutil.EnsureSharedPGPool(ctx, &poolMu, &pool, pgDSN)
	if err != nil {
		return err
	}

	ts := time.Now().UTC()
	connectTS, disconnectTS := any(nil), any(nil)
	if eventType == connEventTypeConnect {
		connectTS = ts
	} else {
		disconnectTS = ts
	}

	_, err = p.Exec(ctx, recordEventSQL,
		ts,
		eventType,
		info.ClientID,
		pluginutil.OptionalString(info.Username),
		pluginutil.OptionalString(info.Peer),
		pluginutil.OptionalString(info.Protocol),
		reasonCode,
		nil,
		connectTS,
		disconnectTS,
	)
	if err != nil {
		return err
	}
	log(mosqLogDebug, "conn-plugin: recorded event", map[string]any{"event": eventType, "client_id": info.ClientID, "username": info.Username, "peer": info.Peer, "protocol": info.Protocol, "reason_code": reasonCode})
	return nil
}
