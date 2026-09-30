package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/sleeperauth"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

var offerNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// offer builds a proposed trade to roster 8 (the operator) from roster 3.
func offer(id string, mutate func(*sleeper.Transaction)) sleeper.Transaction {
	t := sleeper.Transaction{
		TransactionID: id, Type: "trade", Status: "proposed", RosterIDs: []int{3, 8},
		Adds: map[string]int{"4984": 8, "9509": 3}, Drops: map[string]int{"4984": 3, "9509": 8},
		Created: offerNow.Add(-2 * time.Hour).UnixMilli(), Creator: "user-3", ConsenterIDs: []int{3},
	}
	if mutate != nil {
		mutate(&t)
	}
	return t
}

func TestSelectOffers_KeepsOnlyLiveOffersToMeAwaitingMe(t *testing.T) {
	in := []sleeper.Transaction{
		offer("keep", nil),
		offer("mine", func(x *sleeper.Transaction) { x.Creator = "me" }),                 // I sent it (C, deferred)
		offer("waiver", func(x *sleeper.Transaction) { x.Type = "waiver" }),              // a pending claim
		offer("done", func(x *sleeper.Transaction) { x.Status = "complete" }),            // not live
		offer("notme", func(x *sleeper.Transaction) { x.RosterIDs = []int{3, 5} }),       // not my roster
		offer("answered", func(x *sleeper.Transaction) { x.ConsenterIDs = []int{3, 8} }), // I already accepted
		offer("expired", func(x *sleeper.Transaction) {
			x.Settings = map[string]any{"expires_at": float64(offerNow.Add(-time.Minute).Unix())}
		}),
		offer("fresh", func(x *sleeper.Transaction) {
			x.Settings = map[string]any{"expires_at": float64(offerNow.Add(time.Hour).Unix())}
		}),
	}
	keep, expired, answered := selectOffers(in, 8, "me", offerNow)
	var ids []string
	for _, k := range keep {
		ids = append(ids, k.TransactionID)
	}
	if strings.Join(ids, ",") != "keep,fresh" {
		t.Errorf("kept %v, want keep,fresh", ids)
	}
	if expired != 1 || answered != 1 {
		t.Errorf("expired=%d answered=%d, want 1 and 1", expired, answered)
	}
}

func offerFixture(t *testing.T) (offerRunInputs, *[]string) {
	t.Helper()
	sent := &[]string{}
	return offerRunInputs{
		markers:  lineupapi.NewFileBlobStore(t.TempDir(), ""),
		now:      offerNow,
		offers:   []sleeper.Transaction{offer("o1", nil)},
		myRoster: 8,
		players: map[string]sleeper.Player{
			"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"},
			"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson"},
		},
		bundle: &statsguy.Bundle{Players: map[string]statsguy.Player{
			"4984": {ID: "4984", Value: statsguy.FormatValues{SFDynasty: 9000}},
			"9509": {ID: "9509", Value: statsguy.FormatValues{SFDynasty: 11000}},
		}},
		names:  map[int]string{3: "CeeDee Top", 8: "Tha Mobb"},
		league: dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees", Format: "sf_dynasty"},
		send:   func(title, body string) error { *sent = append(*sent, title+"\n"+body); return nil },
		out:    io.Discard,
	}, sent
}

func TestGradeAndAlertOffers_AlertsFromMySideAndMarks(t *testing.T) {
	in, sent := offerFixture(t)
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Graded != 1 || res.Alerted != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v sent=%d", res, len(*sent))
	}
	msg := (*sent)[0]
	// I receive Allen (9000) and give Robinson (11000): the offer favors THEM.
	if !strings.HasPrefix(msg, "[Palm Trees] Offer from CeeDee Top: favors CeeDee Top") {
		t.Errorf("title = %q", strings.SplitN(msg, "\n", 2)[0])
	}
	if !strings.Contains(msg, "You get: Josh Allen (9000)") || !strings.Contains(msg, "You give: Bijan Robinson (11000)") {
		t.Errorf("body = %q", msg)
	}
	if !strings.Contains(msg, "waiting on you") {
		t.Errorf("body lacks the consent state: %q", msg)
	}
	if _, found, err := in.markers.Get(context.Background(), "o1"); err != nil || !found {
		t.Errorf("marker not written: found=%v err=%v", found, err)
	}
}

func TestGradeAndAlertOffers_FavorsYouWhenIComeOutAhead(t *testing.T) {
	in, sent := offerFixture(t)
	in.offers[0].Adds = map[string]int{"9509": 8, "4984": 3} // now I get Robinson
	in.offers[0].Drops = map[string]int{"9509": 3, "4984": 8}
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "favors you (+") {
		t.Errorf("sent = %v", *sent)
	}
}

func TestGradeAndAlertOffers_ExpiryIsNamed(t *testing.T) {
	in, sent := offerFixture(t)
	in.offers[0].Settings = map[string]any{"expires_at": float64(offerNow.Add(26 * time.Hour).Unix())}
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "expires 2026-10-01 14:00 UTC") {
		t.Errorf("sent = %v", *sent)
	}
}

func TestGradeAndAlertOffers_DryRunSendsAndMarksNothing(t *testing.T) {
	in, sent := offerFixture(t)
	in.dryRun = true
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Graded != 1 || res.Alerted != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "o1"); found {
		t.Error("dry-run wrote a marker")
	}
}

func TestGradeAndAlertOffers_FailedSendIsNotMarked(t *testing.T) {
	in, _ := offerFixture(t)
	in.send = func(string, string) error { return errors.New("apns down") }
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Alerted != 0 {
		t.Errorf("res=%+v", res)
	}
	if _, found, _ := in.markers.Get(context.Background(), "o1"); found {
		t.Error("a failed send must not mark: the next poll has to retry")
	}
}

func TestGradeAndAlertOffers_MarkedOfferIsSkippedBeforeGrading(t *testing.T) {
	in, sent := offerFixture(t)
	if err := in.markers.Publish("o1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Skipped != 1 || res.Graded != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestGradeAndAlertOffers_NilMarkersStillSends(t *testing.T) {
	in, sent := offerFixture(t)
	in.markers = nil
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 {
		t.Errorf("sent = %d", len(*sent))
	}
}

func TestFormatOfferAlert_ThreeTeamOfferNamesEachGiver(t *testing.T) {
	in, _ := offerFixture(t)
	txn := offer("o3", func(x *sleeper.Transaction) {
		x.RosterIDs = []int{3, 5, 8}
		x.Adds = map[string]int{"4984": 8, "9509": 5}
		x.Drops = map[string]int{"4984": 3, "9509": 8}
	})
	in.names[5] = "Ghost Riders"
	sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.league.Format)
	title, body := formatOfferAlert(in.league, 8, txn, sides, dynasty.GradeTrade(sides))
	if !strings.HasPrefix(title, "[Palm Trees] Offer (3 teams)") {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(body, "Ghost Riders gets: Bijan Robinson") {
		t.Errorf("body = %q", body)
	}
}

func TestFormatOfferAlert_ConsentStateCopy(t *testing.T) {
	in, _ := offerFixture(t)

	// Two teams: ConsenterIDs [3] is just the proposer's own consent, so
	// "1 of 2 accepted" would be true of every offer and say nothing.
	two := offer("o2", nil)
	sides := dynasty.BuildTradeSides(two, in.players, in.bundle, in.names, in.league.Format)
	_, body := formatOfferAlert(in.league, 8, two, sides, dynasty.GradeTrade(sides))
	if !strings.HasSuffix(body, " | waiting on you") || strings.Contains(body, "1 of 2") || strings.Contains(body, "accepted") {
		t.Errorf("two-team body = %q, want a bare \"waiting on you\"", body)
	}

	// Three teams: a count is information.
	three := offer("o3", func(x *sleeper.Transaction) {
		x.RosterIDs = []int{3, 5, 8}
		x.Adds = map[string]int{"4984": 8, "9509": 5}
		x.Drops = map[string]int{"4984": 3, "9509": 8}
	})
	in.names[5] = "Ghost Riders"
	sides = dynasty.BuildTradeSides(three, in.players, in.bundle, in.names, in.league.Format)
	_, body = formatOfferAlert(in.league, 8, three, sides, dynasty.GradeTrade(sides))
	if !strings.HasSuffix(body, "1 of 3 accepted, waiting on you") {
		t.Errorf("three-team body = %q, want \"1 of 3 accepted, waiting on you\"", body)
	}
}

func TestVerifyOfferIdentity_MismatchNamesBothIDs(t *testing.T) {
	if err := verifyOfferIdentity("738883211463155712", "738883211463155712"); err != nil {
		t.Errorf("matching ids: %v", err)
	}
	err := verifyOfferIdentity("111", "222")
	if err == nil {
		t.Fatal("a token for another account must fail the run")
	}
	if !strings.Contains(err.Error(), "111") || !strings.Contains(err.Error(), "222") {
		t.Errorf("error %q must name both ids", err)
	}
}

func pollLeague(id, name string) leagueContext {
	return leagueContext{
		League:  sleeper.League{LeagueID: id, Name: name},
		Profile: dynasty.LeagueProfile{LeagueID: id, Name: name, Format: "sf_dynasty"},
	}
}

// pollHarness records which leagues were loaded and run, and with what.
type pollHarness struct {
	loaded []string
	ran    []string
	keeps  map[string]int
}

func (h *pollHarness) run(lc leagueContext, _ leagueOffers, keep []sleeper.Transaction) offerRunResult {
	h.ran = append(h.ran, lc.League.LeagueID)
	if h.keeps == nil {
		h.keeps = map[string]int{}
	}
	h.keeps[lc.League.LeagueID] = len(keep)
	return offerRunResult{Graded: len(keep), Alerted: len(keep)}
}

func TestPollOffers_UnauthorizedAbortsTheRun(t *testing.T) {
	h := &pollHarness{}
	leagues := []leagueContext{pollLeague("A", "Alpha"), pollLeague("B", "Bravo")}
	var out bytes.Buffer
	_, _, _, err := pollOffers(context.Background(), leagues, "me", offerNow,
		func(_ context.Context, lc leagueContext) (leagueOffers, error) {
			h.loaded = append(h.loaded, lc.League.LeagueID)
			return leagueOffers{}, sleeperauth.ErrUnauthorized
		}, h.run, &out)
	if !errors.Is(err, sleeperauth.ErrUnauthorized) {
		t.Fatalf("err = %v, want it to wrap ErrUnauthorized", err)
	}
	if strings.Join(h.loaded, ",") != "A" {
		t.Errorf("loaded %v: a rejected token must stop the run before league B is touched", h.loaded)
	}
	if len(h.ran) != 0 {
		t.Errorf("runner called for %v", h.ran)
	}
}

func TestPollOffers_OneFailingLeagueDoesNotStopTheOthers(t *testing.T) {
	h := &pollHarness{}
	leagues := []leagueContext{pollLeague("A", "Alpha"), pollLeague("B", "Bravo"), pollLeague("C", "Charlie")}
	var out bytes.Buffer
	total, found, failed, err := pollOffers(context.Background(), leagues, "me", offerNow,
		func(_ context.Context, lc leagueContext) (leagueOffers, error) {
			if lc.League.LeagueID == "B" {
				return leagueOffers{}, errors.New("sleeper 502")
			}
			return leagueOffers{myRoster: 8, offers: []sleeper.Transaction{offer("o-"+lc.League.LeagueID, nil)}}, nil
		}, h.run, &out)
	if err != nil {
		t.Fatalf("err = %v, want nil: a plain league failure is reported through failed", err)
	}
	if strings.Join(h.ran, ",") != "A,C" {
		t.Errorf("runner called for %v, want A,C", h.ran)
	}
	if strings.Join(failed, ",") != "Bravo" {
		t.Errorf("failed = %v, want [Bravo] (league names, not ids)", failed)
	}
	if found != 2 {
		t.Errorf("found = %d, want 2", found)
	}
	if total.Graded != 2 || total.Alerted != 2 {
		t.Errorf("total = %+v, want the healthy leagues' results accumulated", total)
	}
}

func TestPollOffers_NoRosterIsSkippedAndSaid(t *testing.T) {
	h := &pollHarness{}
	var out bytes.Buffer
	_, found, failed, err := pollOffers(context.Background(), []leagueContext{pollLeague("A", "Alpha")}, "me", offerNow,
		func(context.Context, leagueContext) (leagueOffers, error) { return leagueOffers{myRoster: 0}, nil },
		h.run, &out)
	if err != nil || len(failed) != 0 || found != 0 {
		t.Errorf("err=%v failed=%v found=%d", err, failed, found)
	}
	if len(h.ran) != 0 {
		t.Errorf("runner called for %v: there is nothing to grade without a roster", h.ran)
	}
	if !strings.Contains(out.String(), "no roster owned by me") {
		t.Errorf("out = %q, want the skip line", out.String())
	}
}

func TestPollOffers_CoverageLinePrintsTheZeroCase(t *testing.T) {
	h := &pollHarness{}
	var out bytes.Buffer
	_, found, _, err := pollOffers(context.Background(), []leagueContext{pollLeague("A", "Alpha")}, "me", offerNow,
		func(context.Context, leagueContext) (leagueOffers, error) { return leagueOffers{myRoster: 8}, nil },
		h.run, &out)
	if err != nil || found != 0 {
		t.Errorf("err=%v found=%d", err, found)
	}
	for _, want := range []string{"Alpha", "0 proposed for roster 8", "0 to me and live"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("out = %q, missing %q", out.String(), want)
		}
	}
	// The runner IS called with an empty keep: a league with nothing live still
	// flows through the same path, and is what makes the zero case a counted
	// result rather than a special branch.
	if strings.Join(h.ran, ",") != "A" || h.keeps["A"] != 0 {
		t.Errorf("ran=%v keeps=%v, want runner called once for A with an empty keep", h.ran, h.keeps)
	}
}
