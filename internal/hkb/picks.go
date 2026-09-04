package hkb

import (
	"fmt"
	"regexp"
	"strconv"
)

// pickNameRe matches HKB's PICK asset naming convention: "2027 Early 1st",
// "2028 Late 3rd", etc. HKB prices a pick by projected draft slot (Early,
// Mid, or Late within its round), never by which team holds it -- there is
// no team name anywhere in the 18 PICK rows in the dataset.
var pickNameRe = regexp.MustCompile(`^(\d{4}) (Early|Mid|Late) (\d+)(?:st|nd|rd|th)$`)

// PickAverage returns the mean HKB value across whichever of the three
// projected-slot tiers (Early/Mid/Late) exist for the given draft year and
// round (1 = 1st round, 2 = 2nd, ...), along with how many tiers
// contributed to it.
//
// Fantrax's trade payload identifies a traded pick by year, round, and the
// team whose future draft slot it is (see internal/tradevalue.NewDraftPickAsset)
// -- but not that team's projected standing, which is what would be needed
// to pick one of HKB's three tiers over the other two. Averaging them is a
// deliberately coarse stand-in: a defensible single number rather than a
// claim of precision the recovered identity doesn't support -- the same
// "disagreement over false precision" spirit in which tradevalue withholds a
// verdict when its two pricing methods diverge.
//
// tiersFound is 0 for a year HKB no longer lists as upcoming (its draft has
// already happened), which is the common case for a pick identified from an
// old executed trade -- the caller should leave the asset unpriced rather
// than average zero tiers.
func PickAverage(players []Player, year, round int) (avg int, tiersFound int) {
	total := 0
	for _, p := range players {
		if p.AssetType != "PICK" {
			continue
		}
		m := pickNameRe.FindStringSubmatch(p.Name)
		if m == nil {
			continue
		}
		y, _ := strconv.Atoi(m[1])
		r, _ := strconv.Atoi(m[3])
		if y != year || r != round {
			continue
		}
		total += p.Value
		tiersFound++
	}
	if tiersFound == 0 {
		return 0, 0
	}
	return total / tiersFound, tiersFound
}

// Ordinal renders a round number the way HKB's pick names do: 1 -> "1st",
// 2 -> "2nd", 3 -> "3rd", anything else -> "4th" etc.
func Ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return fmt.Sprintf("%dth", n)
	}
}
