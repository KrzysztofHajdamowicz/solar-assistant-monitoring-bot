package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/config"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/mqttout"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/recovery"
	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/solar"
)

type App struct {
	config       config.Config
	logger       *slog.Logger
	tracker      *monitor.Tracker
	store        *monitor.Store
	publisher    *mqttout.Publisher
	websocket    *solar.WebSocketClient
	rest         diagnosticClient
	ui           reconnectUI
	ssh          rebooter
	startedAt    time.Time
	confirmDelay time.Duration
	mu           sync.Mutex
	recovering   bool
	transition   string
	lastAction   string
	health       string
}

type diagnosticClient interface {
	Metrics(context.Context) ([]monitor.Metric, error)
}

type reconnectUI interface {
	Reconnect(context.Context, time.Duration) (recovery.UIStatuses, error)
	CheckConnected(context.Context, time.Duration) (recovery.UIStatuses, error)
}

type rebooter interface {
	Reboot(context.Context) error
}

type StateMessage struct {
	Timestamp  time.Time          `json:"timestamp"`
	Health     string             `json:"health"`
	Severity   int                `json:"severity"`
	LastAction string             `json:"last_action"`
	Evaluation monitor.Evaluation `json:"evaluation"`
	Tracker    monitor.Snapshot   `json:"tracker"`
	Incident   *monitor.Incident  `json:"incident,omitempty"`
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	store, err := monitor.OpenStore(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	tracker := monitor.NewTracker(cfg.BatteryCount)
	publisher := mqttout.New(cfg.MQTTHost, cfg.MQTTPort, cfg.MQTTUsername, cfg.MQTTPassword, cfg.MQTTTLS, cfg.MQTTTopicPrefix, store, logger)
	application := &App{
		config:       cfg,
		logger:       logger,
		tracker:      tracker,
		store:        store,
		publisher:    publisher,
		rest:         solar.NewRESTClient(cfg.SolarAssistantURL, cfg.Username, cfg.Password),
		ui:           recovery.NewUIClient(cfg.SolarAssistantURL, cfg.Username, cfg.Password, cfg.ChromiumPath, cfg.DisconnectWait, logger),
		ssh:          recovery.NewSSHRebooter(cfg.SSHHost, cfg.SSHUser, cfg.Password, cfg.SSHHostKeySHA256),
		startedAt:    time.Now().UTC(),
		confirmDelay: 10 * time.Second,
		health:       "starting",
		lastAction:   "none",
	}
	application.websocket = solar.NewWebSocketClient(cfg.SolarAssistantURL, cfg.Password, cfg.BatteryCount, tracker, logger, nil)
	return application, nil
}

func (a *App) Run(ctx context.Context) error {
	a.publisher.Start()
	defer a.publisher.Close()

	websocketErrors := make(chan error, 1)
	go func() { websocketErrors <- a.websocket.Run(ctx) }()

	evaluationTicker := time.NewTicker(time.Second)
	stateTicker := time.NewTicker(30 * time.Second)
	defer evaluationTicker.Stop()
	defer stateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-websocketErrors:
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("WebSocket monitor stopped: %w", err)
			}
			return err
		case now := <-evaluationTicker.C:
			a.evaluate(ctx, now.UTC())
		case now := <-stateTicker.C:
			a.publishState(now.UTC())
		}
	}
}

func (a *App) evaluate(ctx context.Context, now time.Time) {
	evaluation := a.tracker.Evaluate(now, a.config.FreezeDuration, a.config.WitnessDuration)
	persistent := a.store.Snapshot()

	if persistent.ActiveIncident != nil {
		a.handleRearm(now, evaluation, persistent.ActiveIncident)
	}

	if !evaluation.TransportHealthy {
		a.setHealth("monitor_error")
		if now.Sub(a.startedAt) >= time.Minute {
			a.emitTransition("transport_error", monitor.Event{
				SchemaVersion: 1, EventType: "monitor_error", Timestamp: now, Severity: 1, Source: "websocket",
				Message:     "Solar Assistant WebSocket is disconnected or the full metric stream is stale",
				BatteryAges: evaluation.BatteryAges,
			})
		}
		return
	}
	if !evaluation.WitnessActive {
		a.setHealth("monitor_error")
		if evaluation.AllInitialized && now.Sub(a.startedAt) >= a.config.FreezeDuration {
			a.emitTransition("witness_error", monitor.Event{
				SchemaVersion: 1, EventType: "monitor_error", Timestamp: now, Severity: 1, Source: "websocket",
				Message:     "LOAD and GRID stopped changing; battery-only failure cannot be confirmed",
				BatteryAges: evaluation.BatteryAges,
			})
		}
		return
	}

	if len(evaluation.StaleBatteries) > 0 && !evaluation.AllStale {
		a.setHealth("degraded")
		a.emitTransition("degraded:"+intsKey(evaluation.StaleBatteries), monitor.Event{
			SchemaVersion: 1, EventType: "battery_degraded", Timestamp: now, Severity: 1, Source: "websocket",
			Message:     fmt.Sprintf("Battery telemetry is stale for packs %v; automatic recovery is suppressed", evaluation.StaleBatteries),
			BatteryAges: evaluation.BatteryAges,
		})
		return
	}

	if evaluation.AllInitialized && evaluation.AllStale {
		a.setHealth("failed")
		if persistent.ActiveIncident != nil && !persistent.ActiveIncident.Resolved && persistent.ActiveIncident.Outcome != "unresolved" && a.startRecovery() {
			go a.recover(ctx, persistent.ActiveIncident.ID)
			return
		}
		if persistent.ActiveIncident == nil && a.startRecovery() {
			incident := monitor.NewIncident(now)
			if err := a.store.Update(func(state *monitor.PersistentState) { state.ActiveIncident = incident }); err != nil {
				a.logger.Error("persist new incident", "error", err)
				a.finishRecovery()
				return
			}
			a.emit(monitor.Event{
				SchemaVersion: 1, IncidentID: incident.ID, EventType: "incident_detected", Timestamp: now,
				Severity: 1, Source: "websocket", Message: "All battery telemetry is frozen while LOAD or GRID remains active",
				BatteryAges: evaluation.BatteryAges,
			})
			go a.recover(ctx, incident.ID)
		}
		return
	}

	a.setHealth("healthy")
	a.clearTransition()
}

func (a *App) recover(ctx context.Context, incidentID string) {
	defer a.finishRecovery()

	// A final independent sample window prevents acting on a boundary race.
	if a.confirmDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.confirmDelay):
		}
	}
	evaluation := a.tracker.Evaluate(time.Now().UTC(), a.config.FreezeDuration, a.config.WitnessDuration)
	if !evaluation.AllStale || !evaluation.WitnessActive || !evaluation.TransportHealthy {
		a.resolveIncident(incidentID, 1, "recovered_before_action", "none", nil)
		return
	}
	a.captureDiagnostic(ctx, incidentID)
	if !a.config.AutomaticRecovery {
		a.resolveIncident(incidentID, 1, "automatic_recovery_disabled", "none", nil)
		return
	}

	persistent := a.store.Snapshot()
	if persistent.ActiveIncident == nil || persistent.ActiveIncident.ID != incidentID {
		return
	}
	if !persistent.ActiveIncident.ReconnectAttempted {
		baseline := a.tracker.Versions()
		if !a.markAttempt(incidentID, "reconnect") {
			return
		}
		a.emit(monitor.Event{
			SchemaVersion: 1, IncidentID: incidentID, EventType: "reconnect_started", Timestamp: time.Now().UTC(),
			Severity: 2, Source: "ui", Message: "Starting Solar Assistant Disconnect/Connect recovery",
		})
		statuses, reconnectErr := a.ui.Reconnect(ctx, a.config.UIConnectTimeout)
		if reconnectErr == nil {
			reconnectErr = a.waitForTelemetry(ctx, baseline, a.config.TelemetryRecovery)
		}
		if reconnectErr == nil {
			a.resolveIncident(incidentID, 2, "recovered_after_reconnect", "reconnect", statusesMap(statuses))
			return
		}
		a.logger.Warn("Disconnect/Connect did not recover telemetry", "error", reconnectErr)
		a.emit(monitor.Event{
			SchemaVersion: 1, IncidentID: incidentID, EventType: "reconnect_failed", Timestamp: time.Now().UTC(),
			Severity: 2, Source: "ui", Message: reconnectErr.Error(), Statuses: statusesMap(statuses),
		})
	}

	baseline := a.tracker.Versions()
	connectionGeneration := a.tracker.Snapshot().ConnectionGeneration
	if !a.markAttempt(incidentID, "reboot") {
		persistent = a.store.Snapshot()
		if persistent.ActiveIncident != nil && persistent.ActiveIncident.ID == incidentID && persistent.ActiveIncident.Outcome == "" {
			a.unresolved(incidentID, errors.New("recovery was interrupted after the reboot attempt; refusing to reboot twice"))
		}
		return
	}
	a.emit(monitor.Event{
		SchemaVersion: 1, IncidentID: incidentID, EventType: "reboot_started", Timestamp: time.Now().UTC(),
		Severity: 3, Source: "ssh", Message: "Disconnect/Connect failed; starting one bounded Solar Assistant reboot",
	})
	if err := a.ssh.Reboot(ctx); err != nil {
		a.unresolved(incidentID, fmt.Errorf("SSH reboot failed: %w", err))
		return
	}
	if err := a.waitForWebSocketReconnect(ctx, connectionGeneration, a.config.RebootTimeout); err != nil {
		a.unresolved(incidentID, err)
		return
	}
	statuses, statusErr := a.ui.CheckConnected(ctx, a.config.UIConnectTimeout)
	if statusErr != nil {
		a.unresolved(incidentID, statusErr)
		return
	}
	if err := a.waitForTelemetry(ctx, baseline, a.config.TelemetryRecovery); err != nil {
		a.unresolved(incidentID, err)
		return
	}
	a.resolveIncident(incidentID, 3, "recovered_after_reboot", "reboot", statusesMap(statuses))
}

func (a *App) captureDiagnostic(ctx context.Context, incidentID string) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	metrics, err := a.rest.Metrics(probeCtx)
	if updateErr := a.store.Update(func(state *monitor.PersistentState) {
		if state.ActiveIncident == nil || state.ActiveIncident.ID != incidentID {
			return
		}
		if err != nil {
			state.ActiveIncident.DiagnosticError = err.Error()
			return
		}
		state.ActiveIncident.DiagnosticSnapshot = metrics
	}); updateErr != nil {
		a.logger.Error("persist diagnostic REST snapshot", "error", updateErr)
	}
}

func (a *App) waitForWebSocketReconnect(ctx context.Context, generation uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snapshot := a.tracker.Snapshot()
		if snapshot.Connected && snapshot.ConnectionGeneration > generation {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("Solar Assistant WebSocket did not reconnect after reboot")
}

func (a *App) waitForTelemetry(ctx context.Context, baseline []uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.tracker.Snapshot().Connected && a.tracker.AllVersionsAdvanced(baseline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("not every battery produced changed telemetry before timeout")
}

func (a *App) markAttempt(incidentID, action string) bool {
	marked := false
	err := a.store.Update(func(state *monitor.PersistentState) {
		incident := state.ActiveIncident
		if incident == nil || incident.ID != incidentID || incident.Resolved {
			return
		}
		switch action {
		case "reconnect":
			if incident.ReconnectAttempted {
				return
			}
			incident.ReconnectAttempted = true
			incident.Severity = 2
		case "reboot":
			if incident.RebootAttempted {
				return
			}
			incident.RebootAttempted = true
			incident.Severity = 3
		}
		marked = true
	})
	if err != nil {
		a.logger.Error("persist recovery attempt", "action", action, "error", err)
		return false
	}
	if marked {
		a.mu.Lock()
		a.lastAction = action
		a.mu.Unlock()
	}
	return marked
}

func (a *App) resolveIncident(incidentID string, severity int, outcome, action string, statuses map[string]string) {
	now := time.Now().UTC()
	var started time.Time
	if err := a.store.Update(func(state *monitor.PersistentState) {
		if state.ActiveIncident == nil || state.ActiveIncident.ID != incidentID {
			return
		}
		started = state.ActiveIncident.StartedAt
		state.ActiveIncident.Severity = severity
		state.ActiveIncident.Resolved = true
		state.ActiveIncident.Outcome = outcome
		state.ActiveIncident.RearmSince = now
	}); err != nil {
		a.logger.Error("persist resolved incident", "error", err)
	}
	a.mu.Lock()
	a.lastAction = action
	a.health = "recovering"
	a.mu.Unlock()
	a.emit(monitor.Event{
		SchemaVersion: 1, IncidentID: incidentID, EventType: "recovered", Timestamp: now,
		Severity: severity, Source: action, Message: outcome, Duration: int64(now.Sub(started) / time.Second), Statuses: statuses,
	})
}

func (a *App) unresolved(incidentID string, cause error) {
	now := time.Now().UTC()
	var started time.Time
	if err := a.store.Update(func(state *monitor.PersistentState) {
		if state.ActiveIncident == nil || state.ActiveIncident.ID != incidentID {
			return
		}
		started = state.ActiveIncident.StartedAt
		state.ActiveIncident.Severity = 4
		state.ActiveIncident.Outcome = "unresolved"
	}); err != nil {
		a.logger.Error("persist unresolved incident", "error", err)
	}
	a.setHealth("failed")
	a.emit(monitor.Event{
		SchemaVersion: 1, IncidentID: incidentID, EventType: "incident_unresolved", Timestamp: now,
		Severity: 4, Source: "recovery", Message: cause.Error(), Duration: int64(now.Sub(started) / time.Second),
	})
}

func (a *App) handleRearm(now time.Time, evaluation monitor.Evaluation, incident *monitor.Incident) {
	a.mu.Lock()
	recovering := a.recovering
	a.mu.Unlock()
	if recovering {
		return
	}
	healthy := evaluation.TransportHealthy && evaluation.WitnessActive && evaluation.AllInitialized && len(evaluation.StaleBatteries) == 0
	if !healthy {
		if !incident.RearmSince.IsZero() {
			_ = a.store.Update(func(state *monitor.PersistentState) {
				if state.ActiveIncident != nil && state.ActiveIncident.ID == incident.ID {
					state.ActiveIncident.RearmSince = time.Time{}
				}
			})
		}
		return
	}
	if !incident.Resolved {
		a.resolveIncident(incident.ID, incident.Severity, "telemetry_recovered_without_further_action", "none", nil)
		return
	}
	if incident.RearmSince.IsZero() {
		_ = a.store.Update(func(state *monitor.PersistentState) {
			if state.ActiveIncident != nil && state.ActiveIncident.ID == incident.ID {
				state.ActiveIncident.RearmSince = now
			}
		})
		return
	}
	if now.Sub(incident.RearmSince) < a.config.RearmDuration {
		return
	}
	_ = a.store.Update(func(state *monitor.PersistentState) {
		if state.ActiveIncident != nil && state.ActiveIncident.ID == incident.ID {
			state.ActiveIncident = nil
		}
	})
	a.emit(monitor.Event{
		SchemaVersion: 1, IncidentID: incident.ID, EventType: "monitor_rearmed", Timestamp: now,
		Severity: incident.Severity, Source: "websocket", Message: "Healthy telemetry remained stable for the configured rearm period",
	})
}

func (a *App) startRecovery() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recovering {
		return false
	}
	a.recovering = true
	return true
}

func (a *App) finishRecovery() {
	a.mu.Lock()
	a.recovering = false
	a.mu.Unlock()
}

func (a *App) setHealth(health string) {
	a.mu.Lock()
	a.health = health
	a.mu.Unlock()
}

func (a *App) emitTransition(key string, event monitor.Event) {
	a.mu.Lock()
	if a.transition == key {
		a.mu.Unlock()
		return
	}
	a.transition = key
	a.mu.Unlock()
	a.emit(event)
}

func (a *App) clearTransition() {
	a.mu.Lock()
	a.transition = ""
	a.mu.Unlock()
}

func (a *App) emit(event monitor.Event) {
	a.logger.Info("monitor event", "type", event.EventType, "severity", event.Severity, "incident_id", event.IncidentID, "message", event.Message)
	if err := a.publisher.Emit(event); err != nil {
		a.logger.Error("persist monitor event", "error", err)
	}
	a.publishState(time.Now().UTC())
}

func (a *App) publishState(now time.Time) {
	evaluation := a.tracker.Evaluate(now, a.config.FreezeDuration, a.config.WitnessDuration)
	persistent := a.store.Snapshot()
	a.mu.Lock()
	health := a.health
	lastAction := a.lastAction
	a.mu.Unlock()
	severity := 0
	if persistent.ActiveIncident != nil {
		severity = persistent.ActiveIncident.Severity
	}
	a.publisher.PublishState(StateMessage{
		Timestamp: now, Health: health, Severity: severity, LastAction: lastAction,
		Evaluation: evaluation, Tracker: a.tracker.Snapshot(), Incident: persistent.ActiveIncident,
	})
}

func statusesMap(statuses recovery.UIStatuses) map[string]string {
	return map[string]string{"inverter": statuses.Inverter, "battery": statuses.Battery}
}

func intsKey(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprintf("%d", value)
	}
	return strings.Join(parts, ",")
}
