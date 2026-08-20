package notify

import "github.com/nixon-commits/rosterbot/internal/pushover"

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
//
// The old Recorder hook — which fired here so every send doubled as a feed
// write — is gone, not just unset. The durable record is Dispatcher's job and
// it happens FIRST, so the record's id can ride in the push payload; do not
// reintroduce a feed write inside a delivery function, because a record that
// exists only when delivery was attempted vanishes exactly when delivery
// breaks.
func SendPushover(userKey, apiToken, title, message string) error {
	return pushover.Send(userKey, apiToken, title, message)
}
