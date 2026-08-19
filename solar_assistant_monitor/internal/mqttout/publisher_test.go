package mqttout

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
)

func TestUnavailableMQTTStillQueuesEvents(t *testing.T) {
	store, err := monitor.OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	publisher := New("", "1883", "", "", false, "solar_assistant_monitor", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	publisher.Start()
	defer publisher.Close()

	event := monitor.Event{SchemaVersion: 1, EventType: "monitor_error", Timestamp: time.Now().UTC(), Severity: 1}
	if err := publisher.Emit(event); err != nil {
		t.Fatal(err)
	}
	pending := store.PendingEvents()
	if len(pending) != 1 || pending[0].EventType != event.EventType {
		t.Fatalf("event was not queued while MQTT was unavailable: %+v", pending)
	}
}
