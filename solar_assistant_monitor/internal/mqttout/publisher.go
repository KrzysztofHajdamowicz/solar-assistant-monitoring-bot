package mqttout

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
)

type Publisher struct {
	client mqtt.Client
	prefix string
	store  *monitor.Store
	logger *slog.Logger
	mu     sync.Mutex
}

func New(host, port, username, password string, useTLS bool, prefix string, store *monitor.Store, logger *slog.Logger) *Publisher {
	publisher := &Publisher{prefix: strings.TrimRight(prefix, "/"), store: store, logger: logger}
	if host == "" {
		return publisher
	}
	scheme := "tcp"
	if useTLS {
		scheme = "ssl"
	}
	broker := fmt.Sprintf("%s://%s:%s", scheme, host, port)
	options := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(fmt.Sprintf("solar-assistant-monitor-%d", time.Now().UnixNano())).
		SetUsername(username).
		SetPassword(password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetKeepAlive(30*time.Second).
		SetPingTimeout(10*time.Second).
		SetOrderMatters(true).
		SetWill(strings.TrimRight(prefix, "/")+"/availability", "offline", 1, true)
	if useTLS {
		options.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	options.OnConnect = func(_ mqtt.Client) {
		publisher.logger.Info("connected to Home Assistant MQTT service")
		publisher.publishRaw(publisher.prefix+"/availability", []byte("online"), true)
		publisher.publishDiscovery()
		go publisher.Flush()
	}
	options.OnConnectionLost = func(_ mqtt.Client, err error) {
		publisher.logger.Warn("MQTT output disconnected; events remain queued", "error", err)
	}
	publisher.client = mqtt.NewClient(options)
	return publisher
}

func (p *Publisher) Start() {
	if p == nil || p.client == nil {
		return
	}
	go func() {
		token := p.client.Connect()
		if token.Wait() && token.Error() != nil {
			p.logger.Warn("initial MQTT connection failed; retrying in background", "error", token.Error())
		}
	}()
}

func (p *Publisher) Close() {
	if p == nil || p.client == nil {
		return
	}
	if p.client.IsConnected() {
		p.publishRaw(p.prefix+"/availability", []byte("offline"), true)
		p.client.Disconnect(500)
	}
}

func (p *Publisher) Emit(event monitor.Event) error {
	if err := p.store.QueueEvent(event); err != nil {
		return err
	}
	if p != nil && p.client != nil && p.client.IsConnected() {
		go p.Flush()
	}
	return nil
}

func (p *Publisher) PublishState(state any) {
	if p == nil || p.client == nil || !p.client.IsConnected() {
		return
	}
	payload, err := json.Marshal(state)
	if err != nil {
		p.logger.Error("encode MQTT state", "error", err)
		return
	}
	p.publishRaw(p.prefix+"/state", payload, true)
}

func (p *Publisher) Flush() {
	if p == nil || p.client == nil || !p.client.IsConnected() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := p.store.PendingEvents()
	published := 0
	for _, event := range pending {
		payload, err := json.Marshal(event)
		if err != nil {
			p.logger.Error("encode queued MQTT event", "error", err)
			break
		}
		token := p.client.Publish(p.prefix+"/event", 1, false, payload)
		if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
			p.logger.Warn("publish queued MQTT event failed", "error", token.Error())
			break
		}
		published++
	}
	if published > 0 {
		if err := p.store.DropPending(published); err != nil {
			p.logger.Error("remove published MQTT events from queue", "error", err)
		}
	}
}

func (p *Publisher) publishRaw(topic string, payload []byte, retained bool) {
	if p == nil || p.client == nil || !p.client.IsConnected() {
		return
	}
	token := p.client.Publish(topic, 1, retained, payload)
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		p.logger.Warn("MQTT publish failed", "topic", topic, "error", token.Error())
	}
}

func (p *Publisher) publishDiscovery() {
	device := map[string]any{
		"identifiers":  []string{"solar_assistant_monitor"},
		"name":         "Solar Assistant Monitor",
		"manufacturer": "Krzysztof Hajdamowicz",
		"model":        "WebSocket recovery monitor",
		"sw_version":   "0.1.0",
	}
	entities := []struct {
		component string
		objectID  string
		config    map[string]any
	}{
		{"binary_sensor", "communication_problem", map[string]any{
			"name": "Communication problem", "unique_id": "solar_assistant_monitor_problem",
			"device_class": "problem", "value_template": "{{ 'OFF' if value_json.health == 'healthy' else 'ON' }}",
			"payload_on": "ON", "payload_off": "OFF",
		}},
		{"sensor", "last_reaction_severity", map[string]any{
			"name": "Last reaction severity", "unique_id": "solar_assistant_monitor_severity",
			"value_template": "{{ value_json.severity | default(0) }}",
			"icon":           "mdi:alert-decagram-outline",
		}},
		{"sensor", "last_action", map[string]any{
			"name": "Last action", "unique_id": "solar_assistant_monitor_action",
			"value_template": "{{ value_json.last_action | default('none') }}",
			"icon":           "mdi:tools",
		}},
	}
	for _, entity := range entities {
		entity.config["state_topic"] = p.prefix + "/state"
		entity.config["availability_topic"] = p.prefix + "/availability"
		entity.config["payload_available"] = "online"
		entity.config["payload_not_available"] = "offline"
		entity.config["device"] = device
		payload, _ := json.Marshal(entity.config)
		topic := fmt.Sprintf("homeassistant/%s/solar_assistant_monitor/%s/config", entity.component, entity.objectID)
		p.publishRaw(topic, payload, true)
	}
}
