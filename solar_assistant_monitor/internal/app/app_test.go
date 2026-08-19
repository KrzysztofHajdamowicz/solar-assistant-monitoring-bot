package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/config"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/mqttout"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/recovery"
)

func TestAttemptFlagsPreventDuplicateReconnectAndReboot(t *testing.T) {
	store, err := monitor.OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	incident := monitor.NewIncident(time.Now().UTC())
	if err := store.Update(func(state *monitor.PersistentState) { state.ActiveIncident = incident }); err != nil {
		t.Fatal(err)
	}
	application := &App{store: store}

	if !application.markAttempt(incident.ID, "reconnect") {
		t.Fatal("first reconnect attempt should be accepted")
	}
	if application.markAttempt(incident.ID, "reconnect") {
		t.Fatal("second reconnect attempt must be rejected")
	}
	if !application.markAttempt(incident.ID, "reboot") {
		t.Fatal("first reboot attempt should be accepted")
	}
	if application.markAttempt(incident.ID, "reboot") {
		t.Fatal("second reboot attempt must be rejected")
	}
	snapshot := store.Snapshot()
	if !snapshot.ActiveIncident.ReconnectAttempted || !snapshot.ActiveIncident.RebootAttempted {
		t.Fatalf("attempt flags not persisted: %+v", snapshot.ActiveIncident)
	}
}

func TestRecoveryStopsAfterSuccessfulReconnect(t *testing.T) {
	application, incident := recoveryTestApp(t)
	ui := &fakeUI{tracker: application.tracker, advanceOnReconnect: true}
	application.ui = ui
	application.ssh = &fakeRebooter{tracker: application.tracker}

	application.recover(context.Background(), incident.ID)

	resolved := application.store.Snapshot().ActiveIncident
	if resolved == nil || !resolved.Resolved || resolved.Outcome != "recovered_after_reconnect" {
		t.Fatalf("unexpected incident result: %+v", resolved)
	}
	if ui.reconnectCalls != 1 || application.ssh.(*fakeRebooter).calls != 0 {
		t.Fatalf("unexpected actions: reconnect=%d reboot=%d", ui.reconnectCalls, application.ssh.(*fakeRebooter).calls)
	}
	if len(resolved.DiagnosticSnapshot) != 1 {
		t.Fatalf("REST snapshot was not persisted: %+v", resolved)
	}
}

func TestRecoveryEscalatesOnceToReboot(t *testing.T) {
	application, incident := recoveryTestApp(t)
	ui := &fakeUI{tracker: application.tracker, reconnectErr: errors.New("mock reconnect failure"), advanceOnCheck: true}
	rebooter := &fakeRebooter{tracker: application.tracker}
	application.ui = ui
	application.ssh = rebooter

	application.recover(context.Background(), incident.ID)

	resolved := application.store.Snapshot().ActiveIncident
	if resolved == nil || !resolved.Resolved || resolved.Outcome != "recovered_after_reboot" {
		t.Fatalf("unexpected incident result: %+v", resolved)
	}
	if ui.reconnectCalls != 1 || ui.checkCalls != 1 || rebooter.calls != 1 {
		t.Fatalf("unexpected actions: reconnect=%d check=%d reboot=%d", ui.reconnectCalls, ui.checkCalls, rebooter.calls)
	}
	if !resolved.ReconnectAttempted || !resolved.RebootAttempted {
		t.Fatalf("attempt flags not persisted: %+v", resolved)
	}
}

func TestInterruptedIncidentDoesNotRepeatReboot(t *testing.T) {
	application, incident := recoveryTestApp(t)
	if err := application.store.Update(func(state *monitor.PersistentState) {
		state.ActiveIncident.ReconnectAttempted = true
		state.ActiveIncident.RebootAttempted = true
	}); err != nil {
		t.Fatal(err)
	}
	rebooter := &fakeRebooter{tracker: application.tracker}
	application.ui = &fakeUI{tracker: application.tracker}
	application.ssh = rebooter

	application.recover(context.Background(), incident.ID)

	result := application.store.Snapshot().ActiveIncident
	if rebooter.calls != 0 || result.Outcome != "unresolved" || result.Severity != 4 {
		t.Fatalf("interrupted incident repeated an action: reboot=%d incident=%+v", rebooter.calls, result)
	}
}

type fakeDiagnostic struct{}

func (fakeDiagnostic) Metrics(context.Context) ([]monitor.Metric, error) {
	return []monitor.Metric{testMetric("battery_1/current", 1)}, nil
}

type fakeUI struct {
	tracker            *monitor.Tracker
	reconnectErr       error
	advanceOnReconnect bool
	advanceOnCheck     bool
	reconnectCalls     int
	checkCalls         int
}

func (u *fakeUI) Reconnect(context.Context, time.Duration) (recovery.UIStatuses, error) {
	u.reconnectCalls++
	if u.advanceOnReconnect {
		advanceAllBatteries(u.tracker)
	}
	return recovery.UIStatuses{Inverter: "Connected", Battery: "Connected"}, u.reconnectErr
}

func (u *fakeUI) CheckConnected(context.Context, time.Duration) (recovery.UIStatuses, error) {
	u.checkCalls++
	if u.advanceOnCheck {
		advanceAllBatteries(u.tracker)
	}
	return recovery.UIStatuses{Inverter: "Connected", Battery: "Connected"}, nil
}

type fakeRebooter struct {
	tracker *monitor.Tracker
	calls   int
}

func (r *fakeRebooter) Reboot(context.Context) error {
	r.calls++
	r.tracker.SetConnected(false, time.Now().UTC())
	r.tracker.SetConnected(true, time.Now().UTC())
	return nil
}

func recoveryTestApp(t *testing.T) (*App, *monitor.Incident) {
	t.Helper()
	store, err := monitor.OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	incident := monitor.NewIncident(time.Now().UTC().Add(-181 * time.Second))
	if err := store.Update(func(state *monitor.PersistentState) { state.ActiveIncident = incident }); err != nil {
		t.Fatal(err)
	}
	tracker := monitor.NewTracker(3)
	old := time.Now().UTC().Add(-181 * time.Second)
	tracker.SetConnected(true, old)
	tracker.Apply([]monitor.Metric{
		testMetric("battery_1/current", 1), testMetric("battery_2/current", 2), testMetric("battery_3/current", 3),
		testMetric("total/load_power", 100),
	}, old)
	tracker.Apply([]monitor.Metric{testMetric("total/load_power", 101)}, time.Now().UTC())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parsedURL, _ := url.Parse("http://solar-assistant.test")
	application := &App{
		config: config.Config{
			SolarAssistantURL: parsedURL, AutomaticRecovery: true, FreezeDuration: 180 * time.Second,
			WitnessDuration: 30 * time.Second, UIConnectTimeout: time.Second,
			TelemetryRecovery: 100 * time.Millisecond, RebootTimeout: 100 * time.Millisecond,
		},
		logger: logger, tracker: tracker, store: store, rest: fakeDiagnostic{}, startedAt: old,
		confirmDelay: 0, health: "failed", lastAction: "none",
	}
	application.publisher = mqttout.New("", "", "", "", false, "solar_assistant_monitor", store, logger)
	return application, incident
}

func advanceAllBatteries(tracker *monitor.Tracker) {
	tracker.Apply([]monitor.Metric{
		testMetric("battery_1/current", 11), testMetric("battery_2/current", 12), testMetric("battery_3/current", 13),
	}, time.Now().UTC())
}

func testMetric(topic string, value any) monitor.Metric {
	raw, _ := json.Marshal(value)
	return monitor.Metric{Topic: topic, Value: raw}
}
