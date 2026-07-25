package lineuprun

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// fakeDateClient is the whole dependency surface ResolveDates needs — two
// methods, no Fantrax client. That narrowness is the point of the phase split.
type fakeDateClient struct {
	seasonStart, seasonEnd time.Time
	seasonErr              error

	weekStart, weekEnd time.Time
	weekErr            error

	seasonCalls, weekCalls int
}

func (f *fakeDateClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	f.seasonCalls++
	return f.seasonStart, f.seasonEnd, f.seasonErr
}

func (f *fakeDateClient) GetMatchupWeekBounds(date, seasonStart time.Time) (time.Time, time.Time, error) {
	f.weekCalls++
	return f.weekStart, f.weekEnd, f.weekErr
}

func discard(string, ...any) {}

// Explicit --dates needs no lookup at all: the caller's list passes through and
// the client is never touched.
func TestResolveDates_ExplicitDatesPassThroughWithoutFetching(t *testing.T) {
	ft := &fakeDateClient{}
	base := []time.Time{day(2026, 7, 25), day(2026, 7, 26)}

	got, seasonStart, err := ResolveDates(ft, base, Options{Today: day(2026, 7, 25)}, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Errorf("got %v, want %v", got, base)
	}
	if !seasonStart.IsZero() {
		t.Errorf("no lookup requested — seasonStart should stay zero, got %v", seasonStart)
	}
	if ft.seasonCalls != 0 || ft.weekCalls != 0 {
		t.Errorf("explicit dates must not fetch: season=%d week=%d", ft.seasonCalls, ft.weekCalls)
	}
}

// --matchup resolves to the remaining days of the current matchup week,
// starting at today (past days in the week are skipped).
func TestResolveDates_MatchupSkipsPastDaysInWeek(t *testing.T) {
	ft := &fakeDateClient{
		seasonStart: day(2026, 3, 25), seasonEnd: day(2026, 9, 13),
		// The merged All-Star weekly period — the rosterbot-z3b window.
		weekStart: day(2026, 7, 13), weekEnd: day(2026, 7, 26),
	}

	got, seasonStart, err := ResolveDates(ft, nil,
		Options{Today: day(2026, 7, 25), NeedsMatchupLookup: true}, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []time.Time{day(2026, 7, 25), day(2026, 7, 26)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !seasonStart.Equal(day(2026, 3, 25)) {
		t.Errorf("seasonStart = %v, want 2026-03-25", seasonStart)
	}
}

// --dates all resolves from today (not the season opener) through season end.
func TestResolveDates_SeasonStartsFromTodayNotOpener(t *testing.T) {
	ft := &fakeDateClient{
		seasonStart: day(2026, 3, 25), seasonEnd: day(2026, 3, 28),
	}

	got, _, err := ResolveDates(ft, nil,
		Options{Today: day(2026, 3, 27), NeedsSeasonLookup: true}, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []time.Time{day(2026, 3, 27), day(2026, 3, 28)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The acceptance criterion this phase exists for: Run must stop using the
// caller's *config.Config as an output parameter. ResolveDates returns a value
// and must not write through to the slice it was handed.
func TestResolveDates_DoesNotMutateCallerSlice(t *testing.T) {
	ft := &fakeDateClient{
		seasonStart: day(2026, 3, 25), seasonEnd: day(2026, 9, 13),
		weekStart: day(2026, 7, 25), weekEnd: day(2026, 7, 27),
	}
	// Extra capacity is the dangerous shape: a bare append would write into the
	// caller's backing array instead of allocating.
	base := make([]time.Time, 1, 8)
	base[0] = day(2026, 7, 25)

	got, _, err := ResolveDates(ft, base, Options{Today: day(2026, 7, 25), NeedsMatchupLookup: true}, discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(base) != 1 || !base[0].Equal(day(2026, 7, 25)) {
		t.Errorf("caller's slice was mutated: %v", base)
	}
	if len(got) <= len(base) {
		t.Errorf("expected resolved dates to extend the base, got %v", got)
	}
	// Mutating the result must not reach back into the caller's array either.
	got[0] = day(2000, 1, 1)
	if base[0].Equal(day(2000, 1, 1)) {
		t.Error("result shares a backing array with the caller's slice")
	}
}

func TestResolveDates_SeasonFetchErrorPropagates(t *testing.T) {
	want := errors.New("season range down")
	ft := &fakeDateClient{seasonErr: want}

	if _, _, err := ResolveDates(ft, nil, Options{NeedsSeasonLookup: true}, discard); !errors.Is(err, want) {
		t.Errorf("expected wrapped %v, got %v", want, err)
	}
}

func TestResolveDates_NoMatchupWeekIsAnError(t *testing.T) {
	ft := &fakeDateClient{seasonStart: day(2026, 3, 25), seasonEnd: day(2026, 9, 13)}

	_, _, err := ResolveDates(ft, nil, Options{Today: day(2026, 12, 1), NeedsMatchupLookup: true}, discard)
	if err == nil {
		t.Fatal("expected an error when today falls in no matchup week")
	}
}
