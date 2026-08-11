package cmd

import "testing"

func TestLoadFootballConfigRequiresSleeperLeagueID(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "")
	if _, err := loadFootballConfig(); err == nil {
		t.Fatal("loadFootballConfig: expected error when SLEEPER_LEAGUE_ID unset, got nil")
	}
}

func TestLoadFootballConfigDefaultsDynastyFormat(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")
	t.Setenv("DYNASTY_FORMAT", "")

	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatalf("loadFootballConfig: %v", err)
	}
	if cfg.DynastyFormat != "sf_dynasty" {
		t.Errorf("DynastyFormat = %q, want default sf_dynasty", cfg.DynastyFormat)
	}
}

func TestLoadFootballConfigHonorsDynastyFormatOverride(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")
	t.Setenv("DYNASTY_FORMAT", "non_sf_redraft")

	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatalf("loadFootballConfig: %v", err)
	}
	if cfg.DynastyFormat != "non_sf_redraft" {
		t.Errorf("DynastyFormat = %q, want non_sf_redraft", cfg.DynastyFormat)
	}
}

func TestLoadFootballConfigPushoverKeysDefaultToBasePair(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")
	t.Setenv("FOOTBALL_PUSHOVER_USER_KEY", "")
	t.Setenv("FOOTBALL_PUSHOVER_GROUP_KEY", "")
	t.Setenv("PUSHOVER_USER_KEY", "base-user")
	t.Setenv("PUSHOVER_GROUP_KEY", "base-group")

	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatalf("loadFootballConfig: %v", err)
	}
	if cfg.FootballPushoverUserKey != "base-user" {
		t.Errorf("FootballPushoverUserKey = %q, want base-user (fallback)", cfg.FootballPushoverUserKey)
	}
	if cfg.FootballPushoverGroupKey != "base-group" {
		t.Errorf("FootballPushoverGroupKey = %q, want base-group (fallback)", cfg.FootballPushoverGroupKey)
	}
}

func TestLoadFootballConfigPushoverKeysPreferFootballSpecific(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")
	t.Setenv("FOOTBALL_PUSHOVER_USER_KEY", "fb-user")
	t.Setenv("FOOTBALL_PUSHOVER_GROUP_KEY", "fb-group")
	t.Setenv("PUSHOVER_USER_KEY", "base-user")
	t.Setenv("PUSHOVER_GROUP_KEY", "base-group")

	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatalf("loadFootballConfig: %v", err)
	}
	if cfg.FootballPushoverUserKey != "fb-user" {
		t.Errorf("FootballPushoverUserKey = %q, want fb-user (explicit wins)", cfg.FootballPushoverUserKey)
	}
	if cfg.FootballPushoverGroupKey != "fb-group" {
		t.Errorf("FootballPushoverGroupKey = %q, want fb-group (explicit wins)", cfg.FootballPushoverGroupKey)
	}
}

func TestInitFootballRequiresNoFantraxCreds(t *testing.T) {
	for _, v := range []string{"FANTRAX_USERNAME", "FANTRAX_PASSWORD", "FANTRAX_LEAGUE_ID", "FANTRAX_TEAM_ID"} {
		t.Setenv(v, "")
	}
	t.Setenv("STATE_BUCKET", "")
	t.Setenv("RUN_ID", "")
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")

	cfg, sc, err := initFootball()
	if err != nil {
		t.Fatalf("initFootball: %v (must succeed with zero FANTRAX_* vars set)", err)
	}
	if cfg.SleeperLeagueID != "lg1" {
		t.Errorf("SleeperLeagueID = %q, want lg1", cfg.SleeperLeagueID)
	}
	if sc == nil {
		t.Fatal("initFootball: expected non-nil *sleeper.Client")
	}
}

func TestInitFootballErrorsWithoutSleeperLeagueID(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "")
	t.Setenv("STATE_BUCKET", "")
	t.Setenv("RUN_ID", "")

	if _, _, err := initFootball(); err == nil {
		t.Fatal("initFootball: expected error when SLEEPER_LEAGUE_ID unset, got nil")
	}
}

func TestInitFootballSetsClientCacheDirUnlessNoCache(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "lg1")
	t.Setenv("STATE_BUCKET", "")
	t.Setenv("RUN_ID", "")

	origNoCache := noCache
	t.Cleanup(func() { noCache = origNoCache })

	noCache = false
	_, sc, err := initFootball()
	if err != nil {
		t.Fatalf("initFootball: %v", err)
	}
	if sc.CacheDir != cacheDir {
		t.Errorf("CacheDir = %q, want %q (cache enabled)", sc.CacheDir, cacheDir)
	}

	noCache = true
	_, sc, err = initFootball()
	if err != nil {
		t.Fatalf("initFootball (--no-cache): %v", err)
	}
	if sc.CacheDir != "" {
		t.Errorf("CacheDir = %q, want empty (--no-cache set)", sc.CacheDir)
	}
}
