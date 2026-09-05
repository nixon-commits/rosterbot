package fantrax

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// DatedPitcherStart records a single SP game start with its date, FPts, and
// the pitcher's MLB team. Unlike PitcherStart (which gscheck uses for
// deduction), this is a complete list of all active-slot starts for use in
// award/highlight reports.
type DatedPitcherStart struct {
	PitcherName string    `json:"pitcher_name"`
	Date        time.Time `json:"date"`
	FPts        float64   `json:"fpts"`
	MLBTeam     string    `json:"mlb_team"`
}

// PitcherDay is one pitcher's per-day roster fact: he appeared on the team's
// frozen snapshot for that date, listed under MLBTeam, and either did or did
// not take an active-slot start.
//
// It exists because a rate needs a DENOMINATOR and DatedPitcherStart only
// carries the numerator. Counting a pitcher's starts against every day in a
// window reads a mid-window acquisition as an arm that declined to start on
// days he was not ours — the same error class rosterbot-6tw fixed for the
// hitter recency window, where absent days contributed no row at all and were
// indistinguishable from zeroes.
type PitcherDay struct {
	PitcherName string    `json:"pitcher_name"`
	Date        time.Time `json:"date"`
	MLBTeam     string    `json:"mlb_team"`
	// FPts is the day's fantasy-point delta, zeroed on a first appearance so
	// pre-window production is never credited as same-day points. Meaningful
	// only where Started is true.
	FPts    float64 `json:"fpts"`
	Started bool    `json:"started"`

	// FirstAppearance marks a row the walk had no previous YTD to diff
	// against: either the pitcher's first day on our roster inside the window,
	// or the first day after a gap that the baseline never saw.
	//
	// It is a DATA-QUALITY flag, not a roster fact, and it exists because such
	// a row is silently asymmetric. GetTeamPitcherDays caps an unseen
	// pitcher's first GS delta at 1, so the row CAN report Started even though
	// the delta it was derived from covers the pitcher's whole pre-window
	// season — while a first-appearance day on which he did not start is
	// indistinguishable from one on which he did but was benched. A rate
	// denominator must drop the row in both directions rather than keep the
	// half it can see.
	FirstAppearance bool `json:"first_appearance,omitempty"`

	// StatusID and PosShortNames are that day's roster facts, joined from the
	// same period's roster-stats snapshot. They are populated ONLY by
	// GetTeamPitcherDaysWithStatus; GetTeamPitcherDays leaves them empty
	// rather than paying for a join its callers do not read.
	//
	// StatusID is Fantrax's slot status ("1" active, "2" reserve, "3" IL,
	// "9" minors); PosShortNames is the position string rosterSPNames tests
	// for "SP".
	StatusID      string `json:"status_id,omitempty"`
	PosShortNames string `json:"pos_short_names,omitempty"`
}

// PitcherRosterMeta is the per-day roster fact the pitcher-GS snapshot does not
// carry: where the pitcher sat that day, and what he was eligible for.
//
// It is a projection of the roster-stats snapshot rather than the whole row,
// because those two fields are all any consumer of the join needs and a
// narrower type is one fewer thing for a caller to mis-populate.
type PitcherRosterMeta struct {
	StatusID      string
	PosShortNames string
}

// Fantrax roster StatusIDs, as seen on every per-period roster snapshot.
//
// Named because the distinction between the first pair and the second is a
// real one that reads as an arbitrary digit at the call site: "1" and "2" are
// two ways of being AVAILABLE (a bench arm can be activated tomorrow), while
// "3" and "9" are two ways of not being on the major-league roster at all. A
// rate denominator has to drop the second pair and keep the first.
//
// StatusReserve has NO READER anywhere in the tree, and that is deliberate: it
// completes the vocabulary the other three are read against, so "available"
// spells out as both of its values rather than as one value and an implied
// remainder. The cost is that nothing would fail if its value rotted — an
// exported constant with no reader is invisible to the unused-code linters and
// to every test — so treat it as documentation, not as a checked fact. The
// moment something reads it, that stops being true and a test should pin it.
const (
	StatusActive  = "1"
	StatusReserve = "2"
	StatusIL      = "3"
	StatusMinors  = "9"
)

// PitcherDayWalk is the pure kernel of the pitcher-day walk: it carries the
// running YTD baselines and turns one day's frozen snapshots into that day's
// PitcherDay rows.
//
// Separated from the fetch so the derivation can be driven from frozen
// snapshots on disk. The alternative — a diagnostic that re-implements the
// YTD diff — is exactly how rosterbot-goht ended up shipping an estimator with
// a different denominator from the one its own harness measured.
type PitcherDayWalk struct {
	prevGS   map[string]int
	prevFPts map[string]float64
}

// NewPitcherDayWalk starts a walk with no baseline. Every pitcher's first day
// is then a FirstAppearance; call Baseline first to seed the day before the
// window, which is what makes the window's first day a real single-day delta.
func NewPitcherDayWalk() *PitcherDayWalk {
	return &PitcherDayWalk{prevGS: map[string]int{}, prevFPts: map[string]float64{}}
}

// Baseline seeds the running YTD from a snapshot that is NOT emitted as a day —
// the day before the window's start.
func (w *PitcherDayWalk) Baseline(snap map[string]PitcherSnapshotRow) {
	for pid, s := range snap {
		w.prevGS[pid] = s.GS
		w.prevFPts[pid] = s.FPts
	}
}

// Day derives one day's rows and advances the baseline. roster may be nil, in
// which case the roster-meta fields are left empty.
func (w *PitcherDayWalk) Day(date time.Time, snap map[string]PitcherSnapshotRow, roster map[string]PitcherRosterMeta) []PitcherDay {
	out := make([]PitcherDay, 0, len(snap))
	for pid, s := range snap {
		prev, existed := w.prevGS[pid]
		row := PitcherDay{
			PitcherName:     s.Name,
			Date:            date,
			MLBTeam:         s.MLBTeam,
			FirstAppearance: !existed,
		}
		if meta, ok := roster[pid]; ok {
			row.StatusID = meta.StatusID
			row.PosShortNames = meta.PosShortNames
		}
		// Only count active-slot starts so a benched/IL pitcher's outing
		// doesn't appear (mirrors GetTeamGS semantics). The row itself is
		// still emitted: he was rostered that day, which is the fact the
		// denominator needs.
		if s.Active {
			delta := s.GS - prev
			// First-appearance cap: a pitcher cannot earn more than one GS
			// per day, so cap an unseen pitcher's first delta at 1 to avoid
			// counting pre-period or hitter-slot starts. Mirrors GetTeamGS.
			if !existed && delta > 1 {
				delta = 1
			}
			if delta > 0 {
				row.Started = true
				row.FPts = s.FPts - w.prevFPts[pid]
				// On first appearance, the prevFPts baseline is zero so the
				// delta would be the YTD total. Zero it so we don't credit
				// pre-window production as same-day points. Mirrors
				// DailyFantasyPoints's first-appearance handling.
				if !existed {
					row.FPts = 0
				}
			}
		}
		out = append(out, row)
		// Retain latest YTD regardless of active status so future days diff
		// against the real prior YTD (handles two-way players, IL trips).
		w.prevGS[pid] = s.GS
		w.prevFPts[pid] = s.FPts
	}
	return out
}

// GetTeamPitcherStarts returns every active-slot SP start a team made within
// [start, end] (inclusive). It is exactly the Started subset of
// GetTeamPitcherDays — a filter over that one walk rather than a second copy
// of the YTD-diff logic, because a duplicated derivation is how the two
// pitcher-GS cache-key builders drifted for four days without an error
// (rosterbot-sza). TestGetTeamPitcherStarts_IsTheStartedSubsetOfTheDayWalk
// pins them together.
//
// See GetTeamPitcherDays for the caching contract.
func (c *Client) GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]DatedPitcherStart, error) {
	days, err := c.GetTeamPitcherDays(teamID, start, end, seasonStart, cacheDir, cacheTTL)
	if err != nil {
		return nil, err
	}
	var starts []DatedPitcherStart
	for _, d := range days {
		if !d.Started {
			continue
		}
		starts = append(starts, DatedPitcherStart{
			PitcherName: d.PitcherName,
			Date:        d.Date,
			FPts:        d.FPts,
			MLBTeam:     d.MLBTeam,
		})
	}
	return starts, nil
}

// GetTeamPitcherDays returns one row per (pitcher, day) the team's frozen
// per-day snapshots cover within [start, end] (inclusive). Mirrors the per-day
// diff logic from GetTeamGS but without the gsMax filter — we want all starts,
// not just overage. Caller supplies seasonStart so periods can be derived.
//
// cacheDir/cacheTTL enable per-period snapshot caching (key
// `fantrax-pitcher-gs-<teamID>-<season>-<period>`). Past-period snapshots are
// immutable, so a long TTL (fantrax.PastPeriodTTL) lets the recap pipeline
// reuse data across runs and avoid re-hitting Fantrax for completed weeks. The
// TTL is narrowed per period by snapshotTTL, so passing the long one does not
// pin the live period. Pass
// cacheDir="" or cacheTTL=0 to bypass — used by the live gscheck path,
// which always wants fresh data. The 200ms throttle between days only
// fires when the upstream API was actually hit; cache-only days don't
// pace themselves.
func (c *Client) GetTeamPitcherDays(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]PitcherDay, error) {
	return c.teamPitcherDays(teamID, start, end, seasonStart, cacheDir, cacheTTL, false)
}

// GetTeamPitcherDaysWithStatus is GetTeamPitcherDays with each row joined
// against the same period's roster-stats snapshot, so it also carries the day's
// StatusID and PosShortNames.
//
// A SEPARATE method rather than a flag on the walk, because the join is not
// free and only one caller reads it. It doubles the walk's snapshot reads
// (one roster-stats read per period alongside each pitcher-GS read) and, more
// importantly, it adds a second upstream that can fail — and the recap, the
// only other caller of the walk, would then be able to lose a whole week's
// pitching highlights over data it never looks at. Keeping the fetch profile
// and the failure surface of GetTeamPitcherDays untouched is the point.
//
// No new upstream endpoint is involved: the roster-stats snapshot is the one
// DailyFantasyPoints already walks, read through the same cached helper at the
// same key and tier (`fantrax-roster-stats-<teamID>-<season>-<period>`,
// PastPeriodTTL narrowed by snapshotTTL).
func (c *Client) GetTeamPitcherDaysWithStatus(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]PitcherDay, error) {
	return c.teamPitcherDays(teamID, start, end, seasonStart, cacheDir, cacheTTL, true)
}

func (c *Client) teamPitcherDays(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration, withRosterMeta bool) ([]PitcherDay, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("end %s before start %s", end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	// One cache per period, not one for the window: the caller's TTL is the long
	// immutable-past one, and snapshotTTL narrows it to todayTTL for any period
	// that can still change. Before rosterbot-qoa this was a single flat-TTL
	// cache, which was survivable at 30 days and is not at PastPeriodTTL — the
	// live period's snapshot would be pinned for the rest of the season.
	curPeriod := c.DailyPeriodFor(seasonStart, time.Now().UTC())
	season := seasonStart.Year()
	snapCacheFor := func(period DailyPeriod) *cache.FileCache[map[string]playerGSSnapshot] {
		ttl := snapshotTTL(cacheTTL, period, curPeriod)
		if cacheDir == "" || ttl == 0 {
			return nil
		}
		return cache.New[map[string]playerGSSnapshot](cacheDir, ttl)
	}

	// Baseline YTD from the day before `start` so the first day yields a
	// single-day delta. Same logic as DailyFantasyPoints baseline handling.
	walk := NewPitcherDayWalk()
	dayBefore := start.AddDate(0, 0, -1)
	if !dayBefore.Before(seasonStart) {
		basePeriod := c.DailyPeriodFor(seasonStart, dayBefore)
		if basePeriod >= 1 {
			info, _, err := c.getPlayerGSSnapshotForPeriodCached(snapCacheFor(basePeriod), teamID, season, basePeriod)
			if err != nil {
				return nil, fmt.Errorf("baseline pitcher snapshot period %d: %w", basePeriod, err)
			}
			walk.Baseline(info)
		}
	}

	// The roster-meta join reads the roster-stats snapshot for the same period
	// through DailyFantasyPoints's own cached helper, at the same key and tier.
	// Nil when the caller did not ask for it, which is what keeps
	// GetTeamPitcherDays's fetch profile and failure surface unchanged.
	// A zero TTL is how "caching is off for this caller" reaches the roster
	// read; getPeriodSnapshotCached takes a live *FileCache either way (unlike
	// its pitcher-GS sibling, which accepts nil), and a zero-TTL cache reads
	// straight through to the fetch.
	rosterCacheFor := func(period DailyPeriod) *cache.FileCache[periodSnapshot] {
		ttl := snapshotTTL(cacheTTL, period, curPeriod)
		if cacheDir == "" {
			ttl = 0
		}
		return cache.New[periodSnapshot](cacheDir, ttl)
	}

	var out []PitcherDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		period := c.DailyPeriodFor(seasonStart, d)
		info, hitNetwork, err := c.getPlayerGSSnapshotForPeriodCached(snapCacheFor(period), teamID, season, period)
		if err != nil {
			return nil, fmt.Errorf("pitcher snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
		}

		var meta map[string]PitcherRosterMeta
		if withRosterMeta {
			snap, rosterHitNetwork, err := c.getPeriodSnapshotCached(rosterCacheFor(period), teamID, season, period)
			if err != nil {
				return nil, fmt.Errorf("roster snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
			}
			hitNetwork = hitNetwork || rosterHitNetwork
			meta = make(map[string]PitcherRosterMeta, len(snap.Pitchers))
			for pid, row := range snap.Pitchers {
				meta[pid] = PitcherRosterMeta{StatusID: row.StatusID, PosShortNames: row.PosShortNames}
			}
		}

		out = append(out, walk.Day(d, info, meta)...)

		if hitNetwork {
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Map iteration above is randomized, so a stable order is not optional:
	// GetTeamPitcherStarts's output feeds the recap's rendered start list, and
	// this package owes idempotency (see buildGSForecast's confirmed-starter
	// sort for the same reasoning).
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].PitcherName < out[j].PitcherName
	})
	return out, nil
}
