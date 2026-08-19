package solar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/solar_assistant_monitor/internal/monitor"
)

type RESTClient struct {
	baseURL  *url.URL
	username string
	password string
	client   *http.Client
}

func NewRESTClient(baseURL *url.URL, username, password string) *RESTClient {
	return &RESTClient{
		baseURL:  baseURL,
		username: username,
		password: password,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *RESTClient) Metrics(ctx context.Context) ([]monitor.Metric, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned HTTP %d", response.StatusCode)
	}
	var metrics []monitor.Metric
	if err := json.NewDecoder(response.Body).Decode(&metrics); err != nil {
		return nil, err
	}
	return metrics, nil
}
