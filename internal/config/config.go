package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// envIntWithFallback reads primary env var first, then falls back to deprecated.
// Prints a warning to stderr if the deprecated var is used.
func envIntWithFallback(primary, deprecated string, fallback int) int {
	if s := os.Getenv(primary); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	if s := os.Getenv(deprecated); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			fmt.Fprintf(os.Stderr, "WARNING: %s is deprecated, use %s instead\n", deprecated, primary)
			return v
		}
	}
	return fallback
}

type Config struct {
	Username    string
	Password    string
	LeagueID    string
	TeamID      string
	DryRun      bool
	Dates       []time.Time
	ILSlots     int
	MinorsSlots int
	GSMax       int // max game starts per matchup week (0 = no limit); also used by gs-check
	GSMin       int // min game starts per matchup week (0 = no minimum); used by gs-check
	BlendMinGP  int // min games played before blending recent stats (default 2)

	// Prospect report settings (all optional, with defaults).
	ProspectRollingDays    int
	ProspectMinGames       int
	ProspectRankCacheHours int
	ProspectRankThreshold  int

	// GS-check settings (all optional; validated by the gs-check command).
	PushoverUserKey  string // Pushover user key for push notifications
	PushoverGroupKey string // Pushover group key for GS violation alerts
	PushoverAPIToken string // Pushover app API token
}

func Load(dryRun bool, dates []time.Time) (*Config, error) {
	// Load .env if present (local dev); ignore error if missing (GHA uses env directly)
	_ = godotenv.Load()

	cfg := &Config{
		Username:    os.Getenv("FANTRAX_USERNAME"),
		Password:    os.Getenv("FANTRAX_PASSWORD"),
		LeagueID:    os.Getenv("FANTRAX_LEAGUE_ID"),
		TeamID:      os.Getenv("FANTRAX_TEAM_ID"),
		DryRun:      dryRun,
		Dates:       dates,
		ILSlots:     envInt("FANTRAX_IL_SLOTS", 0),
		MinorsSlots: envInt("FANTRAX_MINORS_SLOTS", 0),
		GSMax:       envIntWithFallback("GS_MAX", "GS_CAP", 0),
		GSMin:       envInt("GS_MIN", 0),
		BlendMinGP:  envInt("BLEND_MIN_GP", 2),

		ProspectRollingDays:    envInt("PROSPECT_ROLLING_DAYS", 14),
		ProspectMinGames:       envInt("PROSPECT_MIN_GAMES", 8),
		ProspectRankCacheHours: envInt("PROSPECT_RANK_CACHE_HOURS", 168),
		ProspectRankThreshold:  envInt("PROSPECT_UPGRADE_RANK_THRESHOLD", 20),

		PushoverUserKey:  os.Getenv("PUSHOVER_USER_KEY"),
		PushoverGroupKey: os.Getenv("PUSHOVER_GROUP_KEY"),
		PushoverAPIToken: os.Getenv("PUSHOVER_API_TOKEN"),
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

func envInt(key string, fallback int) int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}
