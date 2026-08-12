package cmd

import (
	"reflect"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestPollWeeks_RegularSeasonReturnsOneThroughCurrentWeek(t *testing.T) {
	got := pollWeeks(&sleeper.NFLState{Week: 3, SeasonType: "regular"})
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pollWeeks = %v, want %v", got, want)
	}
}

func TestPollWeeks_PreseasonWeekZeroOrBelowFloorsToWeekOne(t *testing.T) {
	for _, week := range []int{0, -1} {
		got := pollWeeks(&sleeper.NFLState{Week: week, SeasonType: "pre"})
		want := []int{1}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pollWeeks(week=%d) = %v, want %v (floors to week 1)", week, got, want)
		}
	}
}

func TestPollWeeks_WeekOneReturnsJustWeekOne(t *testing.T) {
	got := pollWeeks(&sleeper.NFLState{Week: 1})
	want := []int{1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pollWeeks = %v, want %v", got, want)
	}
}
