package main

import (
	"encoding/json"
	"time"
)

// failMode 控制发布到 RabbitMQ 失败时的处理策略。
type failMode int

const (
	failModeDrop failMode = iota
	failModeBlock
	failModeDisconnect

	defaultDispatchBuffer = 4096
	debugSampleEvery      = uint64(128)
)

// config 保存从 Mosquitto 配置解析出的运行参数。
type config struct {
	backend        string
	dsn            string
	exchange       string
	exchangeType   string
	routingKey     string
	queueName      string
	enqueueTimeout time.Duration
	publishTimeout time.Duration
	failMode       failMode
}

// queueMessage 是发送到 RabbitMQ 的 JSON 负载。
type queueMessage struct {
	TS             string          `json:"ts"`
	Topic          string          `json:"topic"`
	Payload        json.RawMessage `json:"payload"`
	QoS            uint8           `json:"qos"`
	Retain         bool            `json:"retain"`
	ClientID       string          `json:"client_id,omitempty"`
	Username       string          `json:"username,omitempty"`
	Peer           string          `json:"peer,omitempty"`
	Protocol       string          `json:"protocol,omitempty"`
	UserProperties []userProperty  `json:"user_properties,omitempty"`
}

// userProperty 对应 MQTT v5 的用户属性。
type userProperty struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

var (
	cfg       config
	publisher amqpPublisher

	debugFilterCounter  uint64
	debugPublishCounter uint64
	workerWarnCounter   uint64
	backpressureCounter uint64
)
