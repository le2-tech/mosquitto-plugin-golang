package main

/*
#cgo darwin pkg-config: libmosquitto
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
#cgo linux  pkg-config: libmosquitto
#include <stdlib.h>
#include <mqtt_protocol.h>
#include <mosquitto.h>
#include <mosquitto_plugin.h>
#include <mosquitto_broker.h>

typedef int (*mosq_event_cb)(int event, void *event_data, void *userdata);

int message_cb_c(int event, void *event_data, void *userdata);

int register_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
int unregister_event_callback(mosquitto_plugin_id_t *id, int event, mosq_event_cb cb);
void go_mosq_log(int level, const char* msg);
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	amqp "github.com/rabbitmq/amqp091-go"
)

type failMode int

const (
	failModeDrop failMode = iota
	failModeBlock
	failModeDisconnect
)

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

type queueMessage struct {
	TS             string         `json:"ts"`
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

type userProperty struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

type amqpPublisher struct {
	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

func (p *amqpPublisher) closeLocked() {
	if p.ch != nil {
		_ = p.ch.Close()
		p.ch = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *amqpPublisher) ensureLocked() error {
	if p.conn != nil && p.conn.IsClosed() {
		p.conn = nil
		p.ch = nil
	}
	if p.ch != nil && p.ch.IsClosed() {
		p.ch = nil
	}
	if p.conn == nil {
		conn, err := amqp.DialConfig(cfg.dsn, amqp.Config{
			Dial: amqp.DefaultDial(cfg.timeout),
		})
		if err != nil {
			return err
		}
		p.conn = conn
		if cfg.debug {
			mosqLog(C.MOSQ_LOG_INFO, "queue-plugin: connected to rabbitmq")
		}
	}
	if p.ch == nil {
		ch, err := p.conn.Channel()
		if err != nil {
			_ = p.conn.Close()
			p.conn = nil
			return err
		}
		p.ch = ch
		if cfg.debug {
			mosqLog(C.MOSQ_LOG_DEBUG, "queue-plugin: channel opened")
		}
	}
	return nil
}

func (p *amqpPublisher) Publish(ctx context.Context, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLocked(); err != nil {
		return err
	}

	err := p.ch.PublishWithContext(ctx, cfg.exchange, cfg.routingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
	if err == nil {
		return nil
	}

	if errors.Is(err, amqp.ErrClosed) || (p.conn != nil && p.conn.IsClosed()) || (p.ch != nil && p.ch.IsClosed()) {
		p.closeLocked()
		if err2 := p.ensureLocked(); err2 != nil {
			return err
		}
		return p.ch.PublishWithContext(ctx, cfg.exchange, cfg.routingKey, false, false, amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		})
	}

	return err
}

var (
	pid       *C.mosquitto_plugin_id_t
	cfg       config
	publisher amqpPublisher
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
	if !cfg.debug {
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

func parseList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func parseSet(v string) map[string]struct{} {
	items := parseList(v)
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

func topicMatch(pattern, topic string) bool {
	if pattern == "#" {
		return true
	}
	pLevels := strings.Split(pattern, "/")
	tLevels := strings.Split(topic, "/")
	for i, p := range pLevels {
		if p == "#" {
			return i == len(pLevels)-1
		}
		if i >= len(tLevels) {
			return false
		}
		if p == "+" {
			continue
		}
		if p != tLevels[i] {
			return false
		}
	}
	return len(pLevels) == len(tLevels)
}

func matchAny(patterns []string, topic string) bool {
	for _, pattern := range patterns {
		if topicMatch(pattern, topic) {
			return true
		}
	}
	return false
}

func setContains(set map[string]struct{}, value string) bool {
	if len(set) == 0 {
		return false
	}
	_, ok := set[value]
	return ok
}

func allowMessage(topic, username, clientID string, retain bool) (bool, string) {
	if !cfg.includeRetained && retain {
		return false, "retained"
	}
	if len(cfg.excludeTopics) > 0 && matchAny(cfg.excludeTopics, topic) {
		return false, "exclude_topic"
	}
	if len(cfg.includeTopics) > 0 && !matchAny(cfg.includeTopics, topic) {
		return false, "not_included_topic"
	}
	if len(cfg.excludeUsers) > 0 && setContains(cfg.excludeUsers, username) {
		return false, "exclude_user"
	}
	if len(cfg.includeUsers) > 0 && !setContains(cfg.includeUsers, username) {
		return false, "not_included_user"
	}
	if len(cfg.excludeClients) > 0 && setContains(cfg.excludeClients, clientID) {
		return false, "exclude_client"
	}
	if len(cfg.includeClients) > 0 && !setContains(cfg.includeClients, clientID) {
		return false, "not_included_client"
	}
	return true, ""
}

func extractUserProperties(props *C.mosquitto_property) []userProperty {
	if props == nil {
		return nil
	}

	var out []userProperty
	var name *C.char
	var value *C.char

	prop := C.mosquitto_property_read_string_pair(props, C.MQTT_PROP_USER_PROPERTY, &name, &value, C.bool(false))
	for prop != nil {
		out = append(out, userProperty{
			Key:   cstr(name),
			Value: cstr(value),
		})
		prop = C.mosquitto_property_read_string_pair(prop, C.MQTT_PROP_USER_PROPERTY, &name, &value, C.bool(true))
	}

	return out
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

func parseFailMode(v string) (failMode, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "drop":
		return failModeDrop, true
	case "block":
		return failModeBlock, true
	case "disconnect":
		return failModeDisconnect, true
	default:
		return failModeDrop, false
	}
}

func failModeString(mode failMode) string {
	switch mode {
	case failModeDrop:
		return "drop"
	case failModeBlock:
		return "block"
	case failModeDisconnect:
		return "disconnect"
	default:
		return "drop"
	}
}

func failResult(err error) C.int {
	if err == nil {
		return C.MOSQ_ERR_SUCCESS
	}
	mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin publish failed: %v", err)
	switch cfg.failMode {
	case failModeDrop:
		return C.MOSQ_ERR_SUCCESS
	case failModeBlock:
		return C.MOSQ_ERR_ACL_DENIED
	case failModeDisconnect:
		return C.MOSQ_ERR_CONN_LOST
	default:
		return C.MOSQ_ERR_SUCCESS
	}
}

//export go_mosq_plugin_version
func go_mosq_plugin_version(count C.int, versions *C.int) C.int {
	for _, v := range unsafe.Slice(versions, int(count)) {
		if v == 5 {
			return 5
		}
	}
	return -1
}

//export go_mosq_plugin_init
func go_mosq_plugin_init(id *C.mosquitto_plugin_id_t, userdata *unsafe.Pointer,
	opts *C.struct_mosquitto_opt, optCount C.int) (rc C.int) {

	defer func() {
		if r := recover(); r != nil {
			mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: panic in plugin_init: %v\n%s", r, string(debug.Stack()))
			rc = C.MOSQ_ERR_UNKNOWN
		}
	}()

	pid = id

	cfg = config{
		backend:         "rabbitmq",
		exchangeType:    "direct",
		timeout:         1000 * time.Millisecond,
		failMode:        failModeDrop,
		includeRetained: false,
		debug:           false,
	}

	if env := os.Getenv("QUEUE_DSN"); env != "" {
		cfg.dsn = env
	}

	seenExcludeTopics := false

	for _, o := range unsafe.Slice(opts, int(optCount)) {
		k, v := cstr(o.key), cstr(o.value)
		switch k {
		case "queue_backend":
			if v != "" {
				cfg.backend = strings.ToLower(strings.TrimSpace(v))
			}
		case "queue_dsn":
			cfg.dsn = v
		case "queue_exchange":
			cfg.exchange = v
		case "queue_exchange_type":
			if v != "" {
				cfg.exchangeType = strings.ToLower(strings.TrimSpace(v))
			}
		case "queue_routing_key":
			cfg.routingKey = v
		case "queue_queue":
			cfg.queueName = v
		case "queue_timeout_ms":
			if dur, ok := parseTimeoutMS(v); ok {
				cfg.timeout = dur
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_timeout_ms=%q, keeping %dms", v, int(cfg.timeout/time.Millisecond))
			}
		case "queue_fail_mode":
			if mode, ok := parseFailMode(v); ok {
				cfg.failMode = mode
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_fail_mode=%q, keeping %s", v, failModeString(cfg.failMode))
			}
		case "queue_debug":
			if parsed, ok := parseBoolOption(v); ok {
				cfg.debug = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid queue_debug=%q, keeping %t", v, cfg.debug)
			}
		case "payload_encoding":
			if strings.TrimSpace(strings.ToLower(v)) != "base64" {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: payload_encoding=%q not supported, forcing base64", v)
			}
		case "include_topics":
			cfg.includeTopics = parseList(v)
		case "exclude_topics":
			cfg.excludeTopics = parseList(v)
			seenExcludeTopics = true
		case "include_users":
			cfg.includeUsers = parseSet(v)
		case "exclude_users":
			cfg.excludeUsers = parseSet(v)
		case "include_clients":
			cfg.includeClients = parseSet(v)
		case "exclude_clients":
			cfg.excludeClients = parseSet(v)
		case "include_retained":
			if parsed, ok := parseBoolOption(v); ok {
				cfg.includeRetained = parsed
			} else {
				mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: invalid include_retained=%q, keeping %t", v, cfg.includeRetained)
			}
		}
	}

	if !seenExcludeTopics {
		cfg.excludeTopics = []string{"$SYS/#"}
	}

	if cfg.backend != "rabbitmq" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: unsupported backend %q (expected rabbitmq)", cfg.backend)
		return C.MOSQ_ERR_INVAL
	}
	if cfg.exchangeType != "direct" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: exchange_type must be direct")
		return C.MOSQ_ERR_INVAL
	}
	if cfg.dsn == "" || cfg.exchange == "" {
		mosqLog(C.MOSQ_LOG_ERR, "queue-plugin: queue_dsn and queue_exchange must be set")
		return C.MOSQ_ERR_INVAL
	}

	mosqLog(C.MOSQ_LOG_INFO,
		"queue-plugin: init backend=%s dsn=%s exchange=%s exchange_type=%s routing_key=%s queue=%s timeout_ms=%d fail_mode=%s",
		cfg.backend, safeDSN(cfg.dsn), cfg.exchange, cfg.exchangeType, cfg.routingKey, cfg.queueName, int(cfg.timeout/time.Millisecond), failModeString(cfg.failMode),
	)

	publisher.mu.Lock()
	if err := publisher.ensureLocked(); err != nil {
		mosqLog(C.MOSQ_LOG_WARNING, "queue-plugin: initial connect failed: %v", err)
		publisher.closeLocked()
	}
	publisher.mu.Unlock()

	if rc := C.register_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c)); rc != C.MOSQ_ERR_SUCCESS {
		return rc
	}

	mosqLog(C.MOSQ_LOG_INFO, "queue-plugin: plugin initialized")
	return C.MOSQ_ERR_SUCCESS
}

//export go_mosq_plugin_cleanup
func go_mosq_plugin_cleanup(userdata unsafe.Pointer, opts *C.struct_mosquitto_opt, optCount C.int) C.int {
	C.unregister_event_callback(pid, C.MOSQ_EVT_MESSAGE, C.mosq_event_cb(C.message_cb_c))
	publisher.mu.Lock()
	publisher.closeLocked()
	publisher.mu.Unlock()
	mosqLog(C.MOSQ_LOG_INFO, "queue-plugin: plugin cleaned up")
	return C.MOSQ_ERR_SUCCESS
}

//export message_cb_c
func message_cb_c(event C.int, event_data unsafe.Pointer, userdata unsafe.Pointer) C.int {
	ed := (*C.struct_mosquitto_evt_message)(event_data)
	if ed == nil {
		return C.MOSQ_ERR_SUCCESS
	}

	topic := cstr(ed.topic)
	var clientID string
	var username string
	var peer string
	var protocol string
	if ed.client != nil {
		clientID = cstr(C.mosquitto_client_id(ed.client))
		username = cstr(C.mosquitto_client_username(ed.client))
		peer = cstr(C.mosquitto_client_address(ed.client))
		protocol = protocolString(int(C.mosquitto_client_protocol_version(ed.client)))
	}

	allow, reason := allowMessage(topic, username, clientID, bool(ed.retain))
	if !allow {
		debugLog("queue-plugin: filtered topic=%q client_id=%q username=%q reason=%s", topic, clientID, username, reason)
		return C.MOSQ_ERR_SUCCESS
	}

	const maxPayloadLen = int(^uint32(0) >> 1)
	payloadLen := int(ed.payloadlen)
	if ed.payloadlen > C.uint32_t(maxPayloadLen) {
		return failResult(errors.New("payload too large"))
	}
	var payload []byte
	if payloadLen > 0 {
		if ed.payload == nil {
			return failResult(errors.New("payload is nil"))
		}
		payload = C.GoBytes(ed.payload, C.int(payloadLen))
	}

	msg := queueMessage{
		TS:         time.Now().UTC().Format(time.RFC3339),
		Topic:      topic,
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
		QoS:        uint8(ed.qos),
		Retain:     bool(ed.retain),
		ClientID:   clientID,
		Username:   username,
		Peer:       peer,
		Protocol:   protocol,
	}
	msg.UserProperties = extractUserProperties(ed.properties)

	debugLog("queue-plugin: publish topic=%q qos=%d retain=%t len=%d client_id=%q username=%q user_props=%d",
		topic, ed.qos, bool(ed.retain), payloadLen, clientID, username, len(msg.UserProperties))

	body, err := json.Marshal(msg)
	if err != nil {
		return failResult(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	return failResult(publisher.Publish(ctx, body))
}

func main() {}
