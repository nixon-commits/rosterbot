package fantrax

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// periodListEntryRe matches a Fantrax periodList dropdown entry like
// "104 (Mon Jul 6)" → group 1 = daily period number, group 2 = "Mon Jul 6".
var periodListEntryRe = regexp.MustCompile(`^(\d+)\s+\((\w+ \w+ \d+)\)$`)

// parsePeriodList turns Fantrax's periodList dropdown (DisplayedLists["periodList"])
// into a date→DailyPeriod map keyed by "2006-01-02". The label for period N is the
// calendar date whose roster snapshot lives at period N, so this is the
// authoritative daily-period numbering (self-correcting across Fantrax's mid-season
// period insertions). Year comes from seasonYear; an entry whose month precedes
// startMonth rolls to seasonYear+1 (defensive — MLB seasons don't cross a year
// boundary). Malformed/non-string entries are skipped, never fatal.
func parsePeriodList(entries []interface{}, seasonYear int, startMonth time.Month) map[string]DailyPeriod {
	out := make(map[string]DailyPeriod, len(entries))
	for _, e := range entries {
		s, ok := e.(string)
		if !ok {
			continue
		}
		m := periodListEntryRe.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		dt, err := time.Parse("Mon Jan 2 2006", fmt.Sprintf("%s %d", m[2], seasonYear))
		if err != nil {
			continue
		}
		if dt.Month() < startMonth {
			dt = dt.AddDate(1, 0, 0)
		}
		out[dt.Format("2006-01-02")] = DailyPeriod(num)
	}
	return out
}
