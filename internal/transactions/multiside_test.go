package transactions

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
	"github.com/pmurley/go-fantrax/models"
)

func testLookup() hkb.Lookup {
	return buildHKBLookup([]hkb.Player{
		{Name: "Alpha Ace", Value: 5000, Rank: 5, Level: "MLB"},
		{Name: "Beta Bat", Value: 3000, Rank: 40, Level: "MLB"},
		{Name: "Gamma Glove", Value: 1000, Rank: 300, Level: "AAA"},
		{Name: "Delta Depth", Value: 400, Rank: 700, Level: "AA"},
		{Name: "Epsilon Edge", Value: 2500, Rank: 60, Level: "MLB"},
	})
}

// A three-team trade used to lose a third of itself: both group builders
// filled a [2]TradeSide from a map under `if i < 2`, so one side was dropped
// at random and the report printed a confident total for the survivors. No
// three-team trade has happened in this league, which is precisely why the
// silent drop would never have been noticed.
func TestBuildTrade_ThreeTeamTradeKeepsEverySide(t *testing.T) {
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"},
		{TradeGroupID: "g1", PlayerName: "Beta Bat", ToTeamName: "Team B"},
		{TradeGroupID: "g1", PlayerName: "Gamma Glove", ToTeamName: "Team C"},
	}
	tr := buildTrade(group, testLookup(), nil)

	if len(tr.Sides) != 3 {
		t.Fatalf("got %d sides, want 3: %+v", len(tr.Sides), tr.Sides)
	}
	want := map[string]int{"Team A": 5000, "Team B": 3000, "Team C": 1000}
	for _, s := range tr.Sides {
		if want[s.TeamName] != s.Total {
			t.Errorf("%s total = %d, want %d", s.TeamName, s.Total, want[s.TeamName])
		}
	}

	// And the rendered report must name all three, not two of them.
	out := formatTrades("Recent Trades", []Trade{tr}, false)
	for team := range want {
		if !strings.Contains(out, team+" receives:") {
			t.Errorf("report omits %q:\n%s", team, out)
		}
	}
	if !strings.Contains(out, "Team A ⇄ Team B ⇄ Team C") {
		t.Errorf("header does not list all three sides:\n%s", out)
	}
}

// Sides came out of a map, so which team landed at index 0 -- and therefore
// which one the report printed first, and which direction the (±diff) pointed
// -- varied between runs on identical input. CLAUDE.md's idempotency invariant
// is that identical inputs produce identical output.
func TestBuildTrade_SideOrderIsStableAcrossRuns(t *testing.T) {
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Zulu"},
		{TradeGroupID: "g1", PlayerName: "Beta Bat", ToTeamName: "Alfa"},
		{TradeGroupID: "g1", PlayerName: "Gamma Glove", ToTeamName: "Mike"},
	}
	first := formatTrades("Recent Trades", []Trade{buildTrade(group, testLookup(), nil)}, false)
	for i := 0; i < 50; i++ {
		if got := formatTrades("Recent Trades", []Trade{buildTrade(group, testLookup(), nil)}, false); got != first {
			t.Fatalf("run %d differs from run 0:\n--- run 0 ---\n%s\n--- run %d ---\n%s", i, first, i, got)
		}
	}
	// Sorted by team name, so the order is predictable and not merely stable.
	sides := buildTrade(group, testLookup(), nil).Sides
	if sides[0].TeamName != "Alfa" || sides[1].TeamName != "Mike" || sides[2].TeamName != "Zulu" {
		t.Errorf("sides not name-ordered: %s, %s, %s", sides[0].TeamName, sides[1].TeamName, sides[2].TeamName)
	}
}

// Both sums agree here, so naming a winner is legitimate. This also pins the
// two totals themselves: Total stays the plain raw sum it always was (existing
// callers and tests depend on that), and Adjusted is the package-discounted
// figure.
func TestBuildTrade_ReportsRawAndAdjustedTotals(t *testing.T) {
	// Alpha 5000 alone vs Beta 3000 + Gamma 1000 + Delta 400 = 4400 raw.
	// Discounted: 5000 vs 3000 + .62(1000) + .60(400) = 3860 -- A leads on
	// both. (The discounted figure moved with rosterbot-492, which measured
	// the ladder against HKB's calculator; the verdict did not.)
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"},   // 5000
		{TradeGroupID: "g1", PlayerName: "Beta Bat", ToTeamName: "Team B"},    // 3000
		{TradeGroupID: "g1", PlayerName: "Gamma Glove", ToTeamName: "Team B"}, // 1000
		{TradeGroupID: "g1", PlayerName: "Delta Depth", ToTeamName: "Team B"}, // 400
	}
	tr := buildTrade(group, testLookup(), nil)
	byName := map[string]TradeSide{}
	for _, s := range tr.Sides {
		byName[s.TeamName] = s
	}
	if got := byName["Team B"].Total; got != 4400 {
		t.Fatalf("Team B raw = %d, want 4400", got)
	}
	// 3000 + 0.62*1000 + 0.60*400 = 3860
	if got := byName["Team B"].Adjusted; got < 3859 || got > 3861 {
		t.Errorf("Team B adjusted = %.1f, want ~3860", got)
	}
	// Single asset: adjusted == raw by construction.
	if byName["Team A"].Adjusted != float64(byName["Team A"].Total) {
		t.Errorf("one-asset side adjusted %.1f != raw %d", byName["Team A"].Adjusted, byName["Team A"].Total)
	}
	// Both methods put Team A ahead here, so a verdict is legitimate.
	if tr.Verdict.Status != tradevalue.StatusFavors || tr.Verdict.FavoredTeam != "Team A" {
		t.Errorf("verdict = %+v, want favors Team A", tr.Verdict)
	}
	if !strings.Contains(formatTrades("h", []Trade{tr}, false), "→ favors Team A") {
		t.Error("report does not carry the verdict line")
	}
}

// The whole point of the migration. The old report summed raw HKB values and
// named a winner; on a package-for-one where the decayed sum names the OTHER
// side, that confident answer was as likely to be backwards as right. This is
// the shape of the live offer that motivated the Trades tab (Gage Jump +
// Emmet Sheehan for Kyle Harrison), and the report must now decline the call
// exactly as the tab does.
func TestBuildTrade_WithholdsTheCallWhenTheTwoMethodsDisagree(t *testing.T) {
	group := []models.Transaction{
		// Raw: B leads 5500 to 5000. Adjusted: 3000 + .62(2500) = 4550, so A
		// leads 5000 to 4550. Opposite winners.
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"},    // 5000
		{TradeGroupID: "g1", PlayerName: "Beta Bat", ToTeamName: "Team B"},     // 3000
		{TradeGroupID: "g1", PlayerName: "Epsilon Edge", ToTeamName: "Team B"}, // 2500
	}
	tr := buildTrade(group, testLookup(), nil)

	if tr.Verdict.Status != tradevalue.StatusTooClose {
		t.Fatalf("verdict = %+v, want too-close", tr.Verdict)
	}
	if tr.Verdict.RawLeader != "Team B" || tr.Verdict.AdjLeader != "Team A" {
		t.Errorf("leaders = raw %q / adj %q, want raw Team B / adj Team A",
			tr.Verdict.RawLeader, tr.Verdict.AdjLeader)
	}
	if tr.Verdict.FavoredTeam != "" {
		t.Errorf("FavoredTeam = %q, want empty — no side earns the call", tr.Verdict.FavoredTeam)
	}

	out := formatTrades("h", []Trade{tr}, false)
	if !strings.Contains(out, "too close to call") {
		t.Errorf("report names a winner on a disagreement:\n%s", out)
	}
	// The multi-asset side must show both numbers; the single-asset side must
	// not, since for it they are equal by construction.
	if !strings.Contains(out, "adj 4,550") {
		t.Errorf("package side does not show its adjusted total:\n%s", out)
	}
	if strings.Count(out, "adj ") != 1 {
		t.Errorf("expected exactly one adjusted total (the package side):\n%s", out)
	}
}

// A draft pick arrives as a row with an empty player name. It used to render
// as a nameless bullet worth nothing; HKB prices picks up to 1419, so a total
// computed without one is not a total. The verdict is suppressed rather than
// computed from the priced remainder.
func TestBuildTrade_DraftPickIsNamedAndSuppressesTheVerdict(t *testing.T) {
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"},
		{TradeGroupID: "g1", PlayerName: "", ToTeamName: "Team B"},
	}
	tr := buildTrade(group, testLookup(), nil)

	if tr.Verdict.Status != tradevalue.StatusIncomplete {
		t.Errorf("verdict = %q, want incomplete", tr.Verdict.Status)
	}
	if tr.Verdict.UnpricedAssets != 1 {
		t.Errorf("UnpricedAssets = %d, want 1", tr.Verdict.UnpricedAssets)
	}
	out := formatTrades("h", []Trade{tr}, false)
	if !strings.Contains(out, "Draft pick (unidentified)") {
		t.Errorf("pick is not named in the report:\n%s", out)
	}
	if !strings.Contains(out, "no verdict") {
		t.Errorf("report states a verdict despite an unpriced asset:\n%s", out)
	}
}

// Two-sided trades keep the pairwise "(±diff)" they always had; three-sided
// ones must not, because there is no single "other" side to diff against.
func TestFormatTrades_PairwiseDiffOnlyAppearsForTwoSides(t *testing.T) {
	two := buildTrade([]models.Transaction{
		{TradeGroupID: "g", PlayerName: "Alpha Ace", ToTeamName: "Team A"},
		{TradeGroupID: "g", PlayerName: "Beta Bat", ToTeamName: "Team B"},
	}, testLookup(), nil)
	if out := formatTrades("h", []Trade{two}, false); !strings.Contains(out, "(+2,000)") {
		t.Errorf("two-sided trade lost its diff:\n%s", out)
	}

	three := buildTrade([]models.Transaction{
		{TradeGroupID: "g", PlayerName: "Alpha Ace", ToTeamName: "Team A"},
		{TradeGroupID: "g", PlayerName: "Beta Bat", ToTeamName: "Team B"},
		{TradeGroupID: "g", PlayerName: "Gamma Glove", ToTeamName: "Team C"},
	}, testLookup(), nil)
	out := formatTrades("h", []Trade{three}, false)
	if strings.Contains(out, "(+") || strings.Contains(out, "(-") {
		t.Errorf("three-sided trade shows a meaningless pairwise diff:\n%s", out)
	}
}

func testPickHKBPlayers() []hkb.Player {
	return []hkb.Player{
		{Name: "2027 Early 1st", AssetType: "PICK", Value: 1456},
		{Name: "2027 Mid 1st", AssetType: "PICK", Value: 821},
		{Name: "2027 Late 1st", AssetType: "PICK", Value: 778},
	}
}

// rosterbot-uc3: a pick row with IsDraftPick set gets a recovered name and,
// when HKB still lists the year+round, a priced value -- so the verdict
// fires instead of being suppressed the way an unidentified pick used to
// suppress it unconditionally.
func TestBuildTrade_IdentifiedDraftPick_IsNamedAndPricedAndVerdictFires(t *testing.T) {
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"}, // 5000
		{
			TradeGroupID:          "g1",
			ToTeamName:            "Team B",
			IsDraftPick:           true,
			DraftPickYear:         2027,
			DraftPickRound:        1,
			DraftPickOriginalTeam: "Houston Swang and Bang",
		},
	}
	tr := buildTrade(group, testLookup(), testPickHKBPlayers())

	var teamB TradeSide
	for _, s := range tr.Sides {
		if s.TeamName == "Team B" {
			teamB = s
		}
	}
	if len(teamB.Players) != 1 || !teamB.Players[0].IsPick {
		t.Fatalf("Team B side = %+v, want one IsPick player", teamB)
	}
	pick := teamB.Players[0]
	wantAvg := (1456 + 821 + 778) / 3
	if !pick.Ranked || pick.Value != wantAvg || !pick.Estimated {
		t.Errorf("pick = %+v, want Ranked, Value %d, Estimated", pick, wantAvg)
	}
	if pick.Name != "2027 1st Round Pick (Houston Swang and Bang)" {
		t.Errorf("pick.Name = %q", pick.Name)
	}
	if teamB.Total != wantAvg {
		t.Errorf("Team B total = %d, want %d", teamB.Total, wantAvg)
	}
	if tr.Verdict.Status == tradevalue.StatusIncomplete {
		t.Errorf("verdict = incomplete, want a verdict now that the pick is priced: %+v", tr.Verdict)
	}

	out := formatTrades("h", []Trade{tr}, false)
	if !strings.Contains(out, "2027 1st Round Pick (Houston Swang and Bang)") {
		t.Errorf("report does not name the pick:\n%s", out)
	}
	if !strings.Contains(out, "avg of Early/Mid/Late") {
		t.Errorf("report does not flag the estimate:\n%s", out)
	}
}

// A pick from a draft that has already happened (a resolved slot number, no
// original team) is still identified and named, but HKB no longer lists it
// as an upcoming asset, so it stays unpriced and keeps suppressing the
// verdict -- recovering identity does not fabricate a price where none
// exists.
func TestBuildTrade_ResolvedDraftPick_NamedButStillUnpriced(t *testing.T) {
	group := []models.Transaction{
		{TradeGroupID: "g1", PlayerName: "Alpha Ace", ToTeamName: "Team A"},
		{
			TradeGroupID:    "g1",
			ToTeamName:      "Team B",
			IsDraftPick:     true,
			DraftPickYear:   2026,
			DraftPickRound:  1,
			DraftPickNumber: 6,
		},
	}
	tr := buildTrade(group, testLookup(), testPickHKBPlayers())

	if tr.Verdict.Status != tradevalue.StatusIncomplete {
		t.Errorf("verdict = %q, want incomplete", tr.Verdict.Status)
	}
	out := formatTrades("h", []Trade{tr}, false)
	if !strings.Contains(out, "2026 1st Round Pick (Pick 6)") {
		t.Errorf("report does not name the resolved pick:\n%s", out)
	}
}
