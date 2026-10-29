package claims

import (
	"context"
	"fmt"
	"github.com/nixon-commits/rosterbot/internal/jobreport"
	"io"
	"log"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/statcast"
)

// WeightsProvider lets Run fetch league scoring weights for projection scoring.
// Satisfied by *fantrax.Client.
type WeightsProvider interface {
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
}

// Run fetches claims since the cursor, builds the recap, emits output, and
// writes the audit ledger. It is a no-op (early return, cursor still advances)
// when there are no new claims.
func Run(ctx context.Context, ft ClaimsClient, today time.Time, opts Options) error {
	if opts.CacheDir == "" {
		opts.CacheDir = ".cache"
	}
	if opts.LedgerDir == "" {
		opts.LedgerDir = ".waivers/claims"
	}
	if opts.CursorPath == "" {
		opts.CursorPath = cursorFile
	}
	if opts.DropsMin == 0 {
		opts.DropsMin = 2000
	}

	since := opts.Since
	if since.IsZero() {
		since = loadCursor(opts.CursorPath)
		if since.IsZero() {
			since = today.AddDate(0, 0, -3)
		}
	}

	txs, err := ft.GetRecentTransactions(since)
	if err != nil {
		return fmt.Errorf("get recent transactions: %w", err)
	}

	// Advanced last so a mid-run failure re-processes next time, and never in
	// a dry run. WriteLedger is idempotent (overwrites per-date) but the push
	// may duplicate if the cursor write fails after the send — accepted for
	// an advisory notification.
	cursor := jobreport.Effect{Name: "claims cursor", Do: func() error { return saveCursor(opts.CursorPath, today) }}
	emit := jobreport.Options{DryRun: opts.DryRun}

	// Fast no-op: no transactions at all since the cursor — skip HKB fetch
	// entirely. It is still a Job Report: the run had a result (no moves) and
	// the cursor is its one effect.
	noop := func() error {
		return jobreport.Emit(ctx, jobreport.Report{
			Job:     "claims",
			Wire:    toWireResult(BuildLedger(today, nil)),
			Text:    func(w io.Writer) { fmt.Fprintln(w, "No waiver claims processed since last run.") },
			Effects: []jobreport.Effect{cursor},
		}, emit)
	}
	if len(txs) == 0 {
		return noop()
	}

	players := opts.HKBPlayers
	if players == nil {
		players, err = hkb.GetPlayers(ctx, opts.CacheDir)
		if err != nil {
			return fmt.Errorf("get HKB players: %w", err)
		}
	}
	moves := BuildMoves(txs, buildHKBLookup(players))

	// No-op: transactions existed but none were claim moves (e.g. only trades).
	// Advance cursor and return.
	if len(moves) == 0 {
		return noop()
	}

	// Enrichment: MLBAM IDs, Statcast signals, projections (all best-effort).
	resolveAddedIDs(moves, opts.CacheDir)
	if !opts.NoSignals {
		if bundle, berr := statcast.LoadBundle(ctx, opts.CacheDir, today.Year(), today, statcast.CacheTTL); berr == nil {
			EnrichSignals(moves, bundle, statcast.DefaultThresholds())
		} else {
			log.Printf("WARNING: signal enrichment skipped: %v", berr)
		}
	}
	if wp, ok := ft.(WeightsProvider); ok {
		hw, herr := wp.GetScoringWeights()
		pw, perr := wp.GetPitcherScoringWeights()
		if herr == nil && perr == nil {
			enrichProjections(ctx, moves, hw, pw, opts.CacheDir, projections.ProjectionCacheTTL)
		} else {
			log.Printf("WARNING: projection scoring skipped: %v / %v", herr, perr)
		}
	}

	led := BuildLedger(today, moves)
	return jobreport.Emit(ctx, jobreport.Report{
		Job:  "claims",
		Wire: toWireResult(led),
		// stdout in colour; the GHA summary without.
		Text:    func(w io.Writer) { fmt.Fprintln(w, FormatReport(moves, opts.DropsMin, true)) },
		Summary: func(w io.Writer) { fmt.Fprint(w, FormatReport(moves, opts.DropsMin, false)) },
		Title:   "Waiver Claims",
		Body:    FormatPushover(moves),
		Effects: []jobreport.Effect{
			{Name: "claims ledger", Do: func() error { return WriteLedger(opts.LedgerDir, led) }},
			cursor,
		},
	}, emit)
}
