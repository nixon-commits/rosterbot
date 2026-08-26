// Package pushover is the Pushover HTTP client, and nothing else.
//
// A STDLIB-ONLY LEAF, for the same reason internal/opsalert is one: the
// opsnotify Lambda sends Pushover alerts, and internal/notify — the previous
// home of this function — now hosts the fantasy-event dispatcher, whose APNs
// and feed sinks import internal/lineupapi and therefore (transitively)
// internal/fantrax and chromedp. Importing that from a Lambda that exists to
// say "a task failed" would bloat it with a headless browser it can never
// use. notify.SendPushover survives as a thin delegate over Send, so the
// operator call sites did not move.
package pushover

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Send sends a push notification via the Pushover API. Messages longer than
// MaxMessageLen are truncated on a rune boundary with a trailing ellipsis —
// the never-should-fire backstop behind callers that budget whole blocks via
// Builder before sending.
func Send(userKey, apiToken, title, message string) error {
	message = Truncate(message)

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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pushover API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}
