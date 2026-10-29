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

// recentWeeks is the drop detector's window: the current bucket and `back`
// before it, floored at week 1 like pollWeeks.
func TestRecentWeeks_LastBucketsFlooredAtWeekOne(t *testing.T) {
	cases := []struct {
		week, back int
		want       []int
	}{
		{10, 2, []int{8, 9, 10}},
		{2, 2, []int{1, 2}},
		{1, 2, []int{1}},
		{0, 2, []int{1}},
		{5, 0, []int{5}},
	}
	for _, tc := range cases {
		got := recentWeeks(&sleeper.NFLState{Week: tc.week}, tc.back)
		if len(got) != len(tc.want) {
			t.Fatalf("recentWeeks(week=%d, back=%d) = %v, want %v", tc.week, tc.back, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("recentWeeks(week=%d, back=%d) = %v, want %v", tc.week, tc.back, got, tc.want)
			}
		}
	}
}
