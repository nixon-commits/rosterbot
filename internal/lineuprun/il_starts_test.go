package lineuprun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

var ilToday = time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)

// fakeProbables answers ProbableStarters from a per-date map.
type fakeProbables struct {
	byDate map[string]map[string]string
	err    error
}

func (f fakeProbables) ProbableStarters(date time.Time) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byDate[date.Format("2006-01-02")], nil
}

// fakeMarkers is an in-memory BlobStore-shaped dedup store.
type fakeMarkers struct {
	seen      map[string][]byte
	getErr    error
	publErr   error
	publCalls int
	// getKeys records every key the caller asked about. It is what lets a test
	// pin that a marker store was actually CONSULTED, rather than that the
	// alert merely mentioned a key — the difference between dedup being wired
	// and dedup being silently disabled.
	getKeys []string
}

func newFakeMarkers() *fakeMarkers { return &fakeMarkers{seen: map[string][]byte{}} }

func (f *fakeMarkers) Get(ctx context.Context, key string) ([]byte, bool, error) {
	f.getKeys = append(f.getKeys, key)
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	b, ok := f.seen[key]
	return b, ok, nil
}

func (f *fakeMarkers) Publish(key string, data []byte) error {
	f.publCalls++
	if f.publErr != nil {
		return f.publErr
	}
	f.seen[key] = data
	return nil
}

func ilRoster() []fantrax.Player {
	return []fantrax.Player{
		{ID: "p1", Name: "Jacob deGrom", MLBTeam: "TEX", Status: "Injured Reserve", IsInjured: true},
	}
}

func probablesToday() fakeProbables {
	return fakeProbables{byDate: map[string]map[string]string{
		"2026-08-16": {"jacob degrom": "TEX"},
	}}
}

func TestReportILStarts_SendsAndMarksOnce(t *testing.T) {
	markers := newFakeMarkers()
	var sent []string

	reportILStarts(ilStartInputs{
		Roster:  ilRoster(),
		Sched:   probablesToday(),
		Today:   ilToday,
		Markers: markers,
		Notify:  func(m string) error { sent = append(sent, m); return nil },
		Out:     io.Discard,
	})

	if len(sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(sent))
	}
	if len(markers.seen) != 1 {
		t.Fatalf("expected 1 marker written, got %d", len(markers.seen))
	}
	if _, ok := markers.seen["p1-2026-08-16"]; !ok {
		t.Errorf("expected marker keyed by player and start date, got keys %v", markers.seen)
	}
}

// The Lineup job runs hourly; without dedup this is ~14 pushes a day.
func TestReportILStarts_SecondRunIsSilent(t *testing.T) {
	markers := newFakeMarkers()
	var sent []string
	notify := func(m string) error { sent = append(sent, m); return nil }

	in := ilStartInputs{
		Roster: ilRoster(), Sched: probablesToday(), Today: ilToday,
		Markers: markers, Notify: notify, Out: io.Discard,
	}
	reportILStarts(in)
	reportILStarts(in)

	if len(sent) != 1 {
		t.Fatalf("expected the second run to be silent, got %d notifications", len(sent))
	}
}

// A failed send must retry on the next run, never be marked as delivered.
func TestReportILStarts_FailedSendIsNotMarked(t *testing.T) {
	markers := newFakeMarkers()

	reportILStarts(ilStartInputs{
		Roster: ilRoster(), Sched: probablesToday(), Today: ilToday,
		Markers: markers,
		Notify:  func(string) error { return errors.New("pushover unreachable") },
		Out:     io.Discard,
	})

	if markers.publCalls != 0 {
		t.Fatalf("expected no marker write after a failed send, got %d", markers.publCalls)
	}
}

// Marking during a dry run would suppress a later real alert that never went out.
func TestReportILStarts_DryRunNeitherSendsNorMarks(t *testing.T) {
	markers := newFakeMarkers()
	var sent []string

	reportILStarts(ilStartInputs{
		Roster: ilRoster(), Sched: probablesToday(), Today: ilToday,
		Markers: markers,
		Notify:  func(m string) error { sent = append(sent, m); return nil },
		DryRun:  true,
		Out:     io.Discard,
	})

	if len(sent) != 0 {
		t.Errorf("expected no send in a dry run, got %d", len(sent))
	}
	if markers.publCalls != 0 {
		t.Errorf("expected no marker write in a dry run, got %d", markers.publCalls)
	}
}

// A marker-store read failure must degrade to a duplicate alert, never to silence.
func TestReportILStarts_MarkerReadFailureStillSends(t *testing.T) {
	markers := newFakeMarkers()
	markers.getErr = errors.New("s3 unavailable")
	var sent []string

	reportILStarts(ilStartInputs{
		Roster: ilRoster(), Sched: probablesToday(), Today: ilToday,
		Markers: markers,
		Notify:  func(m string) error { sent = append(sent, m); return nil },
		Out:     io.Discard,
	})

	if len(sent) != 1 {
		t.Fatalf("expected the alert to send despite a marker read failure, got %d", len(sent))
	}
}

// Tomorrow's announced start is the actionable one — a full day of lead time.
func TestReportILStarts_CoversTomorrow(t *testing.T) {
	markers := newFakeMarkers()
	var sent []string

	reportILStarts(ilStartInputs{
		Roster: ilRoster(),
		Sched: fakeProbables{byDate: map[string]map[string]string{
			"2026-08-17": {"jacob degrom": "TEX"},
		}},
		Today:   ilToday,
		Markers: markers,
		Notify:  func(m string) error { sent = append(sent, m); return nil },
		Out:     io.Discard,
	})

	if len(sent) != 1 {
		t.Fatalf("expected tomorrow's start to alert, got %d", len(sent))
	}
	if _, ok := markers.seen["p1-2026-08-17"]; !ok {
		t.Errorf("expected marker keyed to the start date, got keys %v", markers.seen)
	}
}

// A schedule outage must not take down the optimize run.
func TestReportILStarts_ScheduleErrorIsSoft(t *testing.T) {
	markers := newFakeMarkers()

	reportILStarts(ilStartInputs{
		Roster:  ilRoster(),
		Sched:   fakeProbables{err: errors.New("statsapi down")},
		Today:   ilToday,
		Markers: markers,
		Notify:  func(string) error { t.Error("should not notify when probables are unavailable"); return nil },
		Out:     io.Discard,
	})
}

// The zero case must be visible. A check that prints nothing when it finds
// nothing is indistinguishable from a check whose inputs were empty — which is
// how the recency window stayed silently truncated for a season (rosterbot-6tw).
func TestReportILStarts_PrintsCoverageEvenWhenNothingFires(t *testing.T) {
	var buf strings.Builder

	reportILStarts(ilStartInputs{
		Roster: []fantrax.Player{
			{ID: "h1", Name: "Bobby Witt Jr.", MLBTeam: "KC", Status: "Active"},
			{ID: "p1", Name: "Jacob deGrom", MLBTeam: "TEX", Status: "Injured Reserve"},
		},
		Sched: fakeProbables{byDate: map[string]map[string]string{
			"2026-08-16": {"tarik skubal": "DET", "paul skenes": "PIT"},
		}},
		Today:   ilToday,
		Markers: newFakeMarkers(),
		Notify:  func(string) error { return nil },
		Out:     &buf,
	})

	got := buf.String()
	if !strings.Contains(got, "il-start check:") {
		t.Fatalf("expected a coverage line even with no findings, got %q", got)
	}
	// The two numbers that decide whether this check can ever fire.
	if !strings.Contains(got, "1 IL") {
		t.Errorf("expected the IL-player count in the coverage line, got %q", got)
	}
	if !strings.Contains(got, "2 probable") {
		t.Errorf("expected the probables count in the coverage line, got %q", got)
	}
}

// No marker store (local dev) must not panic or block the check's output.
func TestReportILStarts_NilMarkerStoreDoesNotPanic(t *testing.T) {
	var sent []string

	reportILStarts(ilStartInputs{
		Roster: ilRoster(), Sched: probablesToday(), Today: ilToday,
		Markers: nil,
		Notify:  func(m string) error { sent = append(sent, m); return nil },
		Out:     io.Discard,
	})

	if len(sent) != 1 {
		t.Fatalf("expected the alert to send with no marker store, got %d", len(sent))
	}
}
