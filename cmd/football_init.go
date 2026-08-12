package cmd

import (
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// FootballConfig holds dynasty-football configuration, read directly from
// env. Deliberately independent of internal/config.Config: config.Load fails
// outright without the four FANTRAX_* vars, and football commands must never
// need Fantrax credentials.
type FootballConfig struct {
	SleeperLeagueID          string
	DynastyFormat            string
	FootballPushoverUserKey  string
	FootballPushoverGroupKey string
}

func loadFootballConfig() (*FootballConfig, error) {
	leagueID := os.Getenv("SLEEPER_LEAGUE_ID")
	if leagueID == "" {
		return nil, fmt.Errorf("missing required env var: SLEEPER_LEAGUE_ID")
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
		DynastyFormat:            format,
		FootballPushoverUserKey:  userKey,
		FootballPushoverGroupKey: groupKey,
	}, nil
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
