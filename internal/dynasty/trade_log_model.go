package dynasty

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// TradeLogModel is the football-trades.json payload consumed by the dashboard
// SPA: every graded trade the log covers, newest first.
//
// It is a camelCase view of TradeLogRow rather than the stored rows passed
// through, matching every other dashboard model in this repo (dynasty.Model,
// valuereport.Model) while the durable NDJSON keeps the snake_case store
// convention it shares with Row and tradeboard.LogRow. The one place both
// conventions meet is FormatValues, whose keys are format IDENTIFIERS — the
// same strings as DYNASTY_FORMAT and the tab's toggle — not field names.
type TradeLogModel struct {
	Empty       bool            `json:"empty"`
	GeneratedAt string          `json:"generatedAt"`
	Trades      []TradeLogEntry `json:"trades"`
	// CoversFrom is the earliest TRADE date in the list — what the list actually
	// covers, which is the only reading the view can make an honest sentence out
	// of. It is deliberately NOT the earliest partition date: Dt is the GRADE
	// date, and --relog writes every rebuilt row into today's partition, so a
	// grade-date reading would print "logged from 2026-08-18 onward" directly
	// above a card headed 2026-01-23.
	CoversFrom string `json:"coversFrom,omitempty"`
	// Regraded counts rows rebuilt after the fact rather than captured when the
	// alert went out. Non-zero means part of this list carries today's prices,
	// which the view must say rather than let the reader assume otherwise.
	Regraded int `json:"regraded"`
}

// TradeLogEntry is one graded trade, as the dashboard reads it.
type TradeLogEntry struct {
	TransactionID string `json:"transactionId"`
	// TradeDate is YYYY-MM-DD, or "" when Sleeper gave no usable timestamp.
	// Empty means unknown; it never silently becomes the epoch.
	TradeDate string `json:"tradeDate,omitempty"`
	GradedAt  string `json:"gradedAt"`
	// AlertFormat names which of the four verdicts was the one Pushover sent.
	AlertFormat     string                          `json:"alertFormat,omitempty"`
	Regraded        bool                            `json:"regraded,omitempty"`
	OriginalSummary string                          `json:"originalSummary,omitempty"`
	Sides           []TradeLogEntrySide             `json:"sides"`
	Verdicts        map[string]TradeLogEntryVerdict `json:"verdicts"`
}

// TradeLogEntrySide is one side of a trade as the dashboard reads it.
type TradeLogEntrySide struct {
	TeamID   string                `json:"teamId"`
	TeamName string                `json:"teamName"`
	Assets   []TradeLogEntryAsset  `json:"assets"`
	Totals   statsguy.FormatValues `json:"totals"`
}

// TradeLogEntryAsset is one asset as the dashboard reads it.
type TradeLogEntryAsset struct {
	Name   string                `json:"name"`
	Values statsguy.FormatValues `json:"values"`
	Priced bool                  `json:"priced"`
}

// TradeLogEntryVerdict is one format's grade as the dashboard reads it.
type TradeLogEntryVerdict struct {
	Status          string  `json:"status"`
	FavoredTeamID   string  `json:"favoredTeamId,omitempty"`
	FavoredTeamName string  `json:"favoredTeamName,omitempty"`
	Pct             float64 `json:"pct,omitempty"`
	UnpricedAssets  int     `json:"unpricedAssets,omitempty"`
	Summary         string  `json:"summary"`
}

// BuildTradeLogModel reshapes the stored log for the dashboard. Pure: no I/O.
//
// Rows are deduped across partitions before display, so a trade regraded after
// a marker read failure appears once — as the grade that was actually alerted.
// Ordering is by trade date descending (a trade log is read newest-first), with
// unknown-dated rows last rather than sorted to the top as year zero.
func BuildTradeLogModel(rows []TradeLogRow, generatedAt time.Time) *TradeLogModel {
	if len(rows) == 0 {
		return &TradeLogModel{Empty: true, GeneratedAt: generatedAt.UTC().Format(time.RFC3339)}
	}

	deduped := DedupeTradeLog(rows)

	entries := make([]TradeLogEntry, 0, len(deduped))
	coversFrom, regraded := "", 0
	for _, r := range deduped {
		e := entryFor(r)
		if e.TradeDate != "" && (coversFrom == "" || e.TradeDate < coversFrom) {
			coversFrom = e.TradeDate
		}
		if e.Regraded {
			regraded++
		}
		entries = append(entries, e)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		// Unknown trade dates sink to the bottom: an empty string would
		// otherwise sort as the oldest possible date under a plain compare.
		if (a.TradeDate == "") != (b.TradeDate == "") {
			return b.TradeDate == ""
		}
		if a.TradeDate != b.TradeDate {
			return a.TradeDate > b.TradeDate
		}
		return a.TransactionID > b.TransactionID
	})

	return &TradeLogModel{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Trades:      entries,
		CoversFrom:  coversFrom,
		Regraded:    regraded,
	}
}

func entryFor(r TradeLogRow) TradeLogEntry {
	sides := make([]TradeLogEntrySide, 0, len(r.Sides))
	for _, s := range r.Sides {
		assets := make([]TradeLogEntryAsset, 0, len(s.Assets))
		for _, a := range s.Assets {
			assets = append(assets, TradeLogEntryAsset{Name: a.Name, Values: a.Values, Priced: a.Priced})
		}
		sides = append(sides, TradeLogEntrySide{
			TeamID:   s.TeamID,
			TeamName: s.TeamName,
			Assets:   assets,
			Totals:   s.Totals,
		})
	}

	verdicts := make(map[string]TradeLogEntryVerdict, len(r.Verdicts))
	for f, v := range r.Verdicts {
		verdicts[f] = TradeLogEntryVerdict{
			Status:          string(v.Status),
			FavoredTeamID:   v.FavoredTeamID,
			FavoredTeamName: v.FavoredTeamName,
			Pct:             v.Pct,
			UnpricedAssets:  v.UnpricedAssets,
			Summary:         v.Summary,
		}
	}

	tradeDate := ""
	if !r.TradeDate.IsZero() {
		tradeDate = r.TradeDate.UTC().Format("2006-01-02")
	}

	return TradeLogEntry{
		TransactionID:   r.TransactionID,
		TradeDate:       tradeDate,
		GradedAt:        r.GradedAt.UTC().Format("2006-01-02"),
		AlertFormat:     r.AlertFormat,
		Regraded:        r.Regraded,
		OriginalSummary: r.OriginalSummary,
		Sides:           sides,
		Verdicts:        verdicts,
	}
}
