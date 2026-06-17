package notify

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Recorder, when set, is called for every Pushover send with the same title +
// message — the seam the app's activity feed hooks into so events are persisted
// (dual-send) without each command knowing about the feed. Set process-wide in
// cmd.initApp, mirroring the cache.Notify global. Best-effort: the recorder
// runs after the send and its outcome never affects SendPushover's result.
var Recorder func(title, message string)

// SendPushover sends a push notification via the Pushover API.
func SendPushover(userKey, apiToken, title, message string) error {
	if r := Recorder; r != nil {
		r(title, message)
	}
	if len(message) > 1024 {
		message = message[:1024]
	}

	data := url.Values{
		"token":    {apiToken},
		"user":     {userKey},
		"message":  {message},
		"priority": {"0"},
		"title":    {title},
		"html":     {"1"},
	}

	resp, err := http.PostForm("https://api.pushover.net/1/messages.json", data)
	if err != nil {
		return fmt.Errorf("send pushover request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pushover API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}
