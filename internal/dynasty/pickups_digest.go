package dynasty

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/pushover"
)

// PickupItem is one line of a league's pickup digest with the facts the
// ranking and the dedup marker need.
type PickupItem struct {
	Kind     string // chop | drop | role
	Key      string // marker key without the league prefix
	PlayerID string
	Line     string
	Position string
	Value    int
	Priced   bool
	ChopSize int
}

// PlayerIDIs is a small test convenience.
func (it PickupItem) PlayerIDIs(id string) bool { return it.PlayerID == id }

// PickupItems renders detector output into digest lines, one per player so
// each is marked individually: a chop with fourteen players is fourteen
// markers under one transaction, and a digest that already sent nine of them
// carries only the other five.
func PickupItems(captureDate string, chops []Chop, drops []DroppedPlayer, roles []RoleChange) []PickupItem {
	var out []PickupItem
	for _, c := range chops {
		for _, p := range c.Players {
			out = append(out, PickupItem{
				Kind: "chop", Key: "chop-" + c.TransactionID + "-" + p.PlayerID, PlayerID: p.PlayerID,
				Line:     fmt.Sprintf("CHOP %s %s %s (%d) — %s eliminated", p.Name, p.Position, p.Team, p.Value, c.RosterName),
				Position: p.Position, Value: p.Value, Priced: true, ChopSize: len(c.Players),
			})
		}
	}
	for _, d := range drops {
		out = append(out, PickupItem{
			Kind: "drop", Key: "drop-" + d.TransactionID + "-" + d.PlayerID, PlayerID: d.PlayerID,
			Line:     fmt.Sprintf("DROP %s %s %s (%d) by %s", d.Name, d.Position, d.Team, d.Value, d.DroppedByName),
			Position: d.Position, Value: d.Value, Priced: true,
		})
	}
	for _, r := range roles {
		was := "was unlisted"
		switch {
		case r.PrevTeam != "":
			was = "was " + r.PrevTeam
		case r.PrevOrder > 0:
			was = fmt.Sprintf("was #%d", r.PrevOrder)
		}
		val := "unvalued"
		if r.Priced {
			val = fmt.Sprintf("%d", r.Value)
		}
		out = append(out, PickupItem{
			Kind: "role", Key: "role-" + r.PlayerID + "-" + captureDate, PlayerID: r.PlayerID,
			Line:     fmt.Sprintf("ROLE %s %s %s now #1 %s (%s) (%s)", r.Name, r.Position, r.Team, r.DepthChartPosition, was, val),
			Position: r.Position, Value: r.Value, Priced: r.Priced,
		})
	}
	return out
}

// pickupRank orders a league's digest: lower tier first, then higher value.
//
// OPERATOR-WRITTEN (spec Section 4: how a valued drop ranks against an
// unvalued role change is the operator's call). The operator chose the
// suggested default, which is what this implements:
//
//	tier 0  chop (the whole roster hit waivers at once; value order within)
//	tier 1  anything priced — drop or role change — by value
//	tier 2  unvalued role changes, a superflex QB first (value 1 vs 0)
//
// Alternatives considered and not taken: an unvalued QB role change in a
// superflex league outranking every drop (tier 0 with value 0), or fixed
// sections (chops, drops, roles) regardless of value.
func pickupRank(it PickupItem, superflex bool) (tier int, value int) {
	switch {
	case it.Kind == "chop":
		return 0, it.Value
	case it.Priced:
		return 1, it.Value
	case superflex && it.Position == "QB":
		return 2, 1
	default:
		return 2, 0
	}
}

// OrderPickups is a stable sort by pickupRank.
func OrderPickups(items []PickupItem, superflex bool) []PickupItem {
	out := append([]PickupItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		ti, vi := pickupRank(out[i], superflex)
		tj, vj := pickupRank(out[j], superflex)
		if ti != tj {
			return ti < tj
		}
		return vi > vj
	})
	return out
}

// chopTxn extracts the transaction id from a chop item's "chop-<txn>-<player>"
// key. It cuts rather than indexes so a hand-built item with a malformed key
// yields "" instead of panicking the digest.
func chopTxn(key string) string {
	_, rest, _ := strings.Cut(key, "-")
	txn, _, _ := strings.Cut(rest, "-")
	return txn
}

// FormatPickupDigest renders the title and a body that fits Pushover's
// limit on whole lines, and reports how many items it carried.
//
// shown is the contract the caller marks against: the digest carries exactly
// items[:shown], in the order given, so the caller marks those and leaves
// items[shown:] unmarked to be sent by a later digest. The fit STOPS at the
// first line that does not fit rather than skipping it and trying shorter
// ones — a skip-and-continue fit would carry a non-prefix subset, and "…and N
// more" would no longer mean the N lowest-ranked. When everything fits,
// shown == len(items) and there is no trailer; otherwise the body ends with
// "…and N more" for N == len(items)-shown.
func FormatPickupDigest(profile LeagueProfile, items []PickupItem) (title, body string, shown int) {
	var drops, roles int
	chopTxns := map[string]bool{}
	for _, it := range items {
		switch it.Kind {
		case "chop":
			// Distinct transactions, not players: a fourteen-player chop is
			// one elimination.
			chopTxns[chopTxn(it.Key)] = true
		case "drop":
			drops++
		case "role":
			roles++
		}
	}
	chops := len(chopTxns)
	title = fmt.Sprintf("[%s] Pickups: %d chop%s, %d drop%s, %d role change%s", profile.Name,
		chops, plural(chops), drops, plural(drops), roles, plural(roles))

	body, shown = fitLines(items, 0)
	if shown < len(items) {
		// The "…and N more" trailer needs room of its own, or appending it
		// would push a full body past the limit and leave Truncate to cut a
		// line mid-way. Size it for the worst case (every line left out) and
		// refit: the prefix can only shrink, and the count printed is the
		// count actually left out on this pass.
		body, shown = fitLines(items, len(moreTrailer(len(items))))
		body += moreTrailer(len(items) - shown)
	}
	return title, pushover.Truncate(body), shown
}

func moreTrailer(n int) string { return fmt.Sprintf("…and %d more", n) }

// fitLines adds each item's line, newline-terminated, while it fits within
// MaxMessageLen-reserve, and stops at the first line that does not. It
// returns the body and how many items it carried, which are exactly
// items[:shown]: it never skips a refused line to place a shorter,
// lower-ranked one after it, so the shown set is always a rank-order prefix.
func fitLines(items []PickupItem, reserve int) (body string, shown int) {
	var b pushover.Builder
	for _, it := range items {
		line := it.Line + "\n"
		if b.Len()+len(line) > pushover.MaxMessageLen-reserve || !b.Add(line) {
			break
		}
		shown++
	}
	return b.String(), shown
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
