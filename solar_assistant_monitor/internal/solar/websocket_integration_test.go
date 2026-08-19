package solar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
)

func TestWebSocketClientConsumesPhoenixMetrics(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/websocket" || request.URL.Query().Get("password") != "secret" {
			http.NotFound(response, request)
			return
		}
		connection, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, join, err := connection.ReadMessage()
		if err != nil || len(join) == 0 {
			return
		}
		messages := []any{
			[]any{"1", "1", "metrics", "phx_reply", map[string]any{"status": "ok", "response": map[string]any{}}},
			[]any{"1", nil, "metrics", "data", map[string]any{"metrics": []map[string]any{
				{"topic": "battery_1/current", "value": 1.2},
				{"topic": "total/load_power", "value": 100},
			}}},
		}
		for _, message := range messages {
			payload, _ := json.Marshal(message)
			if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
		<-request.Context().Done()
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL)
	tracker := monitor.NewTracker(1)
	client := NewWebSocketClient(baseURL, "secret", 1, tracker, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if versions := tracker.Versions(); versions[0] > 0 {
			snapshot := tracker.Snapshot()
			if !snapshot.Connected || snapshot.LastWitnessChange.IsZero() {
				t.Fatalf("unexpected tracker snapshot: %+v", snapshot)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("WebSocket metrics were not applied before timeout")
}
