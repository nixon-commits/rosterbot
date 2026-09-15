package fantrax

import (
	"errors"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

func stubPlayoffFetch(t *testing.T, fn func(*Client) (*auth_client.PlayoffBracket, error)) {
	t.Helper()
	orig := fetchPlayoffBracketFn
	fetchPlayoffBracketFn = fn
	t.Cleanup(func() { fetchPlayoffBracketFn = orig })
}

func oneRoundBracket() *auth_client.PlayoffBracket {
	return &auth_client.PlayoffBracket{Rounds: []auth_client.PlayoffRound{{
		Number: 1, Caption: "Round 1", ScoringPeriod: 23,
		Matchups: []auth_client.PlayoffMatchup{{
			Home: auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: "home1", TeamName: "Home"},
			Away: auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotBye},
		}},
	}}}
}

// The bracket is read through the file cache like every other Fantrax read:
// a second call in the same TTL window must not reach Fantrax, and what
// comes back from disk must carry the typed slot kinds intact.
func TestGetPlayoffBracket_ReadsThroughTheCache(t *testing.T) {
	c := &Client{leagueID: "lg1", cacheDir: t.TempDir()}
	calls := 0
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) {
		calls++
		return oneRoundBracket(), nil
	})

	first, err := c.GetPlayoffBracket()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := c.GetPlayoffBracket()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 1 {
		t.Errorf("fetched %d times, want 1 (second call should hit the cache)", calls)
	}
	for name, b := range map[string]*auth_client.PlayoffBracket{"first": first, "second": second} {
		if len(b.Rounds) != 1 || b.Rounds[0].ScoringPeriod != 23 {
			t.Errorf("%s bracket = %+v, want one round in period 23", name, b)
			continue
		}
		m := b.Rounds[0].Matchups[0]
		if m.Away.Kind != auth_client.PlayoffSlotBye || m.Home.TeamID != "home1" {
			t.Errorf("%s bracket lost slot typing through the cache: %+v", name, m)
		}
	}
}

// An empty cacheDir is the --no-cache / hermetic path: every call fetches.
func TestGetPlayoffBracket_NoCacheDirFetchesEveryTime(t *testing.T) {
	c := &Client{leagueID: "lg1", cacheDir: ""}
	calls := 0
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) {
		calls++
		return oneRoundBracket(), nil
	})
	for i := 0; i < 2; i++ {
		if _, err := c.GetPlayoffBracket(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Errorf("fetched %d times, want 2", calls)
	}
}

// A failed fetch is returned to the caller and must not poison the cache: the
// next call fetches again rather than serving a recorded failure or an empty
// bracket that would read as "no playoffs".
func TestGetPlayoffBracket_FetchErrorIsReturnedNotCached(t *testing.T) {
	c := &Client{leagueID: "lg1", cacheDir: t.TempDir()}
	calls := 0
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("fantrax down")
		}
		return oneRoundBracket(), nil
	})

	if _, err := c.GetPlayoffBracket(); err == nil {
		t.Fatal("first call: want the fetch error, got nil")
	}
	b, err := c.GetPlayoffBracket()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 2 || len(b.Rounds) != 1 {
		t.Errorf("after an error: fetched %d times (want 2), rounds %d (want 1)", calls, len(b.Rounds))
	}
}
