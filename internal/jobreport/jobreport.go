// Package jobreport is the Job Report (CONTEXT.md): the four-surface output
// of one scheduled job — the run's wire result, stdout, the GHA step summary,
// and the activity feed with its push — emitted in one fixed order with one
// dry-run rule and one failure policy.
//
// Before this package each producer (waivers, claims, prospects,
// transactions, gs-check, and the grade/backtest commands) restated the
// record → print → summary → send sequence by hand, and the copies drifted:
// prospects had no dry-run guard at all, claims advanced its cursor in a dry
// run, three of six read GITHUB_STEP_SUMMARY, and only gs-check treated a
// failed feed write as fatal. The sequence lives here once; a producer keeps
// only its formatters and its wire-result mapping.
package jobreport

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// Report is one job's output across every surface. Job is both the wire job
// type (jobwire) and the notify Kind: the five sending producers already use
// the same string for both, and a Job Report is one thing, not two.
type Report struct {
	// Job names the producer: "waivers", "claims", "prospects",
	// "transactions", "gs-check", "grade", "backtest".
	Job string
	// Wire is the payload recorded for GET /v1/runs/{id}/output. It is
	// recorded first and in every mode, dry run included: a run that produced
	// a result has a result, whether or not it was allowed to act on it.
	Wire any
	// Text renders the stdout report. nil means the job prints its own
	// progress (grade's store-write loop) and uses only the wire surface.
	Text func(io.Writer)
	// Summary renders the GHA step-summary markdown. nil means this job has
	// no summary. It is output, not a side effect, so a dry run still writes it.
	Summary func(io.Writer)
	// Title and Body are the notification. An empty Body means there is
	// nothing worth telling a manager and no send happens; the after-send
	// effects still run.
	Title string
	Body  string
	// Before runs once before the send, only when there is one (Body
	// non-empty), and never in a dry run. gs-check's
	// league broadcast lives here: it must precede the dispatcher send so a
	// transient feed-write failure cannot silence the whole league. Its error
	// is fatal.
	Before func(context.Context) error
	// Effects run after the send, in order, and never in a dry run: a ledger
	// write, a cursor advance. A failure is reported on the run output and the
	// next effect still runs; it is never fatal, because the send already
	// happened and failing the run now would only earn a duplicate on rerun.
	Effects []Effect
}

// Effect is one after-send side effect, named so a failure can say which.
type Effect struct {
	Name string
	Do   func() error
}

// Options is the environment Emit runs in. Every zero value has a production
// default; tests set all four.
type Options struct {
	// DryRun stops the sequence before the first side effect: Before, the
	// send and the Effects are skipped, and stdout says what would have been
	// sent. Recording, printing and the summary still happen.
	DryRun bool
	// Out is where Text, the dry-run line and effect failures go. nil means
	// the os.Stdout in force when Emit is called (the producers' tests swap
	// it around the call).
	Out io.Writer
	// SummaryPath is the GHA step-summary file. "" means read
	// GITHUB_STEP_SUMMARY; "" there too means no summary is written.
	SummaryPath string
	// Send delivers the notification. nil means notify.Send, the dispatcher
	// that writes the durable feed record and fans out to APNs and Pushover.
	// It errors only when the feed write fails, and that error is fatal.
	Send func(context.Context, notify.Event) error
}

func (o Options) withDefaults() Options {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.SummaryPath == "" {
		o.SummaryPath = os.Getenv("GITHUB_STEP_SUMMARY")
	}
	if o.Send == nil {
		o.Send = notify.Send
	}
	return o
}

// Emit runs the Job Report sequence:
//
//	record the wire result
//	print Text to Out
//	append Summary to the summary file, when there is a file and a Summary
//	dry run: print "[dry-run] would send" with the title and body when Body
//	         is non-empty, then return nil
//	when Body is non-empty: Before (fatal on error), then Send (fatal on
//	     error: a failed feed write fails the job so the rerun writes the
//	     record)
//	Effects in order (a failure is reported on Out as a WARNING naming the
//	        effect; the next effect still runs; never fatal)
//
// The order is the whole module. See jobreport_test.go for the contract,
// one behaviour per test.
func Emit(ctx context.Context, r Report, o Options) error {
	o = o.withDefaults()

	jobwire.RecordOutput(r.Job, r.Wire)

	if r.Text != nil {
		r.Text(o.Out)
	}
	if o.SummaryPath != "" && r.Summary != nil {
		notify.AppendGHASummary(o.SummaryPath, r.Summary)
	}

	if o.DryRun {
		if r.Body != "" {
			fmt.Fprintf(o.Out, "\n[dry-run] would send: %s\n%s\n", r.Title, r.Body)
		}
		return nil
	}

	if r.Body != "" {
		if r.Before != nil {
			if err := r.Before(ctx); err != nil {
				return fmt.Errorf("%s: %w", r.Job, err)
			}
		}
		if err := o.Send(ctx, notify.Event{Kind: r.Job, Title: r.Title, Message: r.Body}); err != nil {
			return fmt.Errorf("%s: send: %w", r.Job, err)
		}
	}

	for _, e := range r.Effects {
		if err := e.Do(); err != nil {
			fmt.Fprintf(o.Out, "WARNING: %s: %s: %v\n", r.Job, e.Name, err)
		}
	}
	return nil
}
