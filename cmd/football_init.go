package cmd

import (
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// FootballConfig holds dynasty-football configuration, read directly from
// env. Deliberately independent of internal/config.Config: config.Load fails
// outright without the four FANTRAX_* vars, and football commands must never
// need Fantrax credentials.
type FootballConfig struct {
	// SleeperLeagueID is the single league football-values reads. Optional at
	// load: the multi-league jobs (football-trades, offers, pickups) discover
	// their leagues from SleeperUserID and never read it, so only
	// football-values requires it (requireSleeperLeagueID).
	SleeperLeagueID string
	// SleeperUserID is the operator's Sleeper account id — stable where a
	// username is not. Optional at load so football-values keeps working
	// without it; the jobs that need it call requireSleeperUserID.
	SleeperUserID string
	// FormatOverrides is SLEEPER_FORMAT_OVERRIDES parsed: league id → StatsGuy
	// column, applied by dynasty.DeriveProfile over its derivation.
	FormatOverrides          map[string]string
	DynastyFormat            string
	FootballPushoverUserKey  string
	FootballPushoverGroupKey string
}

func loadFootballConfig() (*FootballConfig, error) {
	leagueID := os.Getenv("SLEEPER_LEAGUE_ID")
	overrides, err := dynasty.ParseFormatOverrides(os.Getenv("SLEEPER_FORMAT_OVERRIDES"))
	if err != nil {
		return nil, err
	}
	format := os.Getenv("DYNASTY_FORMAT")
	if format == "" {
		format = "sf_dynasty"
	}
	userKey := os.Getenv("FOOTBALL_PUSHOVER_USER_KEY")
	if userKey == "" {
		userKey = os.Getenv("PUSHOVER_USER_KEY")
	}
	groupKey := os.Getenv("FOOTBALL_PUSHOVER_GROUP_KEY")
	if groupKey == "" {
		groupKey = os.Getenv("PUSHOVER_GROUP_KEY")
	}
	return &FootballConfig{
		SleeperLeagueID:          leagueID,
		SleeperUserID:            os.Getenv("SLEEPER_USER_ID"),
		FormatOverrides:          overrides,
		DynastyFormat:            format,
		FootballPushoverUserKey:  userKey,
		FootballPushoverGroupKey: groupKey,
	}, nil
}

// requireSleeperUserID is the check every multi-league job runs first. Kept
// off loadFootballConfig so a deployment that has not yet set the parameter
// breaks the new jobs loudly and leaves football-values alone.
func (c *FootballConfig) requireSleeperUserID() error {
	if c.SleeperUserID == "" {
		return fmt.Errorf("missing required env var: SLEEPER_USER_ID (the operator's Sleeper account id; this job discovers leagues from it)")
	}
	return nil
}

// requireSleeperLeagueID is football-values' check: it is the one command
// that reads a single configured league. The multi-league jobs discover
// theirs from SLEEPER_USER_ID and must not fail over a variable they never
// use.
func (c *FootballConfig) requireSleeperLeagueID() error {
	if c.SleeperLeagueID == "" {
		return fmt.Errorf("missing required env var: SLEEPER_LEAGUE_ID (the league football-values reads)")
	}
	return nil
}

// initFootball loads football configuration and creates a Sleeper client,
// sharing the sport-agnostic middle (cache store, cache-notify, activity
// recorders) with initApp via initShared. Unlike initApp, this never touches
// internal/config or internal/fantrax, so it needs no FANTRAX_* env vars.
func initFootball() (*FootballConfig, *sleeper.Client, error) {
	if err := initShared(); err != nil {
		return nil, nil, err
	}
	cfg, err := loadFootballConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("football config: %w", err)
	}
	sc := sleeper.NewClient()
	if !noCache {
		sc.CacheDir = cacheDir
	}
	return cfg, sc, nil
}
