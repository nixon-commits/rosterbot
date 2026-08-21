package dynasty

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// tradeLogFilename is the leaf object name in every partition.
const tradeLogFilename = "trades.ndjson"

// TradeLogFormats is every StatsGuy format a logged trade is priced in.
//
// All four, not just DYNASTY_FORMAT: the dashboard's Football tab already
// carries a four-way format toggle, and a row priced in one format would be
// silently relabelled by three of the four toggle positions. Same reasoning as
// Row's four value leaves.
var TradeLogFormats = []string{"sf_dynasty", "non_sf_dynasty", "sf_redraft", "non_sf_redraft"}

// TradeLogAsset is one asset as it was priced when the trade was graded.
type TradeLogAsset struct {
	Name string `json:"name"`
	// Values are the StatsGuy prices AT GRADE TIME. The JSON keys are the four
	// format identifiers (matching DYNASTY_FORMAT and the dashboard toggle),
	// not field names, so they stay snake_case in the camelCase view model too.
	Values statsguy.FormatValues `json:"values"`
	// Priced records whether the asset joined the bundle at all. False means
	// there is no number, not a number of zero — FAAB, and any pick tier
	// StatsGuy does not list.
	Priced bool `json:"priced"`
}

// TradeLogSide is what one roster received, with its priced totals.
type TradeLogSide struct {
	TeamID   string          `json:"team_id"`
	TeamName string          `json:"team_name"`
	Assets   []TradeLogAsset `json:"assets"`
	// Totals is the sum of this side's PRICED assets per format. On an
	// incomplete verdict it is a floor, not a total, which is why the verdict
	// rather than this number is what the view leads with.
	Totals statsguy.FormatValues `json:"totals"`
}

// TradeLogVerdict is one format's grade, with the exact string the alert used.
//
// Summary is stored rather than re-derived because the Pushover alert renders
// Pct at %.0f: a reader formatting the float itself would eventually disagree
// with what was actually sent, and the log's whole purpose is to be the record
// of what was reported.
type TradeLogVerdict struct {
	Status          TradeStatus `json:"status"`
	FavoredTeamID   string      `json:"favored_team_id,omitempty"`
	FavoredTeamName string      `json:"favored_team_name,omitempty"`
	Pct             float64     `json:"pct,omitempty"`
	UnpricedAssets  int         `json:"unpriced_assets,omitempty"`
	Summary         string      `json:"summary"`
}

// TradeLogRow is one completed Sleeper trade as graded, in the durable log.
//
// THE INVARIANT THIS TYPE EXISTS FOR: every value here is as priced AT GRADE
// TIME. StatsGuy publishes no history, so re-deriving a trade from Sleeper plus
// the current bundle answers "what would this be worth today" while presenting
// itself as "what was this worth then". Sleeper does retain completed trades
// indefinitely, so the trade's IDENTITY is recoverable forever — its GRADE is
// not. Same reasoning as internal/tradeboard's offer log (HKB moves daily) and
// as docs/adr/0002, one sport down.
//
// Dt is the GRADE date, not the trade date. The two differ by months for a
// backfilled row, and the partition has to mean "when these numbers were
// true" for the invariant above to hold; TradeDate carries when the trade
// actually happened, so nothing is lost.
type TradeLogRow struct {
	Dt            string `json:"dt"`
	TransactionID string `json:"transaction_id"`
	// TradeDate is when Sleeper says the trade completed. Zero when the
	// transaction carried no usable Created timestamp — read it as unknown,
	// never as the epoch.
	TradeDate time.Time `json:"trade_date"`
	GradedAt  time.Time `json:"graded_at"`
	// AlertFormat is the DYNASTY_FORMAT the Pushover alert was rendered in, so
	// a reader can tell which of the four verdicts was the one actually sent.
	AlertFormat string                     `json:"alert_format"`
	Sides       []TradeLogSide             `json:"sides"`
	Verdicts    map[string]TradeLogVerdict `json:"verdicts"`

	// Regraded marks a row reconstructed by `football-trades --relog` rather
	// than captured when the alert went out: identity from Sleeper (accurate),
	// values from the bundle at RELOG time (not what they were worth then).
	// The view must say so — an unlabelled re-price is exactly the silent
	// mislabelling this whole store exists to prevent.
	Regraded bool `json:"regraded,omitempty"`
	// OriginalSummary is the dedup marker's body: the one surviving grade-time
	// artifact for a trade alerted before this log existed. Set only on
	// regraded rows, and only when a marker was found.
	OriginalSummary string `json:"original_summary,omitempty"`
}

// TradeVerdictSummary renders a verdict the way the Pushover alert titles it.
//
// One renderer, two consumers: the alert title and the durable log row. A
// second copy is how the stored summary and the sent message drift apart.
func TradeVerdictSummary(v TradeVerdict) string {
	if v.Status == TradeFavors {
		return fmt.Sprintf("favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	}
	return string(v.Status)
}

// BuildTradeLogRow grades one completed trade in every StatsGuy format and
// returns the durable row. Pure: no I/O, no clock read (gradedAt is supplied).
//
// The four verdicts come from four projections of ONE BuildTradeSidesAll call,
// so every format sees the identical asset set — building the sides per format
// would re-iterate txn.Adds, whose order Go randomizes, and index-aligning
// those results would eventually pair a name with another asset's value.
func BuildTradeLogRow(gradedAt time.Time, txn sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, teamNamesByRosterID map[int]string, alertFormat string) TradeLogRow {
	all := BuildTradeSidesAll(txn, players, bundle, teamNamesByRosterID)

	sides := make([]TradeLogSide, 0, len(all))
	for _, s := range all {
		assets := make([]TradeLogAsset, 0, len(s.Assets))
		for _, a := range s.Assets {
			assets = append(assets, TradeLogAsset{Name: a.Name, Values: a.Values, Priced: a.Priced})
		}
		sides = append(sides, TradeLogSide{
			TeamID:   s.TeamID,
			TeamName: s.TeamName,
			Assets:   assets,
			Totals:   s.Totals(),
		})
	}

	verdicts := make(map[string]TradeLogVerdict, len(TradeLogFormats))
	for _, f := range TradeLogFormats {
		v := GradeTrade(ProjectTradeSides(all, f))
		verdicts[f] = TradeLogVerdict{
			Status:          v.Status,
			FavoredTeamID:   v.FavoredTeamID,
			FavoredTeamName: v.FavoredTeamName,
			Pct:             v.Pct,
			UnpricedAssets:  v.UnpricedAssets,
			Summary:         TradeVerdictSummary(v),
		}
	}

	var tradeDate time.Time
	if txn.Created > 0 {
		tradeDate = time.UnixMilli(txn.Created).UTC()
	}

	return TradeLogRow{
		Dt:            gradedAt.UTC().Format("2006-01-02"),
		TransactionID: txn.TransactionID,
		TradeDate:     tradeDate,
		GradedAt:      gradedAt.UTC(),
		AlertFormat:   alertFormat,
		Sides:         sides,
		Verdicts:      verdicts,
	}
}

// MergeTradeLog folds freshly graded rows into the rows already in a partition.
//
// ndjsonstore.Write is a WHOLE-OBJECT overwrite, and the producer polls every
// six hours: two trades landing on the same UTC day in different polls are
// disjoint graded sets, so writing the second set bare would delete the first
// with no error and a cheerful success summary. Every write to a partition must
// go read -> merge -> write. Precedent: cmd/trades.go's appendOfferLog.
//
// Keyed on TransactionID, and on a collision the PRIOR row wins. A trade is
// normally graded once — the dedup marker sees to that — but a marker READ
// error falls through to grade-and-send, so the same transaction can be graded
// twice in one day. The first row is the one whose values match the alert that
// actually went out, and that is the row the invariant promises.
func MergeTradeLog(prior, fresh []TradeLogRow) []TradeLogRow {
	byID := make(map[string]TradeLogRow, len(prior)+len(fresh))
	for _, r := range prior {
		byID[r.TransactionID] = r
	}
	for _, r := range fresh {
		if _, seen := byID[r.TransactionID]; seen {
			continue // prior wins: it is the grade the alert reported
		}
		byID[r.TransactionID] = r
	}

	out := make([]TradeLogRow, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TransactionID < out[j].TransactionID })
	return out
}

// DedupeTradeLog collapses the whole log to one row per TransactionID, keeping
// the best available record: a CAPTURED row always beats a regraded one, and
// among rows of the same kind the EARLIEST grade wins.
//
// MergeTradeLog only dedupes within a partition. Two rows for one transaction
// can still land in different partitions — a marker read failure on a later day
// regrades a trade already logged on an earlier one — and a re-priced duplicate
// must never displace the row captured when the alert was sent. Earliest wins
// for the same reason prior wins in MergeTradeLog.
//
// THE CAPTURED-BEATS-REGRADED RULE IS NOT REDUNDANT WITH EARLIEST-WINS, and
// leaving it out inverts the whole invariant in one specific case: --relog runs
// today over a trade that has not been alerted yet, writing a regraded row at
// today's prices; the next poll then alerts that trade for real and writes a
// genuine grade-time row on a LATER day. Earliest-wins alone would keep the
// re-price and permanently discard the real capture — the exact substitution
// this store exists to prevent, arrived at from the opposite direction.
// relogFootballTrades also refuses to touch an unalerted trade, so this is the
// second of two independent guards rather than the only one.
func DedupeTradeLog(rows []TradeLogRow) []TradeLogRow {
	byID := make(map[string]TradeLogRow, len(rows))
	for _, r := range rows {
		prev, seen := byID[r.TransactionID]
		if seen && !betterTradeLogRow(r, prev) {
			continue
		}
		byID[r.TransactionID] = r
	}
	out := make([]TradeLogRow, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TransactionID < out[j].TransactionID })
	return out
}

// betterTradeLogRow reports whether a should replace b as the record of one
// trade. A captured row beats a regraded one outright; otherwise the earlier
// grade wins.
func betterTradeLogRow(a, b TradeLogRow) bool {
	if a.Regraded != b.Regraded {
		return !a.Regraded
	}
	return a.GradedAt.Before(b.GradedAt)
}

// TradeLogWriter persists a day's graded trades.
//
// Deliberately NOT a method on Writer (the Dynasty Value Store's). That writer
// is rooted at analysis/football-values/ and this log lives under
// football/trades/log/, so one interface would mean two instances on which
// half the methods must never be called.
type TradeLogWriter interface {
	WriteTradeLog(date time.Time, rows []TradeLogRow) error
}

// TradeLogReader loads the whole trade log.
type TradeLogReader interface {
	ReadAllTrades() ([]TradeLogRow, error)
}

func tradeLogObjectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), tradeLogFilename)
}

// TradeLogObjectKey is the store-relative partition key (dt=YYYY-MM-DD/trades.ndjson).
func TradeLogObjectKey(date time.Time) string { return tradeLogObjectKey(date) }

type tradeLogWriter struct{ store ndjsonstore.Store }

// NewTradeLogWriter returns a TradeLogWriter persisting rows to store.
func NewTradeLogWriter(store ndjsonstore.Store) TradeLogWriter { return tradeLogWriter{store: store} }

// NewFileTradeLogWriter returns a TradeLogWriter over a local directory root.
func NewFileTradeLogWriter(root string) TradeLogWriter {
	return NewTradeLogWriter(ndjsonstore.NewFileStore(root))
}

func (w tradeLogWriter) WriteTradeLog(date time.Time, rows []TradeLogRow) error {
	return ndjsonstore.Write(w.store, tradeLogObjectKey(date), rows)
}

type tradeLogReader struct{ store ndjsonstore.Store }

// NewTradeLogReader returns a TradeLogReader over rows in store.
func NewTradeLogReader(store ndjsonstore.Store) TradeLogReader { return tradeLogReader{store: store} }

// NewFileTradeLogReader returns a TradeLogReader over a local directory root.
func NewFileTradeLogReader(root string) TradeLogReader {
	return NewTradeLogReader(ndjsonstore.NewFileStore(root))
}

func (r tradeLogReader) ReadAllTrades() ([]TradeLogRow, error) {
	// Empty prefix: every dt= partition sits directly under the store root.
	return ndjsonstore.ReadAll[TradeLogRow](r.store, "", tradeLogFilename, nil)
}
