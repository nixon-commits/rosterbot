package lineuprun

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/roster"
)

// ilStartLookahead is how many days past today to check for announced starts.
// Today is still recoverable before first pitch; tomorrow is the one that buys
// a full day to act. Beyond that, probables are too sparse to be worth the call.
const ilStartLookahead = 1

// ilStartSchedule is the probable-starter surface this check needs.
// *schedule.Client satisfies it; a map-backed fake satisfies it in tests.
type ilStartSchedule interface {
	ProbableStarters(date time.Time) (map[string]string, error)
}

// ilStartMarkers is the dedup seam. lineupapi.BlobStore satisfies it; a nil
// value disables dedup, which is the correct local-dev behavior (alert every
// run rather than stay quiet about something there is no record of).
type ilStartMarkers interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Publish(key string, data []byte) error
}

type ilStartInputs struct {
	Roster  []fantrax.Player
	Sched   ilStartSchedule
	Today   time.Time
	Markers ilStartMarkers
	// Notify returns an error so a failed send can be left unmarked. The
	// void Emit.Notify seam cannot express that, and check -> send -> mark
	// is only sound if "sent" is actually known.
	Notify func(message string) error
	DryRun bool
	Out    io.Writer
}

// reportILStarts finds players stranded in an IL slot whose MLB club has
// announced them as a probable starter, prints them, and pushes each once.
//
// This is the only signal for that situation. Fantrax's injury icon — the sole
// input to Player.IsInjured, and so to roster.CheckRoster's HealthyInIL — is
// editorial and lags real activations, so the existing alert stays silent
// exactly while the start is still recoverable. And because
// fetchPitcherRosterForPeriod reads only Active+Reserve, an IL pitcher never
// reaches the optimizer: the start is lost outright, not merely mis-slotted.
//
// Every failure here is soft. This runs inside the hourly lineup job, and a
// statsapi hiccup or an unreachable marker store must never take that down.
func reportILStarts(in ilStartInputs) {
	var (
		found        []roster.ILStart
		mismatches   []string
		probableSeen int
	)
	for i := 0; i <= ilStartLookahead; i++ {
		date := in.Today.AddDate(0, 0, i)
		probables, err := in.Sched.ProbableStarters(date)
		if err != nil {
			fmt.Fprintf(in.Out, "  il-start check: probables unavailable for %s: %v\n",
				date.Format("2006-01-02"), err)
			continue
		}
		probableSeen += len(probables)
		starts, mismatch := roster.CheckILStarters(in.Roster, probables, date)
		found = append(found, starts...)
		mismatches = append(mismatches, mismatch...)
	}

	// Printed unconditionally, including the all-zero case, on the same
	// reasoning as the "mlb recency coverage:" line: this check's normal
	// output is silence, so without the two input counts a genuinely clean
	// roster and a structurally blind check look identical. If IL=0 the check
	// cannot fire because there is nothing to find; if probables=0 it cannot
	// fire because the feed is empty. Those are different problems and only
	// this line tells them apart.
	fmt.Fprintf(in.Out, "  il-start check: %d IL, %d probable starters over %d days, %d match\n",
		countIL(in.Roster), probableSeen, ilStartLookahead+1, len(found))

	// A name that matched but whose club disagreed is usually a lagging trade
	// on one side or the other. Naming it keeps a missed alert visible instead
	// of silent — the failure mode this whole check exists to remove.
	if len(mismatches) > 0 {
		fmt.Fprintf(in.Out, "  il-start check: name matched but club disagreed: %s\n",
			strings.Join(mismatches, ", "))
	}

	if len(found) == 0 {
		return
	}

	fmt.Fprintln(in.Out, "\n=== IL Players With An Announced Start ===")
	ctx := context.Background()
	for _, s := range found {
		key := ilStartMarkerKey(s)
		msg := ilStartMessage(s)
		fmt.Fprintf(in.Out, "  🚨 %-25s (%s)  starting %s → activate off IL\n",
			s.Player.Name, s.Player.MLBTeam, s.StartDate.Format("Mon Jan 2"))

		if in.DryRun {
			continue // neither send nor mark: marking here would mute a real alert later
		}

		// check
		if in.Markers != nil {
			if _, already, err := in.Markers.Get(ctx, key); err != nil {
				fmt.Fprintf(in.Out, "  il-start check: marker read %s: %v (treating as unsent)\n", key, err)
			} else if already {
				continue
			}
		}

		// send
		if err := in.Notify(msg); err != nil {
			fmt.Fprintf(in.Out, "  il-start check: send failed for %s: %v\n", key, err)
			continue // do not mark: a failed send must retry, never go silent
		}

		// mark — never claim-then-send (rosterbot-chs). A marker-write failure
		// degrades to a duplicate alert next hour, never to silence.
		if in.Markers != nil {
			if err := in.Markers.Publish(key, []byte(msg)); err != nil {
				fmt.Fprintf(in.Out, "  il-start check: marker write %s: %v (next run will alert again)\n", key, err)
			}
		}
	}
	fmt.Fprintln(in.Out)
}

// ilStartMarkerKey keys on (player, start date), not player alone: a pitcher
// stranded across two separate starts is two things worth hearing about, and a
// player-only key would alert once and then stay quiet for the rest of the season.
func ilStartMarkerKey(s roster.ILStart) string {
	return s.Player.ID + "-" + s.StartDate.Format("2006-01-02")
}

func countIL(players []fantrax.Player) int {
	n := 0
	for _, p := range players {
		if p.Status == "Injured Reserve" {
			n++
		}
	}
	return n
}

func ilStartMessage(s roster.ILStart) string {
	return fmt.Sprintf("🚨 %s (%s) is starting %s but is in your IL slot — activate him or lose the start.",
		s.Player.Name, s.Player.MLBTeam, s.StartDate.Format("Mon Jan 2"))
}
