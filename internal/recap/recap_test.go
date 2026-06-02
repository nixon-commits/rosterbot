package recap

import (
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func dayRoster(date time.Time, players ...fantrax.DayPlayerFP) fantrax.DayRoster {
	return fantrax.DayRoster{Date: date, Players: players}
}

func TestComputeSeasonMeanFromDays(t *testing.T) {
	d1 := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	days := []fantrax.DayRoster{
		// Day 1: 2 active starters with games (40 + 10), 1 active no-game-no-pts (excluded), 1 inactive (excluded)
		dayRoster(d1,
			fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: 40},
			fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: 10},
			fantrax.DayPlayerFP{Active: true, HadGame: false, FPts: 0},
			fantrax.DayPlayerFP{Active: false, HadGame: true, FPts: 99},
		),
		// Day 2: all-bench day — should NOT count toward denominator
		dayRoster(d2,
			fantrax.DayPlayerFP{Active: false, HadGame: true, FPts: 50},
			fantrax.DayPlayerFP{Active: true, HadGame: false, FPts: 0},
		),
		// Day 3: negative FPts on an active starter who had a game (counts)
		dayRoster(d3,
			fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: -3},
			fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: 13},
		),
	}

	mean, played := computeSeasonMeanFromDays(days)
	if played != 2 {
		t.Fatalf("days played: want 2, got %d", played)
	}
	// Day1 sum = 50; Day3 sum = 10. Mean = 30.
	if math.Abs(mean-30.0) > 1e-9 {
		t.Errorf("mean: want 30, got %.6f", mean)
	}
}

func TestComputeSeasonMeanFromDaysEmpty(t *testing.T) {
	if mean, played := computeSeasonMeanFromDays(nil); mean != 0 || played != 0 {
		t.Errorf("nil → want (0,0), got (%.2f, %d)", mean, played)
	}
}

func TestComputeSeasonMeanFromDaysAllInactive(t *testing.T) {
	d := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{
		dayRoster(d,
			fantrax.DayPlayerFP{Active: false, HadGame: true, FPts: 50},
			fantrax.DayPlayerFP{Active: false, HadGame: false, FPts: 0},
		),
	}
	if mean, played := computeSeasonMeanFromDays(days); mean != 0 || played != 0 {
		t.Errorf("all-inactive → want (0,0), got (%.2f, %d)", mean, played)
	}
}

func TestComputeSeasonMeanFromDaysSingleDay(t *testing.T) {
	d := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{
		dayRoster(d, fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: 42.5}),
	}
	mean, played := computeSeasonMeanFromDays(days)
	if played != 1 || math.Abs(mean-42.5) > 1e-9 {
		t.Errorf("single day → want (42.5, 1), got (%.2f, %d)", mean, played)
	}
}
