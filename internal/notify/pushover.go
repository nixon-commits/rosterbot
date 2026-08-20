package notify

import "github.com/nixon-commits/rosterbot/internal/pushover"

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

// SendPushover sends a push notification via the Pushover API. The HTTP
// client itself lives in the stdlib-only internal/pushover leaf so the
// opsnotify Lambda can send without importing this package, which now pulls
// lineupapi (and transitively chromedp) through the dispatcher's sinks.
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
	return pushover.Send(userKey, apiToken, title, message)
}
