package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// feedWriter is the slice of the notification store this needs.
type feedWriter interface {
	PutNotification(ctx context.Context, n lineupapi.Notification) error
}

// recordConnectFailure tells whoever can actually act about a failed connect.
//
// THIS IS THE FEED'S FIRST WRITER THAT IS NOT PUSHOVER, and that is the point
// (rosterbot-crq.14). Until now the only thing that produced a Notification was
// notify.Recorder, which fires INSIDE SendPushover — so nothing could reach a
// tenant's feed without also reaching the operator's phone, and
// PUSHOVER_USER_KEY is one deployment-wide secret. The feed was a mirror of the
// operator's alerts rather than a per-tenant channel, which is why "tenant-
// actionable -> that user's in-app feed" had no implementation.
//
// The connect task already runs with ROSTERBOT_USER_ID set to the tenant
// (handleConnect passes the caller to the launcher), so the store composed from
// the environment is already theirs. Only the writer was missing.
//
// ROUTING IS BY WHO CAN ACT, not by severity. A tenant-actionable failure goes
// to their feed and NOT to the operator — becoming the help desk for problems
// users can fix themselves is the specific outcome this exists to prevent. An
// operator-actionable one goes the other way, because telling a user to
// re-enter a working password wastes their time while leaving the one person
// who can act unaware.
func recordConnectFailure(ctx context.Context, feed feedWriter, push func(string),
	uid lineupapi.UserID, class string) {

	if operatorActionableClass(class) {
		if push != nil {
			push(fmt.Sprintf("Fantrax connect blocked for %s: %s. This needs you, not them.",
				uid, class))
		}
		return
	}

	recordTenantConnectFailure(ctx, feed, uid, class)
}

// recordTenantConnectFailure writes the tenant half only, for callers that have
// already decided the failure is theirs to hear about.
//
// SPLIT OUT BECAUSE THE OPERATOR HALF IS NOT SHARED (rosterbot-zi4u). The push
// above is edge-triggered: it fires once per user-initiated connect, because a
// human pressed a button. The session ladder runs inside EVERY scheduled job of
// every tenant, so routing it through the same operator push made the alert
// level-triggered — a bot challenge that stands for a day would have restated
// itself on all ~24 of that day's runs, the "30 pushes in 24 hours" failure
// CLAUDE.md records for the stale-cache alert. The ladder therefore calls this
// function, and reports its operator-actionable half on the console instead.
func recordTenantConnectFailure(ctx context.Context, feed feedWriter,
	uid lineupapi.UserID, class string) {

	if feed == nil {
		// Never silent. The feed is the ONLY channel a tenant-actionable failure
		// has, so its absence has to be visible beside the failure itself rather
		// than only wherever the store failed to open — otherwise a run that
		// told nobody looks identical to one that told them.
		fmt.Fprintf(os.Stderr, "warning: no activity feed for %s; the %s failure reaches "+
			"nobody\n", uid, class)
		return
	}
	now := time.Now().UTC()
	n := lineupapi.Notification{
		ID:      lineupapi.NewNotificationID(now),
		Kind:    "alert",
		Status:  "failure",
		Title:   "Fantrax connection failed",
		Message: connectFailureMessage(class),
		// The tenant is on the RECORD, not only in the S3 prefix it was written
		// under. A record that cannot say whose it is once read is the shape
		// that made per-tenant notifications unverifiable.
		UserID:    string(uid),
		CreatedAt: now.Format(time.RFC3339),
		RunID:     os.Getenv("RUN_ID"),
	}
	// Soft, like everything else on this path: the connect task already records
	// the class on the connection record and exits 0 deliberately, so a feed
	// hiccup must not turn a diagnosable outcome into a crashed task.
	if err := feed.PutNotification(ctx, n); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write the connect failure to %s's feed: %v\n",
			uid, err)
	}
}

// connectFailureMessage is the tenant-facing wording for a failure class.
//
// The classes exist so a person can be told WHICH failure happened; handing
// them the raw class would throw that away at the last step. It mirrors
// settings.js's FAILURE_COPY deliberately — the feed and the settings page must
// not disagree about what went wrong.
func connectFailureMessage(class string) string {
	switch class {
	case lineupapi.ConnErrBadCredentials:
		return "Fantrax rejected that username or password. Reconnect in Settings with the correct details."
	case lineupapi.ConnErrTwoFactor:
		return "Fantrax asked for a two-factor code, which rosterbot cannot answer. " +
			"This account can only be connected with two-factor auth turned off."
	case lineupapi.ConnErrTeamNotOwned:
		return "Those credentials do not control the team you were invited for."
	case lineupapi.ConnErrNoTeam:
		return "No Fantrax team is assigned to your account yet — ask the admin to set one."
	case lineupapi.ConnErrTeamClaimed:
		return "That Fantrax team is already claimed by another account."
	case lineupapi.ConnErrVerificationInterrupted:
		return "Your Fantrax sign-in worked — your password is not the problem. " +
			"Something after it did not finish, so the connection is not confirmed yet. " +
			"Try connecting again in a minute."
	case lineupapi.ConnErrCheckFailed:
		return "The check couldn't run because of a problem on our side — your password " +
			"is not the problem. Try connecting again in a minute."
	default:
		return "The Fantrax sign-in did not complete and Fantrax did not say why. " +
			"Trying again in Settings sometimes works."
	}
}

// notifyOperator pushes to the operator's personal channel.
//
// Separated from recordConnectFailure so the routing decision is testable
// without the network, and so the one place that decides WHO hears about a
// failure is not also the place that knows how to send. Its signature is the
// fixed `func(string)` shape connectDeps.push carries (both here and in the
// session ladder), so it has no request-scoped context to accept — this is
// the outermost point, and context.Background() here is explicit rather than
// threaded blindly deeper.
func notifyOperator(message string) {
	user, token := os.Getenv("PUSHOVER_USER_KEY"), os.Getenv("PUSHOVER_API_TOKEN")
	if user == "" || token == "" {
		return
	}
	if err := notify.SendPushover(context.Background(), user, token, "rosterbot: connect blocked", message); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not alert the operator: %v\n", err)
	}
}
