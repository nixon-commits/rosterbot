package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
	"github.com/nixon-commits/rosterbot/internal/recaplog"
	"github.com/nixon-commits/rosterbot/internal/report"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/valuereport"
	"github.com/spf13/cobra"
)

var (
	projSiteOut   string
	projSiteOpen  bool
	projSiteScope string
)

var projectionSiteCmd = &cobra.Command{
	Use:   "projection-site",
	Short: "Render the projection-accuracy dashboard from the Analysis Store",
	Long: `Reads the Graded Snapshots written by the grade command (analysis/grades/
on S3 when STATE_BUCKET is set, else local .analysis/) and writes the
aggregated model as JSON, fed to the dashboard SPA.

Output is split by who may read it. The three private reports — model, gap and
views — go to the state bucket's reports/ prefix (.reports/ locally), which no
CloudFront distribution serves; the SPA reaches them through the passkey-gated
GET /v1/reports/{name}. The league-wide artifacts — value.json, football.json
and football-trades.json — are written to <out> and published to the dashboard
bucket's public report/ prefix.`,
	RunE: runProjectionSite,
}

func init() {
	projectionSiteCmd.Flags().StringVar(&projSiteOut, "out", "report", "output directory for the publicly-published artifacts (value.json, football.json, football-trades.json)")
	projectionSiteCmd.Flags().BoolVar(&projSiteOpen, "open", false, "open the rendered value.json in the default handler")
	projectionSiteCmd.Flags().StringVar(&projSiteScope, "scope", string(scopeAll),
		"which half to render: all | tenant (private per-user reports) | league (public value/football)")
	rootCmd.AddCommand(projectionSiteCmd)
}

func runProjectionSite(cmd *cobra.Command, args []string) error {
	scope, err := parseProjectionScope(projSiteScope)
	if err != nil {
		return err
	}
	today := todayET()

	reader, err := statestore.FromEnv().AnalysisReader()
	if err != nil {
		return fmt.Errorf("init analysis reader: %w", err)
	}

	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read grades: %w", err)
	}

	// Earliest graded date is a safe season-start floor with no Fantrax call.
	seasonStart := today
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.Before(seasonStart) {
			seasonStart = d
		}
	}

	m := report.Aggregate(rows, time.Now().UTC(), seasonStart)

	// The three private reports go to the state bucket's reports/ prefix, which
	// no CloudFront distribution serves; the SPA reads them back through the
	// passkey-gated GET /v1/reports/{name}. Opening the store is fatal here for
	// the same reason writing model.json was: it is this command's primary
	// output, and a run that cannot write it has done nothing.
	var reports lineupapi.Publisher
	if scopeWritesPrivateReports(scope) {
		rs, rerr := statestore.FromEnv().ReportsStore()
		if rerr != nil {
			return fmt.Errorf("init reports store: %w", rerr)
		}
		reports = rs
		if err := publishReport(reports, lineupapi.ReportModelKey, m); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Published report %q (%d graded rows, latest %s)\n",
			lineupapi.ReportModelKey, len(rows), m.LatestDate)
		// Disclose read-time exclusions (rosterbot-c61b) on the same run that
		// would otherwise silently compare stale model input against fresh
		// actuals -- the rows are still in the store, but m has already
		// withheld them from every view/compare figure it just published.
		if m.ExcludedRows > 0 {
			fmt.Fprintf(os.Stderr, "Excluded %d graded row(s) as known-stale model input:\n", m.ExcludedRows)
			for _, p := range m.Excluded {
				fmt.Fprintf(os.Stderr, "  dt=%s system=%s rows=%d (%s; %s)\n",
					p.Dt, p.System, p.Rows, p.Bead, p.Reason)
			}
		}
	}

	// Only for a scope that actually publishes into it — see ensurePublicDir.
	// Creating it unconditionally would make a per-tenant run's empty directory
	// wipe the league's public artifacts on the next sync.
	if err := ensurePublicDir(scope, projSiteOut); err != nil {
		return err
	}

	// Emit value.json (team HKB value tracker) alongside the accuracy model.
	// It reads its own store and is additive, so a team-value hiccup soft-fails
	// rather than blocking the accuracy dashboard deploy. It stays on the
	// public report/ prefix: league-wide standings, not one manager's numbers.
	if scopeWritesPublicDir(scope) {
		if err := renderValueSite(projSiteOut); err != nil {
			fmt.Fprintf(os.Stderr, "warning: value.json not written: %v\n", err)
		}
	}

	// Emit the views report (recap-site readership) on the same terms: its own
	// source, additive, and soft-failing so a log-read hiccup never blocks the
	// accuracy dashboard deploy. Private — readership is the operator's, and
	// UNLIKE the model and gap reports it is the same deployment-wide data on
	// every run, so under per-tenant fan-out it publishes only into the
	// operator's partition. Without the gate every tester's Views tab showed
	// the operator's site analytics, visitor IPs included.
	if scopeWritesPrivateReports(scope) {
		if !viewsReportBelongsHere(statestore.Tenant(), os.Getenv("OPERATOR_USER_ID")) {
			fmt.Fprintln(os.Stderr, "views report: skipped — recap readership belongs to the operator's partition")
		} else if err := renderViewsSite(reports); err != nil {
			fmt.Fprintf(os.Stderr, "warning: views report not written: %v\n", err)
		}
	}

	// Emit the gap report (realized-vs-hindsight lineup gap) on the same terms:
	// its own store, additive, soft-failing so a gap hiccup never blocks the
	// accuracy dashboard deploy. Private — this is how many points the manager
	// left on their own bench.
	if scopeWritesPrivateReports(scope) {
		if err := renderGapSite(reports); err != nil {
			fmt.Fprintf(os.Stderr, "warning: gap report not written: %v\n", err)
		}
	}

	// Emit football.json (dynasty football standings) on the same terms: its
	// own store, additive, soft-failing so a football hiccup never blocks the
	// accuracy dashboard deploy. projection-site never calls initApp (pure
	// statestore reads), so there is no Fantrax coupling to work around here.
	if scopeWritesPublicDir(scope) {
		if err := renderFootballSite(projSiteOut); err != nil {
			fmt.Fprintf(os.Stderr, "warning: football.json not written: %v\n", err)
		}
	}

	// Emit football-trades.json (the durable Football Trade Log) on the same
	// terms as football.json, and — load-bearing — as a SEPARATE artifact
	// rather than a field on it. The Football tab renders two independent
	// sections, and football.js returns early when the standings model is
	// missing or empty; folding the trades in would make a fresh deploy with no
	// value rows yet hide the trade log too.
	//
	// Public, beside football.json and value.json: these are completed trades,
	// already league-visible on Sleeper. That is the opposite call from
	// baseball's /v1/trades, which is passkey-gated because it serves PENDING
	// offers — an offer you have not decided on yet is genuinely private.
	//
	// Freshness caveat, stated because the view states it too: FootballTrades
	// polls every six hours but this job runs once daily at 15:00 UTC, so a
	// trade graded at 18:45 is not on the dashboard for ~20h. The producer
	// cannot publish it itself — its task has no ./report, and publishSite
	// mirrors that directory with --delete, so a football-trades task that
	// created one would orphan value.json and football.json for the league.
	if scopeWritesPublicDir(scope) {
		if err := renderFootballTradesSite(projSiteOut); err != nil {
			fmt.Fprintf(os.Stderr, "warning: football-trades.json not written: %v\n", err)
		}
	}

	// --open points at value.json: the private reports are no longer files at a
	// predictable path (the store owns their layout), and value.json is the one
	// artifact this command still writes to <out>.
	if projSiteOpen && scopeWritesPublicDir(scope) {
		if err := openInBrowser(cmd.Context(), filepath.Join(projSiteOut, "value.json")); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// publishReport marshals v the way writeJSONModel does — indented JSON, one
// trailing newline — and stores it under key.
//
// Indentation is preserved from the file era on purpose: these bodies are
// passed through GET /v1/reports/{name} untouched, so this is the exact text an
// operator sees when they curl the endpoint to debug a tab.
func publishReport(pub lineupapi.Publisher, key string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode %s report: %w", key, err)
	}
	if err := pub.Publish(key, buf.Bytes()); err != nil {
		return fmt.Errorf("publish %s report: %w", key, err)
	}
	return nil
}

// renderValueSite reads the Team Value Store (S3 when STATE_BUCKET is set, else
// local .teamvalue/) and writes <outDir>/value.json. An empty store still
// writes a valid (empty) model rather than erroring.
func renderValueSite(outDir string) error {
	reader, err := statestore.FromEnv().TeamValueReader()
	if err != nil {
		return fmt.Errorf("init team-value reader: %w", err)
	}
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read team values: %w", err)
	}
	vm := valuereport.BuildModel(rows)
	outPath := filepath.Join(outDir, "value.json")
	if err := writeJSONModel(outPath, vm); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d team-value rows)\n", outPath, len(rows))
	return nil
}

// viewsLimit is how many recent page reads views.json carries. The tab shows a
// recent-hits table, not a history, so this is a display cap rather than a
// sample — and it bounds how many log objects ReadRecent has to open.
const viewsLimit = 200

// renderViewsSite reads the recap site's CloudFront access logs and publishes
// the views report.
//
// Skipped entirely (nothing published, no error) when RECAP_LOG_BUCKET is
// unset, which is the normal local-dev case: CloudFront writes these logs only
// in AWS, so there is nothing to read locally. A deployed run with no readers
// yet still publishes a valid empty model, so the tab can say "no reads yet"
// rather than erroring.
func renderViewsSite(pub lineupapi.Publisher) error {
	store, err := statestore.FromEnv().RecapLogReader()
	if err != nil {
		return fmt.Errorf("init recap-log reader: %w", err)
	}
	if store == nil {
		return nil
	}
	m, err := recaplog.ReadRecent(store, "", viewsLimit, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("read recap logs: %w", err)
	}
	if err := publishReport(pub, lineupapi.ReportViewsKey, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Published report %q (%d recent page reads)\n",
		lineupapi.ReportViewsKey, len(m.Hits))
	return nil
}

// renderGapSite reads the Lineup Gap Store (S3 when STATE_BUCKET is set, else
// local .lineupgap/) and publishes the gap report.
func renderGapSite(pub lineupapi.Publisher) error {
	reader, err := statestore.FromEnv().LineupGapReader()
	if err != nil {
		return fmt.Errorf("init lineup gap reader: %w", err)
	}
	return writeGapModel(reader, pub)
}

// writeGapModel is the I/O-light half of renderGapSite, split out so the model
// and stored shape are testable against a local store with no environment.
//
// An empty store still publishes a valid (empty) model rather than erroring:
// the gap block is the first thing on the Projections tab, and a fresh deploy
// must render "no data yet" rather than a broken headline.
func writeGapModel(reader lineupgap.Reader, pub lineupapi.Publisher) error {
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read lineup gaps: %w", err)
	}
	m := lineupgap.BuildModel(rows, time.Now().UTC())
	if err := publishReport(pub, lineupapi.ReportGapKey, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Published report %q (%d lineup-gap days)\n",
		lineupapi.ReportGapKey, len(rows))
	return nil
}

// renderFootballSite reads the Dynasty Value Store (S3 when STATE_BUCKET is
// set, else local .footballvalue/) and writes <outDir>/football.json. An
// empty store still writes a valid (empty) model rather than erroring.
func renderFootballSite(outDir string) error {
	reader, err := statestore.FromEnv().FootballValueReader()
	if err != nil {
		return fmt.Errorf("init football-value reader: %w", err)
	}
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read football values: %w", err)
	}
	coverage, err := reader.ReadAllCoverage()
	if err != nil {
		return fmt.Errorf("read football coverage: %w", err)
	}
	fm := dynasty.BuildModel(rows, coverage, time.Now())
	outPath := filepath.Join(outDir, "football.json")
	if err := writeJSONModel(outPath, fm); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d football rows, %d teams)\n", outPath, len(rows), len(fm.Teams))
	return nil
}

// renderFootballTradesSite reads the Football Trade Log (S3 when STATE_BUCKET
// is set, else local .football/trades/log/) and writes
// <outDir>/football-trades.json. An empty log still writes a valid (empty)
// model rather than erroring, so a fresh deploy renders "no trades logged yet"
// instead of a broken tab.
func renderFootballTradesSite(outDir string) error {
	reader, err := statestore.FromEnv().FootballTradeLogReader()
	if err != nil {
		return fmt.Errorf("init football trade log reader: %w", err)
	}
	rows, err := reader.ReadAllTrades()
	if err != nil {
		return fmt.Errorf("read football trade log: %w", err)
	}
	m := dynasty.BuildTradeLogModel(rows, time.Now())
	outPath := filepath.Join(outDir, "football-trades.json")
	if err := writeJSONModel(outPath, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d logged rows, %d trades)\n", outPath, len(rows), len(m.Trades))
	return nil
}

// writeJSONModel encodes v as indented JSON at path.
//
// Close is returned rather than deferred-and-dropped because these files are the
// dashboard's entire data feed: json.Encoder buffers, the final flush happens
// inside Close, and entrypoint.sh syncs whatever landed on disk regardless. A
// dropped Close error means a truncated model.json published to CloudFront while
// the run printed "Wrote ..." and exited 0 — a broken dashboard reported as a
// success. On an encode error the Close error is joined rather than dropped: the
// encode failure is the cause and reads first, but a Close that also fails says
// something independent about the disk, which is worth knowing on exactly the
// path where the write already went wrong.
func writeJSONModel(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errors.Join(fmt.Errorf("encode %s: %w", filepath.Base(path), err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
