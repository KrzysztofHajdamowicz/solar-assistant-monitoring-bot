package recovery

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type UIStatuses struct {
	Inverter string `json:"inverter"`
	Battery  string `json:"battery"`
}

type UIClient struct {
	baseURL        *url.URL
	username       string
	password       string
	chromiumPath   string
	disconnectWait time.Duration
	logger         *slog.Logger
}

func NewUIClient(baseURL *url.URL, username, password, chromiumPath string, disconnectWait time.Duration, logger *slog.Logger) *UIClient {
	return &UIClient{
		baseURL:        baseURL,
		username:       username,
		password:       password,
		chromiumPath:   chromiumPath,
		disconnectWait: disconnectWait,
		logger:         logger,
	}
}

func (c *UIClient) Reconnect(ctx context.Context, connectTimeout time.Duration) (UIStatuses, error) {
	browserCtx, cancel, err := c.browserContext(ctx)
	if err != nil {
		return UIStatuses{}, err
	}
	defer cancel()
	if err := c.navigate(browserCtx); err != nil {
		return UIStatuses{}, err
	}
	if err := clickDeviceAction(browserCtx, "disconnect"); err != nil {
		return UIStatuses{}, fmt.Errorf("click Disconnect: %w", err)
	}
	if err := waitForDeviceAction(browserCtx, "connect", 30*time.Second); err != nil {
		return UIStatuses{}, fmt.Errorf("wait for Connect: %w", err)
	}
	select {
	case <-browserCtx.Done():
		return UIStatuses{}, browserCtx.Err()
	case <-time.After(c.disconnectWait):
	}
	if err := clickDeviceAction(browserCtx, "connect"); err != nil {
		return UIStatuses{}, fmt.Errorf("click Connect: %w", err)
	}
	return waitForConnected(browserCtx, connectTimeout)
}

func (c *UIClient) CheckConnected(ctx context.Context, timeout time.Duration) (UIStatuses, error) {
	browserCtx, cancel, err := c.browserContext(ctx)
	if err != nil {
		return UIStatuses{}, err
	}
	defer cancel()
	if err := c.navigate(browserCtx); err != nil {
		return UIStatuses{}, err
	}
	return waitForConnected(browserCtx, timeout)
}

func (c *UIClient) browserContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	path := c.chromiumPath
	if _, err := os.Stat(path); err != nil {
		if _, fallbackErr := os.Stat("/usr/bin/chromium"); fallbackErr == nil {
			path = "/usr/bin/chromium"
		} else {
			return nil, nil, fmt.Errorf("Chromium executable not found at %s", c.chromiumPath)
		}
	}
	options := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
	)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(parent, options...)
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx, chromedp.WithLogf(func(format string, args ...any) {
		c.logger.Debug("chromium", "message", fmt.Sprintf(format, args...))
	}))
	cancel := func() {
		browserCancel()
		allocatorCancel()
	}
	return browserCtx, cancel, nil
}

func (c *UIClient) navigate(ctx context.Context) error {
	endpoint := *c.baseURL
	endpoint.Path = "/configuration"
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password))
	return chromedp.Run(ctx,
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers{"Authorization": authorization}),
		chromedp.Navigate(endpoint.String()),
		chromedp.WaitVisible(`#devices`, chromedp.ByQuery),
	)
}

func clickDeviceAction(ctx context.Context, action string) error {
	script := fmt.Sprintf(`(() => {
  const link = document.querySelector('#devices a[phx-click="%s"]');
  if (!link) return false;
  link.click();
  return true;
})()`, action)
	var clicked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &clicked)); err != nil {
		return err
	}
	if !clicked {
		return fmt.Errorf("device action %q is not available", action)
	}
	return nil
}

func waitForDeviceAction(ctx context.Context, action string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	selector := fmt.Sprintf(`#devices a[phx-click="%s"]`, action)
	for time.Now().Before(deadline) {
		var present bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q) !== null`, selector), &present)); err != nil {
			return err
		}
		if present {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("device action %q did not appear before timeout", action)
}

func waitForConnected(ctx context.Context, timeout time.Duration) (UIStatuses, error) {
	deadline := time.Now().Add(timeout)
	last := UIStatuses{}
	for time.Now().Before(deadline) {
		statuses, err := readStatuses(ctx)
		if err != nil {
			return last, err
		}
		last = statuses
		if statuses.Inverter == "Connected" && statuses.Battery == "Connected" {
			return statuses, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return last, fmt.Errorf("device statuses did not become Connected: inverter=%q battery=%q", last.Inverter, last.Battery)
}

func readStatuses(ctx context.Context) (UIStatuses, error) {
	script := `(() => {
  const form = document.querySelector('#devices form');
  if (!form) return null;
  const find = (title) => {
    const heading = Array.from(form.querySelectorAll('h4')).find((h) => h.textContent.trim() === title);
    if (!heading) return '';
    let node = heading.nextElementSibling;
    while (node && node.tagName !== 'H4') {
      const label = node.querySelector && node.querySelector('label');
      if (label && label.textContent.replace(':', '').trim() === 'Status') {
        const status = node.querySelector('.status-with-dot');
        return status ? status.textContent.trim() : '';
      }
      node = node.nextElementSibling;
    }
    return '';
  };
  return {inverter: find('Inverter'), battery: find('Battery')};
})()`
	var result *UIStatuses
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		return UIStatuses{}, err
	}
	if result == nil {
		return UIStatuses{}, errors.New("Solar Assistant device form is missing")
	}
	return *result, nil
}
