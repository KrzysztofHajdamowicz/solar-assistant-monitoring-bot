package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	SchemaVersion int               `json:"schema_version"`
	IncidentID    string            `json:"incident_id,omitempty"`
	EventType     string            `json:"event_type"`
	Timestamp     time.Time         `json:"timestamp"`
	Severity      int               `json:"severity"`
	Source        string            `json:"source"`
	Message       string            `json:"message"`
	Duration      int64             `json:"duration_seconds,omitempty"`
	BatteryAges   map[int]int64     `json:"battery_age_seconds,omitempty"`
	Statuses      map[string]string `json:"statuses,omitempty"`
	Details       map[string]any    `json:"details,omitempty"`
}

type Incident struct {
	ID                 string    `json:"id"`
	StartedAt          time.Time `json:"started_at"`
	Severity           int       `json:"severity"`
	ReconnectAttempted bool      `json:"reconnect_attempted"`
	RebootAttempted    bool      `json:"reboot_attempted"`
	Resolved           bool      `json:"resolved"`
	Outcome            string    `json:"outcome,omitempty"`
	RearmSince         time.Time `json:"rearm_since,omitempty"`
	DiagnosticSnapshot []Metric  `json:"diagnostic_snapshot,omitempty"`
	DiagnosticError    string    `json:"diagnostic_error,omitempty"`
}

type PersistentState struct {
	ActiveIncident *Incident `json:"active_incident,omitempty"`
	PendingEvents  []Event   `json:"pending_events,omitempty"`
	LastEvent      *Event    `json:"last_event,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	state PersistentState
}

func OpenStore(path string) (*Store, error) {
	store := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return store, nil
}

func (s *Store) Snapshot() PersistentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

func (s *Store) Update(update func(*PersistentState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.state)
	s.state.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *Store) QueueEvent(event Event) error {
	return s.Update(func(state *PersistentState) {
		state.PendingEvents = append(state.PendingEvents, event)
		state.LastEvent = &event
	})
}

func (s *Store) PendingEvents() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.state.PendingEvents...)
}

func (s *Store) DropPending(count int) error {
	if count <= 0 {
		return nil
	}
	return s.Update(func(state *PersistentState) {
		if count >= len(state.PendingEvents) {
			state.PendingEvents = nil
			return
		}
		state.PendingEvents = append([]Event(nil), state.PendingEvents[count:]...)
	})
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func NewIncident(now time.Time) *Incident {
	var random [6]byte
	_, _ = rand.Read(random[:])
	return &Incident{
		ID:        now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:]),
		StartedAt: now.UTC(),
		Severity:  1,
	}
}

func cloneState(state PersistentState) PersistentState {
	data, _ := json.Marshal(state)
	var cloned PersistentState
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
