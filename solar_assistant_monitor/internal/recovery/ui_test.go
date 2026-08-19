package recovery

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestUIReconnectAgainstMockPage(t *testing.T) {
	if os.Getenv("RUN_BROWSER_TESTS") != "1" {
		t.Skip("set RUN_BROWSER_TESTS=1 to run the Chromium integration test")
	}
	chromiumPath := findTestBrowser()
	if chromiumPath == "" {
		t.Skip("Chromium/Chrome is not installed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "admin" || password != "secret" {
			response.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(response, mockConfigurationPage)
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL)
	client := NewUIClient(baseURL, "admin", "secret", chromiumPath, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	statuses, err := client.Reconnect(ctx, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if statuses.Inverter != "Connected" || statuses.Battery != "Connected" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
}

func findTestBrowser() string {
	if configured := os.Getenv("CHROMIUM_PATH"); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
	}
	for _, name := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	if runtime.GOOS == "darwin" {
		path := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

const mockConfigurationPage = `<!doctype html>
<html><body>
<div id="devices"><form>
  <h4>Inverter</h4>
  <div class="form-field"><label>Status</label><div><span id="inverter-status" class="status-with-dot positive">Connected</span></div></div>
  <h4>Battery</h4>
  <div class="form-field"><label>Status</label><div><span id="battery-status" class="status-with-dot positive">Connected</span></div></div>
  <a id="action" href="#" phx-click="disconnect">Disconnect</a>
</form></div>
<script>
document.getElementById('action').addEventListener('click', function(event) {
  event.preventDefault();
  const link = event.currentTarget;
  if (link.getAttribute('phx-click') === 'disconnect') {
    document.getElementById('inverter-status').textContent = 'Disconnected';
    document.getElementById('battery-status').textContent = 'Disconnected';
    link.setAttribute('phx-click', 'connect');
    link.textContent = 'Connect';
  } else {
    document.getElementById('inverter-status').textContent = 'Connected';
    document.getElementById('battery-status').textContent = 'Connected';
    link.setAttribute('phx-click', 'disconnect');
    link.textContent = 'Disconnect';
  }
});
</script>
</body></html>`
