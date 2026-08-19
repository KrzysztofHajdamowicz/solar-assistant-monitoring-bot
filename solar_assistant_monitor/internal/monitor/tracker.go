package monitor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var dynamicBatteryMetric = regexp.MustCompile(`^(?:current|power|voltage|state_of_charge|temperature(?:_\d+)?|cell_voltage_(?:\d+|-_(?:average|highest|lowest|imbalance)))$`)

type Metric struct {
	Topic string          `json:"topic"`
	Value json.RawMessage `json:"value"`
}

type batteryState struct {
	values      map[string]string
	initialized bool
	lastChanged time.Time
	lastMessage time.Time
	version     uint64
}

type Tracker struct {
	mu                   sync.RWMutex
	batteries            []batteryState
	witnessValues        map[string]string
	connected            bool
	lastMessage          time.Time
	lastWitnessMessage   time.Time
	lastWitnessChange    time.Time
	connectionGeneration uint64
}

type BatterySnapshot struct {
	Index       int       `json:"index"`
	Initialized bool      `json:"initialized"`
	LastChanged time.Time `json:"last_changed,omitempty"`
	LastMessage time.Time `json:"last_message,omitempty"`
	Version     uint64    `json:"version"`
}

type Snapshot struct {
	Connected            bool              `json:"connected"`
	LastMessage          time.Time         `json:"last_message,omitempty"`
	LastWitnessMessage   time.Time         `json:"last_witness_message,omitempty"`
	LastWitnessChange    time.Time         `json:"last_witness_change,omitempty"`
	ConnectionGeneration uint64            `json:"connection_generation"`
	Batteries            []BatterySnapshot `json:"batteries"`
}

type Evaluation struct {
	TransportHealthy bool          `json:"transport_healthy"`
	WitnessActive    bool          `json:"witness_active"`
	AllInitialized   bool          `json:"all_initialized"`
	AllStale         bool          `json:"all_stale"`
	StaleBatteries   []int         `json:"stale_batteries"`
	BatteryAges      map[int]int64 `json:"battery_age_seconds"`
}

func NewTracker(batteryCount int) *Tracker {
	batteries := make([]batteryState, batteryCount)
	for i := range batteries {
		batteries[i].values = make(map[string]string)
	}
	return &Tracker{
		batteries:     batteries,
		witnessValues: make(map[string]string),
	}
}

func (t *Tracker) SetConnected(connected bool, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if connected && !t.connected {
		t.connectionGeneration++
	}
	t.connected = connected
	if connected {
		t.lastMessage = now
	}
}

func (t *Tracker) Apply(metrics []Metric, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(metrics) > 0 {
		t.lastMessage = now
	}
	for _, metric := range metrics {
		value := canonicalValue(metric.Value)
		if isWitnessTopic(metric.Topic) {
			t.lastWitnessMessage = now
			previous, exists := t.witnessValues[metric.Topic]
			if !exists || previous != value {
				t.witnessValues[metric.Topic] = value
				t.lastWitnessChange = now
			}
			continue
		}
		batteryIndex, name, ok := batteryMetric(metric.Topic)
		if !ok || batteryIndex < 0 || batteryIndex >= len(t.batteries) || !dynamicBatteryMetric.MatchString(name) {
			continue
		}
		battery := &t.batteries[batteryIndex]
		battery.lastMessage = now
		previous, exists := battery.values[name]
		if !exists || previous != value {
			battery.values[name] = value
			battery.lastChanged = now
			battery.version++
		}
		battery.initialized = true
	}
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshotLocked()
}

func (t *Tracker) Evaluate(now time.Time, freezeDuration, witnessDuration time.Duration) Evaluation {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := Evaluation{
		TransportHealthy: t.connected && !t.lastMessage.IsZero() && now.Sub(t.lastMessage) <= maxDuration(2*witnessDuration, time.Minute),
		WitnessActive:    !t.lastWitnessChange.IsZero() && now.Sub(t.lastWitnessChange) <= witnessDuration,
		AllInitialized:   true,
		AllStale:         true,
		BatteryAges:      make(map[int]int64, len(t.batteries)),
	}
	for i, battery := range t.batteries {
		index := i + 1
		if !battery.initialized || battery.lastChanged.IsZero() {
			result.AllInitialized = false
			result.AllStale = false
			result.BatteryAges[index] = -1
			continue
		}
		age := now.Sub(battery.lastChanged)
		result.BatteryAges[index] = int64(age / time.Second)
		if age >= freezeDuration {
			result.StaleBatteries = append(result.StaleBatteries, index)
		} else {
			result.AllStale = false
		}
	}
	if len(t.batteries) == 0 {
		result.AllInitialized = false
		result.AllStale = false
	}
	return result
}

func (t *Tracker) Versions() []uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	versions := make([]uint64, len(t.batteries))
	for i := range t.batteries {
		versions[i] = t.batteries[i].version
	}
	return versions
}

func (t *Tracker) AllVersionsAdvanced(baseline []uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(baseline) != len(t.batteries) {
		return false
	}
	for i, battery := range t.batteries {
		if battery.version <= baseline[i] {
			return false
		}
	}
	return true
}

func (t *Tracker) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Connected:            t.connected,
		LastMessage:          t.lastMessage,
		LastWitnessMessage:   t.lastWitnessMessage,
		LastWitnessChange:    t.lastWitnessChange,
		ConnectionGeneration: t.connectionGeneration,
		Batteries:            make([]BatterySnapshot, len(t.batteries)),
	}
	for i, battery := range t.batteries {
		snapshot.Batteries[i] = BatterySnapshot{
			Index:       i + 1,
			Initialized: battery.initialized,
			LastChanged: battery.lastChanged,
			LastMessage: battery.lastMessage,
			Version:     battery.version,
		}
	}
	return snapshot
}

func batteryMetric(topic string) (int, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "battery_") {
		return 0, "", false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(parts[0], "battery_"))
	if err != nil || index < 1 {
		return 0, "", false
	}
	return index - 1, parts[1], true
}

func isWitnessTopic(topic string) bool {
	return topic == "total/load_power" || topic == "total/grid_power"
}

func canonicalValue(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(normalized)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func SortedAges(ages map[int]int64) []string {
	keys := make([]int, 0, len(ages))
	for key := range ages {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, fmt.Sprintf("battery_%d=%ds", key, ages[key]))
	}
	return values
}
