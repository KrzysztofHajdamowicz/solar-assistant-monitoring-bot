package monitor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAttemptFlagsAndEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident := NewIncident(time.Now().UTC())
	incident.ReconnectAttempted = true
	if err := store.Update(func(state *PersistentState) { state.ActiveIncident = incident }); err != nil {
		t.Fatal(err)
	}
	if err := store.QueueEvent(Event{SchemaVersion: 1, EventType: "test", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reopened.Snapshot()
	if snapshot.ActiveIncident == nil || !snapshot.ActiveIncident.ReconnectAttempted {
		t.Fatalf("attempt flag was not persisted: %+v", snapshot.ActiveIncident)
	}
	if len(snapshot.PendingEvents) != 1 {
		t.Fatalf("pending event was not persisted: %+v", snapshot.PendingEvents)
	}
}
