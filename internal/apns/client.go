package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

const (
	productionHost = "https://api.push.apple.com"
	sandboxHost    = "https://api.sandbox.push.apple.com"
)

// ErrDeviceGone means APNs will never accept this token again and the caller
// should delete the device record. It is returned ONLY for 410/Unregistered
// and 400/BadDeviceToken — never for transient failures, because deleting a
// live device over an Apple outage is unrecoverable from our side.
var ErrDeviceGone = errors.New("apns: device token is no longer valid")

// Payload is the app-facing content of one notification. Body is a summary,
// not the full message: NotificationID points at the activity-feed record
// that holds the complete text, which is what the app opens on tap.
type Payload struct {
	Title          string
	Body           string
	NotificationID string
}

// Client talks to APNs over HTTP/2 with provider-token auth. Go's net/http
// negotiates HTTP/2 over TLS on its own; no extra dependency.
type Client struct {
	tokens *TokenSource
	http   *http.Client

	// hostOverride redirects every request at a test server. Empty in
	// production, where host() picks by the device's environment.
	hostOverride string
}

func New(tokens *TokenSource, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{tokens: tokens, http: httpClient}
}

// host picks the APNs endpoint from the DEVICE record, never by inference: a
// sandbox token presented to the production host answers BadDeviceToken,
// byte-identical to a genuinely dead token, and the pruning rule would then
// delete every development device while looking correct.
func (c *Client) host(d lineupapi.PushDevice) string {
	if c.hostOverride != "" {
		return c.hostOverride
	}
	if d.Environment == "sandbox" {
		return sandboxHost
	}
	return productionHost
}

func (c *Client) Push(ctx context.Context, d lineupapi.PushDevice, p Payload) error {
	body, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{"title": p.Title, "body": p.Body},
			"sound": "default",
		},
		"notification_id": p.NotificationID,
	})
	if err != nil {
		return fmt.Errorf("apns: marshal payload: %w", err)
	}

	tok, err := c.tokens.Token(time.Now())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.host(d)+"/3/device/"+d.Token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// apns-topic is the bundle id, and it comes from the DEVICE, not from a
	// constant: debug and release builds register different bundle ids.
	req.Header.Set("apns-topic", d.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apns: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var apnsErr struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &apnsErr)

	// Exactly two shapes mean "this token is dead forever": 410/Unregistered
	// (app deleted) and 400/BadDeviceToken (malformed or wrong environment).
	// Everything else — throttles, 5xx, our own payload bugs — must surface
	// as an ordinary error so the caller keeps the device.
	if resp.StatusCode == http.StatusGone || apnsErr.Reason == "Unregistered" ||
		(resp.StatusCode == http.StatusBadRequest && apnsErr.Reason == "BadDeviceToken") {
		return fmt.Errorf("apns: %s: %w", apnsErr.Reason, ErrDeviceGone)
	}
	return fmt.Errorf("apns: status %d: %s", resp.StatusCode, apnsErr.Reason)
}
