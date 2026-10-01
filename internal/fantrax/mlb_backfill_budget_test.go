package fantrax

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
)

// backfillDaysForNames builds one day holding n distinct players needing
// backfill, and the resolver mapping each to its own MLBAM id.
func backfillDaysForNames(t *testing.T, date time.Time, n int) []DayRoster {
	t.Helper()
	byName, byID := map[string]int{}, map[int]string{}
	var players []DayPlayerFP
	for i := 0; i < n; i++ {
		name := "Player " + string(rune('A'+i))
		byName[playername.Normalize(name)] = 100000 + i
		byID[100000+i] = name
		players = append(players, DayPlayerFP{PlayerID: name, Name: name, Active: true, needsBackfill: true})
	}
	prev := resolveBackfillNames
	resolveBackfillNames = func([]string, string) (*playername.ResolvedPlayers, error) {
		return &playername.ResolvedPlayers{ByName: byName, ByID: byID}, nil
	}
	t.Cleanup(func() { resolveBackfillNames = prev })
	return []DayRoster{{Date: date, Players: players}}
}

// shrinkBackfillBackoff keeps these tests fast; the retry policy itself is
// covered by TestFetchMLBGameLogUncached_RetriesPastAStalledAttempt.
func shrinkBackfillBackoff(t *testing.T) {
	t.Helper()
	prev := mlbBackfillBackoff
	mlbBackfillBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { mlbBackfillBackoff = prev })
}

// The timeout bounds a request and the retry makes one blip survivable, but
// neither bounds the RUN. Against a persistently dead statsapi every fetch now
// costs the full attempt budget (~50s in production), and a render makes ~600
// of them — hours of grinding that ends in a useless render, which is the
// rosterbot-5zp1 hang wearing a third hat. A run of EXHAUSTED fetches means the
// endpoint is gone, so the pass stops and says so.
func TestBackfillDailyFPts_ConsecutiveExhaustionAbandonsThePass(t *testing.T) {
	shrinkBackfillBackoff(t)
	dir, leagueID := t.TempDir(), "TESTLG"
	writeScoringCache(t, dir, leagueID, ScoringWeights{"1B": 1}, ScoringWeights{})

	var requests int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadGateway) // 5xx: retryable, so it exhausts
	}))
	defer dead.Close()
	prevURL := mlbBackfillGameLogURL
	mlbBackfillGameLogURL = dead.URL + "/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"
	defer func() { mlbBackfillGameLogURL = prevURL }()

	date, _ := time.Parse("2006-01-02", "2026-09-16")
	extra := 20
	days := backfillDaysForNames(t, date, mlbBackfillFailureBudget+extra)

	c := newTestBackfillClient(t, dir, leagueID)
	stats, err := c.backfillDailyFPts(days)

	if err == nil {
		t.Fatal("a dead statsapi produced no error; the pass must stop rather than grind through every player")
	}
	attemptsPerFetch := len(mlbBackfillBackoff) + 1
	if max := mlbBackfillFailureBudget * attemptsPerFetch; requests > max {
		t.Errorf("made %d requests, want at most %d — the budget did not stop the pass", requests, max)
	}
	if stats.Abandoned != extra {
		t.Errorf("Abandoned = %d, want %d player-days never attempted", stats.Abandoned, extra)
	}
	if !strings.Contains(stats.String(), "abandoned") {
		t.Errorf("stats line %q does not report the abandoned work", stats.String())
	}
}

// A durable 4xx is an unknown player, NOT an outage: statsapi answers one
// immediately and no number of attempts changes it. A roster carrying several
// such players in a row must not be read as statsapi being down — that would
// fail a render over a data problem, so only EXHAUSTED failures count.
func TestBackfillDailyFPts_DurableNotFoundNeverTripsTheBudget(t *testing.T) {
	shrinkBackfillBackoff(t)
	dir, leagueID := t.TempDir(), "TESTLG"
	writeScoringCache(t, dir, leagueID, ScoringWeights{"1B": 1}, ScoringWeights{})

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // durable: returns on the first attempt
	}))
	defer notFound.Close()
	prevURL := mlbBackfillGameLogURL
	mlbBackfillGameLogURL = notFound.URL + "/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"
	defer func() { mlbBackfillGameLogURL = prevURL }()

	date, _ := time.Parse("2006-01-02", "2026-09-16")
	total := mlbBackfillFailureBudget * 3
	days := backfillDaysForNames(t, date, total)

	c := newTestBackfillClient(t, dir, leagueID)
	stats, err := c.backfillDailyFPts(days)

	if err != nil {
		t.Fatalf("durable 404s failed the pass: %v — unknown players are a data problem, not an outage", err)
	}
	if stats.Abandoned != 0 {
		t.Errorf("Abandoned = %d, want 0", stats.Abandoned)
	}
	if stats.FetchFailed != total {
		t.Errorf("FetchFailed = %d, want %d — every player should still be counted", stats.FetchFailed, total)
	}
}
