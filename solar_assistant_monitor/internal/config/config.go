package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultOptionsPath = "/data/options.json"

type rawConfig struct {
	SolarAssistantURL               string `json:"solar_assistant_url"`
	SolarAssistantUsername          string `json:"solar_assistant_username"`
	SolarAssistantPassword          string `json:"solar_assistant_password"`
	BatteryCount                    int    `json:"battery_count"`
	FreezeSeconds                   int    `json:"freeze_seconds"`
	WitnessSeconds                  int    `json:"witness_seconds"`
	RearmSeconds                    int    `json:"rearm_seconds"`
	AutomaticRecovery               bool   `json:"automatic_recovery"`
	DisconnectWaitSeconds           int    `json:"disconnect_wait_seconds"`
	UIConnectTimeoutSeconds         int    `json:"ui_connect_timeout_seconds"`
	TelemetryRecoveryTimeoutSeconds int    `json:"telemetry_recovery_timeout_seconds"`
	RebootTimeoutSeconds            int    `json:"reboot_timeout_seconds"`
	SSHHost                         string `json:"ssh_host"`
	SSHUser                         string `json:"ssh_user"`
	SSHHostKeySHA256                string `json:"ssh_host_key_sha256"`
	MQTTTopicPrefix                 string `json:"mqtt_topic_prefix"`
}

type Config struct {
	SolarAssistantURL *url.URL
	Username          string
	Password          string
	BatteryCount      int
	FreezeDuration    time.Duration
	WitnessDuration   time.Duration
	RearmDuration     time.Duration
	AutomaticRecovery bool
	DisconnectWait    time.Duration
	UIConnectTimeout  time.Duration
	TelemetryRecovery time.Duration
	RebootTimeout     time.Duration
	SSHHost           string
	SSHUser           string
	SSHHostKeySHA256  string
	MQTTTopicPrefix   string
	StatePath         string
	ChromiumPath      string
	MQTTHost          string
	MQTTPort          string
	MQTTUsername      string
	MQTTPassword      string
	MQTTTLS           bool
}

func Load() (Config, error) {
	path := getenv("OPTIONS_PATH", defaultOptionsPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read options %s: %w", path, err)
	}

	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("decode options: %w", err)
	}
	if raw.SolarAssistantURL == "" || raw.SolarAssistantPassword == "" {
		return Config{}, errors.New("solar_assistant_url and solar_assistant_password are required")
	}
	parsedURL, err := url.Parse(raw.SolarAssistantURL)
	if err != nil || parsedURL.Hostname() == "" {
		return Config{}, fmt.Errorf("invalid solar_assistant_url %q", raw.SolarAssistantURL)
	}
	if raw.BatteryCount < 1 {
		return Config{}, errors.New("battery_count must be at least 1")
	}
	if raw.SSHHost == "" || raw.SSHUser == "" || raw.SSHHostKeySHA256 == "" {
		return Config{}, errors.New("ssh_host, ssh_user and ssh_host_key_sha256 are required")
	}
	if !strings.HasPrefix(raw.SSHHostKeySHA256, "SHA256:") {
		return Config{}, errors.New("ssh_host_key_sha256 must start with SHA256:")
	}

	return Config{
		SolarAssistantURL: parsedURL,
		Username:          defaultString(raw.SolarAssistantUsername, "admin"),
		Password:          raw.SolarAssistantPassword,
		BatteryCount:      raw.BatteryCount,
		FreezeDuration:    seconds(raw.FreezeSeconds, 180),
		WitnessDuration:   seconds(raw.WitnessSeconds, 30),
		RearmDuration:     seconds(raw.RearmSeconds, 300),
		AutomaticRecovery: raw.AutomaticRecovery,
		DisconnectWait:    seconds(raw.DisconnectWaitSeconds, 5),
		UIConnectTimeout:  seconds(raw.UIConnectTimeoutSeconds, 90),
		TelemetryRecovery: seconds(raw.TelemetryRecoveryTimeoutSeconds, 180),
		RebootTimeout:     seconds(raw.RebootTimeoutSeconds, 480),
		SSHHost:           raw.SSHHost,
		SSHUser:           raw.SSHUser,
		SSHHostKeySHA256:  raw.SSHHostKeySHA256,
		MQTTTopicPrefix:   defaultString(raw.MQTTTopicPrefix, "solar_assistant_monitor"),
		StatePath:         getenv("STATE_PATH", "/data/state.json"),
		ChromiumPath:      getenv("CHROMIUM_PATH", "/usr/bin/chromium-browser"),
		MQTTHost:          os.Getenv("MQTT_HOST"),
		MQTTPort:          defaultString(os.Getenv("MQTT_PORT"), "1883"),
		MQTTUsername:      os.Getenv("MQTT_USERNAME"),
		MQTTPassword:      os.Getenv("MQTT_PASSWORD"),
		MQTTTLS:           strings.EqualFold(os.Getenv("MQTT_SSL"), "true"),
	}, nil
}

func seconds(value, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
