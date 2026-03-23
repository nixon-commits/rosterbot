package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Username string
	Password string
	LeagueID string
	TeamID   string
	DryRun   bool
	Date     time.Time
}

func Load(dryRun bool, date time.Time) (*Config, error) {
	// Load .env if present (local dev); ignore error if missing (GHA uses env directly)
	_ = godotenv.Load()

	cfg := &Config{
		Username: os.Getenv("FANTRAX_USERNAME"),
		Password: os.Getenv("FANTRAX_PASSWORD"),
		LeagueID: os.Getenv("FANTRAX_LEAGUE_ID"),
		TeamID:   os.Getenv("FANTRAX_TEAM_ID"),
		DryRun:   dryRun,
		Date:     date,
	}

	var missing []string
	for name, val := range map[string]string{
		"FANTRAX_USERNAME":  cfg.Username,
		"FANTRAX_PASSWORD":  cfg.Password,
		"FANTRAX_LEAGUE_ID": cfg.LeagueID,
		"FANTRAX_TEAM_ID":   cfg.TeamID,
	} {
		if val == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}

	return cfg, nil
}
