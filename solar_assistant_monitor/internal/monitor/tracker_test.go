package monitor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFreezeThresholdAndWitness(t *testing.T) {
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tracker := NewTracker(3)
	tracker.SetConnected(true, base)
	tracker.Apply([]Metric{
		metric("battery_1/current", 1.0),
		metric("battery_2/current", 2.0),
		metric("battery_3/current", 3.0),
		metric("total/load_power", 100),
	}, base)

	tracker.Apply([]Metric{metric("total/load_power", 101)}, base.Add(179*time.Second))
	before := tracker.Evaluate(base.Add(179*time.Second), 180*time.Second, 30*time.Second)
	if before.AllStale {
		t.Fatal("179 seconds must not be treated as a confirmed freeze")
	}

	atThreshold := tracker.Evaluate(base.Add(180*time.Second), 180*time.Second, 30*time.Second)
	if !atThreshold.AllStale || !atThreshold.WitnessActive || !atThreshold.TransportHealthy {
		t.Fatalf("expected confirmed freeze at threshold, got %+v", atThreshold)
	}
}

func TestPartialStallDoesNotConfirmFullFailure(t *testing.T) {
	base := time.Now().UTC()
	tracker := NewTracker(3)
	tracker.SetConnected(true, base)
	tracker.Apply([]Metric{
		metric("battery_1/current", 1), metric("battery_2/current", 2), metric("battery_3/current", 3),
		metric("total/grid_power", 10),
	}, base)
	tracker.Apply([]Metric{
		metric("battery_3/current", 4), metric("total/grid_power", 11),
	}, base.Add(179*time.Second))

	evaluation := tracker.Evaluate(base.Add(180*time.Second), 180*time.Second, 30*time.Second)
	if evaluation.AllStale {
		t.Fatal("one healthy battery must prevent automatic recovery")
	}
	if len(evaluation.StaleBatteries) != 2 {
		t.Fatalf("expected two stale batteries, got %v", evaluation.StaleBatteries)
	}
}

func TestRepeatedValuesDoNotResetChangeClock(t *testing.T) {
	base := time.Now().UTC()
	tracker := NewTracker(1)
	tracker.SetConnected(true, base)
	tracker.Apply([]Metric{metric("battery_1/current", 0.1), metric("total/load_power", 10)}, base)
	for second := 5; second <= 180; second += 5 {
		now := base.Add(time.Duration(second) * time.Second)
		tracker.Apply([]Metric{metric("battery_1/current", 0.1)}, now)
	}
	tracker.Apply([]Metric{metric("total/load_power", 11)}, base.Add(180*time.Second))

	evaluation := tracker.Evaluate(base.Add(180*time.Second), 180*time.Second, 30*time.Second)
	if !evaluation.AllStale {
		t.Fatalf("identical republishes must not hide a stale source: %+v", evaluation)
	}
}

func TestInactiveWitnessSuppressesRecovery(t *testing.T) {
	base := time.Now().UTC()
	tracker := NewTracker(1)
	tracker.SetConnected(true, base)
	tracker.Apply([]Metric{metric("battery_1/current", 1), metric("total/load_power", 10)}, base)
	tracker.Apply([]Metric{metric("battery_1/current", 1)}, base.Add(180*time.Second))

	evaluation := tracker.Evaluate(base.Add(180*time.Second), 180*time.Second, 30*time.Second)
	if !evaluation.AllStale || evaluation.WitnessActive {
		t.Fatalf("expected stale battery but inactive witness: %+v", evaluation)
	}
}

func TestIntermittentBatteryChangesPreventFalseFullFailure(t *testing.T) {
	base := time.Now().UTC()
	tracker := NewTracker(3)
	tracker.SetConnected(true, base)
	tracker.Apply([]Metric{
		metric("battery_1/cell_voltage_1", 3.31),
		metric("battery_2/cell_voltage_-_imbalance", 0.011),
		metric("battery_3/power", 20),
		metric("total/grid_power", 100),
	}, base)
	// Battery 2 misses most updates but one real dynamic change arrives just
	// before the threshold. That must be enough to suppress a full failure.
	tracker.Apply([]Metric{
		metric("battery_2/cell_voltage_-_imbalance", 0.012),
		metric("total/grid_power", 101),
	}, base.Add(179*time.Second))

	evaluation := tracker.Evaluate(base.Add(180*time.Second), 180*time.Second, 30*time.Second)
	if evaluation.AllStale {
		t.Fatalf("an intermittent real battery change must prevent full-failure recovery: %+v", evaluation)
	}
	if len(evaluation.StaleBatteries) != 2 {
		t.Fatalf("expected only batteries 1 and 3 to be stale: %+v", evaluation)
	}
}

func metric(topic string, value any) Metric {
	raw, _ := json.Marshal(value)
	return Metric{Topic: topic, Value: raw}
}
