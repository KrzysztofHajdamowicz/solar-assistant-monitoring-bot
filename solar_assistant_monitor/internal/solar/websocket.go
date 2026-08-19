package solar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
)

type WebSocketClient struct {
	baseURL      *url.URL
	password     string
	batteryCount int
	tracker      *monitor.Tracker
	logger       *slog.Logger
	onState      func(bool, error)
}

func NewWebSocketClient(baseURL *url.URL, password string, batteryCount int, tracker *monitor.Tracker, logger *slog.Logger, onState func(bool, error)) *WebSocketClient {
	return &WebSocketClient{
		baseURL:      baseURL,
		password:     password,
		batteryCount: batteryCount,
		tracker:      tracker,
		logger:       logger,
		onState:      onState,
	}
}

func (c *WebSocketClient) Run(ctx context.Context) error {
	backoff := time.Second
	paths := []string{"/api/websocket", "/api/socket/websocket"}
	pathIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		generation := c.tracker.Snapshot().ConnectionGeneration
		err := c.runConnection(ctx, paths[pathIndex])
		connectedOnce := c.tracker.Snapshot().ConnectionGeneration > generation
		c.tracker.SetConnected(false, time.Now().UTC())
		if c.onState != nil && ctx.Err() == nil {
			c.onState(false, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if connectedOnce {
			// A previously working endpoint remains preferred after a normal
			// disconnect. Also reset retry latency after every real session.
			backoff = time.Second
		} else {
			pathIndex = (pathIndex + 1) % len(paths)
		}
		c.logger.Warn("Solar Assistant WebSocket disconnected", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if !connectedOnce && backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *WebSocketClient) runConnection(ctx context.Context, path string) error {
	endpoint := *c.baseURL
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = path
	query := endpoint.Query()
	query.Set("password", c.password)
	query.Set("vsn", "2.0.0")
	endpoint.RawQuery = query.Encode()

	conn, response, err := websocket.DefaultDialer.DialContext(ctx, endpoint.String(), http.Header{})
	if err != nil {
		if response != nil {
			return fmt.Errorf("dial %s: HTTP %d: %w", path, response.StatusCode, err)
		}
		return fmt.Errorf("dial %s: %w", path, err)
	}
	defer conn.Close()

	writer := &safeWriter{conn: conn}
	if err := writer.JSON(c.joinMessage()); err != nil {
		return fmt.Errorf("join metrics channel: %w", err)
	}
	c.tracker.SetConnected(true, time.Now().UTC())
	if c.onState != nil {
		c.onState(true, nil)
	}
	c.logger.Info("connected to Solar Assistant WebSocket", "path", path)

	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErrors := make(chan error, 1)
	go c.heartbeat(connectionCtx, writer, heartbeatErrors)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		metrics, event, err := decodePhoenix(payload)
		if err != nil {
			c.logger.Debug("ignored malformed WebSocket message", "error", err)
			continue
		}
		if event == "data" && len(metrics) > 0 {
			c.tracker.Apply(metrics, time.Now().UTC())
		}
		select {
		case err := <-heartbeatErrors:
			return err
		default:
		}
	}
}

func (c *WebSocketClient) joinMessage() []any {
	filters := make([]map[string]string, 0, c.batteryCount+2)
	for index := 1; index <= c.batteryCount; index++ {
		filters = append(filters, map[string]string{"topic": fmt.Sprintf("battery_%d/*", index)})
	}
	filters = append(filters,
		map[string]string{"topic": "total/load_power"},
		map[string]string{"topic": "total/grid_power"},
	)
	return []any{"1", "1", "metrics", "phx_join", map[string]any{"topics": filters}}
}

func (c *WebSocketClient) heartbeat(ctx context.Context, writer *safeWriter, errorsChannel chan<- error) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	ref := 2
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			message := []any{nil, strconv.Itoa(ref), "phoenix", "heartbeat", map[string]any{}}
			ref++
			if err := writer.JSON(message); err != nil {
				select {
				case errorsChannel <- fmt.Errorf("send heartbeat: %w", err):
				default:
				}
				return
			}
		}
	}
}

type safeWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *safeWriter) JSON(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteJSON(value)
}

func decodePhoenix(payload []byte) ([]monitor.Metric, string, error) {
	var envelope []json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, "", err
	}
	if len(envelope) != 5 {
		return nil, "", errors.New("unexpected Phoenix envelope length")
	}
	var event string
	if err := json.Unmarshal(envelope[3], &event); err != nil {
		return nil, "", err
	}
	if event != "data" {
		return nil, event, nil
	}
	var data struct {
		Metrics []monitor.Metric `json:"metrics"`
	}
	if err := json.Unmarshal(envelope[4], &data); err != nil {
		return nil, event, err
	}
	return data.Metrics, event, nil
}
