package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/spf13/cobra"
)

var notifyTestCmd = &cobra.Command{
	Use:   "notify-test",
	Short: "Send one synthetic event through the real dispatcher to this tenant's registered devices",
	Long: `Emits a single notify.Event through the production dispatcher: a durable
activity-feed record first, then APNs to every registered device (and Pushover
while PUSHOVER_FANTASY_DUAL_SEND is set).

This exists because no ordinary job can be used to test push delivery. Every one
of the nine notify.Send call sites is gated behind !dryRun -- emit.go returns
before applyLineupFor, transactions.go returns before its send -- and the iOS
Debug build forces dry_run=true on every job it launches (APIClient.guardedParams).
So a developer holding a Debug build has no way to make a real push happen, and
the alternative -- running a live optimize -- writes to the real Fantrax roster
just to prove a notification works.

It touches no roster and calls no upstream. It reads the device table, writes one
feed record, and pushes. Run it against production credentials from a workstation.

Failure is deliberately LOUD. A dispatcher with no feed store, no APNs sink, or no
registered devices exits non-zero rather than printing nothing and returning 0: a
test command whose silence is indistinguishable from success is worse than no test
command, and this repository has been bitten by exactly that shape before.`,
	RunE: runNotifyTest,
}

var (
	notifyTestKind     string
	notifyTestTitle    string
	notifyTestMessage  string
	notifyTestFeedOnly bool
)

func init() {
	notifyTestCmd.Flags().StringVar(&notifyTestKind, "kind", "lineup",
		"event kind (lineup|waivers|claims|transactions|prospects|gs-check|alert)")
	notifyTestCmd.Flags().StringVar(&notifyTestTitle, "title", "RosterBot push test",
		"notification title")
	notifyTestCmd.Flags().StringVar(&notifyTestMessage, "message",
		"If this arrived on your phone, APNs delivery works. Tap it to check that it opens this item in Activity.",
		"notification body")
	notifyTestCmd.Flags().BoolVar(&notifyTestFeedOnly, "feed-only", false,
		"write the feed record without requiring a registered device (Simulator workflow: "+
			"the Simulator never receives an APNs token, so `xcrun simctl push` delivers the "+
			"payload locally and this only needs to supply the feed record it points at)")
	rootCmd.AddCommand(notifyTestCmd)
}

func runNotifyTest(cmd *cobra.Command, _ []string) error {
	if err := initShared(); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	ctx := cmd.Context()

	// The feed record is the durable half and the id the push payload carries,
	// so an unconfigured dispatcher cannot deliver a tappable notification at
	// all. notify.Send would return nil here -- a silent no-op by design (see
	// dispatch.go's Default) -- which is precisely the success-shaped failure
	// this command must not reproduce.
	if !notify.Configured() {
		return errors.New("notify dispatcher not configured: no durable feed store " +
			"(STATE_BUCKET / statestore env missing?) -- nothing would be recorded or delivered")
	}

	uid := statestore.Tenant()
	if uid == "" {
		return errors.New("ROSTERBOT_USER_ID is empty: an event with no tenant has no devices to fan out to")
	}

	// Read the device list directly rather than inferring from the send's
	// outcome. APNsSink treats "tenant has no devices" as the normal
	// pre-install state and returns nil, so a send into an empty table is
	// indistinguishable from a delivered one at this layer.
	// --feed-only exists for the Simulator, which never gets an APNs token and so
	// can never appear in this table. Its absence is then the expected state, not
	// the misconfiguration the device guard is there to catch, and failing on it
	// would block the one workflow that can still exercise the tap path.
	devices, err := notifyTestDevices(ctx, lineupapi.UserID(uid))
	if err != nil && !notifyTestFeedOnly {
		return err
	}
	if len(devices) == 0 && !notifyTestFeedOnly {
		return fmt.Errorf("tenant %s has no registered push devices: "+
			"sign in on the phone first so it registers, then re-run "+
			"(or pass --feed-only for the Simulator, which cannot register)", uid)
	}
	for _, d := range devices {
		fmt.Printf("  device %s  env=%s  bundle=%s  model=%s\n", d.ID, d.Environment, d.BundleID, d.Model)
	}

	if err := notify.Send(ctx, notify.Event{
		Kind:    notifyTestKind,
		Title:   notifyTestTitle,
		Message: notifyTestMessage,
	}); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	fmt.Printf("\nRecorded feed event (kind=%s) and dispatched to %d device(s).\n",
		notifyTestKind, len(devices))

	// Re-read the device list rather than telling the operator to interpret the
	// absence of a warning. APNsSink prunes on ErrDeviceGone and only prints
	// anything if the DELETE itself fails, so "no output" covers both "Apple
	// accepted it" and "Apple called the token dead and we removed it" -- two
	// outcomes that demand opposite next steps. A disappeared device is the one
	// signal that distinguishes them, and it costs one query to look.
	// Nothing was addressed, so there is nothing to conclude about delivery.
	// Saying "APNs accepted the push" here would be vacuously true and read as
	// evidence -- the same success-shaped silence this block exists to remove.
	if len(devices) == 0 {
		fmt.Println("No devices registered: the feed record was written, but no push was sent.")
		return nil
	}

	after, err := notifyTestDevices(ctx, lineupapi.UserID(uid))
	if err != nil {
		fmt.Printf("could not re-read devices to confirm delivery: %v\n", err)
		return nil
	}

	if gone := prunedDeviceIDs(devices, after); len(gone) > 0 {
		return fmt.Errorf("APNs rejected %d of %d device token(s) as permanently dead "+
			"(410/Unregistered or 400/BadDeviceToken) and they were pruned: %s -- "+
			"the push did NOT arrive there; re-register by signing in again on that device",
			len(gone), len(devices), strings.Join(gone, ", "))
	}
	fmt.Printf("All %d device token(s) survived: APNs accepted the push for each.\n", len(devices))
	fmt.Println("A transient failure would have printed 'warning: apns push to ...' above.")
	return nil
}

// prunedDeviceIDs reports which of before is absent from after.
//
// Compared by ID rather than by count: a device pruned in the same window that
// another registers leaves len(before) == len(after), and a count check would
// then report a delivery that did not happen -- precisely the failure this is
// here to catch. Pure, so that failure path is testable without APNs.
func prunedDeviceIDs(before, after []lineupapi.PushDevice) []string {
	survived := make(map[string]bool, len(after))
	for _, d := range after {
		survived[d.ID] = true
	}
	var gone []string
	for _, d := range before {
		if !survived[d.ID] {
			gone = append(gone, d.ID)
		}
	}
	return gone
}

// notifyTestDevices lists the tenant's registered devices, reporting the same
// missing-configuration cases apnsSink swallows into a nil sink. Kept separate
// so the reasons are distinguishable: "APNs is not configured" and "APNs is
// configured but you have no devices" call for different fixes.
func notifyTestDevices(ctx context.Context, uid lineupapi.UserID) ([]lineupapi.PushDevice, error) {
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return nil, errors.New("IDENTITY_TABLE is unset: the device registry is unreachable, so no push can be addressed")
	}
	store, err := ddbPushDeviceStore(table)
	if err != nil {
		return nil, fmt.Errorf("device store: %w", err)
	}
	devices, err := store.PushDevices(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return devices, nil
}
