package notify

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Recorder, when set, is called for every Pushover send with the same title +
// message. NOTHING INSTALLS IT ANY MORE: fantasy events go through Dispatcher,
// which writes the feed record itself, FIRST, so that the record's id can ride
// in the push payload — and the four operator sends that still call
// SendPushover directly report on the bot's own health, which is not a
// tenant's activity feed's business. The seam survives only so a future
// caller can observe operator sends without editing this function. Do not
// reintroduce a feed write inside a delivery function: a record that exists
// only when delivery was attempted vanishes exactly when delivery breaks.
var Recorder func(title, message string)

// SendPushover sends a push notification via the Pushover API.
//
// Still exported and still called directly, by design: the personal operator
// channel (connect blocked, cache stale fallback, GS limit fetch failure,
// projection status) and gs-check's league-wide group broadcast deliberately
// do not route through Dispatcher — an alert about the bot's own health must
// not travel over the infrastructure whose health it reports on, and the
// group broadcast reaches league mates no app install could. Fantasy events
// belong on Dispatcher via Send; do not add new direct calls for those.
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
