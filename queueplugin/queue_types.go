package main

import "time"

// failMode 控制发布到 RabbitMQ 失败时的处理策略。
type failMode int

const (
	failModeDrop failMode = iota
	failModeBlock
	failModeDisconnect
)

// config 保存从 Mosquitto 配置解析出的运行参数。
type config struct {
	backend         string
	dsn             string
	exchange        string
	exchangeType    string
	routingKey      string
	queueName       string
	timeout         time.Duration
	failMode        failMode
	debug           bool
	includeTopics   []string
	excludeTopics   []string
	includeUsers    map[string]struct{}
	excludeUsers    map[string]struct{}
	includeClients  map[string]struct{}
	excludeClients  map[string]struct{}
	includeRetained bool
}

// queueMessage 是发送到 RabbitMQ 的 JSON 负载。
type queueMessage struct {
	QueueTS        string         `json:"queue_ts"`
	Topic          string         `json:"topic"`
	PayloadB64     string         `json:"payload_b64"`
	QoS            uint8          `json:"qos"`
	Retain         bool           `json:"retain"`
	ClientID       string         `json:"client_id,omitempty"`
	Username       string         `json:"username,omitempty"`
	Peer           string         `json:"peer,omitempty"`
	Protocol       string         `json:"protocol,omitempty"`
	UserProperties []userProperty `json:"user_properties,omitempty"`
}

// userProperty 对应 MQTT v5 的用户属性。
type userProperty struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

var (
	cfg       config
	publisher amqpPublisher
)
