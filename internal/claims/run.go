package claims

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/waivers"
)

// projectionTTL matches the FanGraphs 12h cache cadence used elsewhere.
const projectionTTL = 12 * time.Hour

// WeightsProvider lets Run fetch league scoring weights for projection scoring.
// Satisfied by *fantrax.Client.
type WeightsProvider interface {
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
}

// Run fetches claims since the cursor, builds the recap, emits output, and
// writes the audit ledger. It is a no-op (early return, cursor still advances)
// when there are no new claims.
func Run(ft ClaimsClient, today time.Time, opts Options) error {
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

	players := opts.HKBPlayers
	if players == nil {
		players, err = hkb.GetPlayers(opts.CacheDir)
		if err != nil {
			return fmt.Errorf("get HKB players: %w", err)
		}
	}
	moves := BuildMoves(txs, buildHKBLookup(players))

	// No-op: nothing processed since the cursor. Advance cursor and return.
	if len(moves) == 0 {
		log.Println("No waiver claims processed since last run.")
		if err := saveCursor(opts.CursorPath, today); err != nil {
			log.Printf("WARNING: failed to save claims cursor: %v", err)
		}
		return nil
	}

	// Enrichment: MLBAM IDs, Statcast signals, projections (all best-effort).
	resolveAddedIDs(moves, opts.CacheDir)
	if !opts.NoSignals {
		if bundle, berr := waivers.LoadSavant(opts.CacheDir, today.Year(), today, projectionTTL); berr == nil {
			EnrichSignals(moves, bundle, waivers.DefaultThresholds())
		} else {
			log.Printf("WARNING: signal enrichment skipped: %v", berr)
		}
	}
	if wp, ok := ft.(WeightsProvider); ok {
		hw, herr := wp.GetScoringWeights()
		pw, perr := wp.GetPitcherScoringWeights()
		if herr == nil && perr == nil {
			enrichProjections(moves, hw, pw, opts.CacheDir, projectionTTL)
		} else {
			log.Printf("WARNING: projection scoring skipped: %v / %v", herr, perr)
		}
	}

	// Output: stdout (color) always; GHA summary (no color) when configured.
	fmt.Println(FormatReport(moves, opts.DropsMin, true))
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		if f, ferr := os.OpenFile(summaryPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); ferr != nil {
			log.Printf("WARNING: failed to open GHA summary: %v", ferr)
		} else {
			fmt.Fprint(f, FormatReport(moves, opts.DropsMin, false))
			f.Close()
		}
	}

	// Ledger (skipped in dry-run).
	if !opts.DryRun {
		if err := WriteLedger(opts.LedgerDir, BuildLedger(today, moves)); err != nil {
			log.Printf("WARNING: failed to write ledger: %v", err)
		}
	}

	// Pushover (skipped in dry-run / when creds absent).
	if !opts.DryRun && opts.PushoverUserKey != "" && opts.PushoverAPIToken != "" {
		if err := notify.SendPushover(opts.PushoverUserKey, opts.PushoverAPIToken, "Waiver Claims", FormatPushover(moves)); err != nil {
			log.Printf("notification failed: %v", err)
		}
	}

	// Advance cursor LAST so a mid-run failure re-processes next time.
	// WriteLedger is idempotent (overwrites per-date) but Pushover may duplicate
	// if saveCursor fails after notifying — accepted for an advisory notification.
	if err := saveCursor(opts.CursorPath, today); err != nil {
		log.Printf("WARNING: failed to save claims cursor: %v", err)
	}
	return nil
}
