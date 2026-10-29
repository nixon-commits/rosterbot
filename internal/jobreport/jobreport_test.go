package jobreport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// harness is one Emit call's observable world: the wire recorder, stdout,
// the GHA summary file, the dispatcher, and the after-send effects. Every
// test asserts on what reached these, never on the fake itself.
type harness struct {
	t        *testing.T
	out      bytes.Buffer
	summary  string // path of the summary file
	recorded []string
	sent     []notify.Event
	sendErr  error
	trace    []string // the order in which the surfaces were touched
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, summary: filepath.Join(t.TempDir(), "summary.md")}
	prev := jobwire.OutputRecorder
	jobwire.OutputRecorder = func(job string, _ any) {
		h.recorded = append(h.recorded, job)
		h.trace = append(h.trace, "record")
	}
	t.Cleanup(func() { jobwire.OutputRecorder = prev })
	return h
}

func (h *harness) options(dryRun bool) Options {
	return Options{
		DryRun:      dryRun,
		Out:         &h.out,
		SummaryPath: h.summary,
		Send: func(_ context.Context, e notify.Event) error {
			h.trace = append(h.trace, "send")
			if h.sendErr != nil {
				return h.sendErr
			}
			h.sent = append(h.sent, e)
			return nil
		},
	}
}

func (h *harness) report() Report {
	return Report{
		Job:   "waivers",
		Wire:  struct{ N int }{N: 3},
		Text:  func(w io.Writer) { h.trace = append(h.trace, "print"); _, _ = io.WriteString(w, "three picks\n") },
		Title: "Waiver picks",
		Body:  "Add Someone, drop Nobody",
	}
}

func (h *harness) effect(name string, err error) Effect {
	return Effect{Name: name, Do: func() error {
		h.trace = append(h.trace, "effect:"+name)
		return err
	}}
}

func (h *harness) summaryText() string {
	h.t.Helper()
	b, err := os.ReadFile(h.summary)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		h.t.Fatalf("read summary: %v", err)
	}
	return string(b)
}

func TestEmit_LiveRunTouchesEverySurfaceInOrder(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.summary, []byte("## Earlier job\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.report()
	r.Summary = func(w io.Writer) { h.trace = append(h.trace, "summary"); _, _ = io.WriteString(w, "## Waiver picks\n") }
	r.Before = func(context.Context) error { h.trace = append(h.trace, "before"); return nil }
	r.Effects = []Effect{h.effect("ledger", nil), h.effect("cursor", nil)}

	if err := Emit(t.Context(), r, h.options(false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	want := []string{"record", "print", "summary", "before", "send", "effect:ledger", "effect:cursor"}
	if got := strings.Join(h.trace, " "); got != strings.Join(want, " ") {
		t.Errorf("order = %s\nwant    %s", got, strings.Join(want, " "))
	}
	if !strings.Contains(h.out.String(), "three picks") {
		t.Errorf("stdout missing the report text; got %q", h.out.String())
	}
	if got := h.summaryText(); !strings.Contains(got, "## Earlier job") || !strings.Contains(got, "## Waiver picks") {
		t.Errorf("summary must be appended after the earlier job's section; got %q", got)
	}
	if len(h.sent) != 1 || h.sent[0].Kind != "waivers" || h.sent[0].Title != "Waiver picks" || h.sent[0].Message != "Add Someone, drop Nobody" {
		t.Errorf("sent = %+v, want one waivers event carrying the title and body", h.sent)
	}
	if len(h.recorded) != 1 || h.recorded[0] != "waivers" {
		t.Errorf("recorded = %v, want [waivers]", h.recorded)
	}
}

func TestEmit_DryRunStopsBeforeTheFirstSideEffect(t *testing.T) {
	h := newHarness(t)
	r := h.report()
	r.Summary = func(w io.Writer) { _, _ = io.WriteString(w, "## Waiver picks\n") }
	r.Before = func(context.Context) error { h.trace = append(h.trace, "before"); return nil }
	r.Effects = []Effect{h.effect("ledger", nil), h.effect("cursor", nil)}

	if err := Emit(t.Context(), r, h.options(true)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	for _, step := range h.trace {
		if step == "before" || step == "send" || strings.HasPrefix(step, "effect:") {
			t.Errorf("dry run reached %q; trace = %v", step, h.trace)
		}
	}
	if len(h.recorded) != 1 {
		t.Errorf("dry run must still record the wire result; recorded = %v", h.recorded)
	}
	if h.summaryText() == "" {
		t.Error("dry run must still write the GHA summary (it is output, not a side effect)")
	}
	out := h.out.String()
	if !strings.Contains(out, "Waiver picks") || !strings.Contains(out, "Add Someone, drop Nobody") {
		t.Errorf("dry run must say what it would have sent (title and body); got %q", out)
	}
}

func TestEmit_DryRunWithNothingToSendPrintsNoWouldSendLine(t *testing.T) {
	h := newHarness(t)
	r := h.report()
	r.Body = ""

	if err := Emit(t.Context(), r, h.options(true)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if strings.Contains(strings.ToLower(h.out.String()), "would send") {
		t.Errorf("nothing to send, yet stdout claims a send: %q", h.out.String())
	}
}

func TestEmit_EmptyBodySkipsTheSendButRunsTheEffects(t *testing.T) {
	h := newHarness(t)
	r := h.report()
	r.Body = ""
	r.Before = func(context.Context) error { h.trace = append(h.trace, "before"); return nil }
	r.Effects = []Effect{h.effect("cursor", nil)}

	if err := Emit(t.Context(), r, h.options(false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(h.sent) != 0 {
		t.Errorf("empty body must not send; sent = %+v", h.sent)
	}
	if strings.Contains(strings.Join(h.trace, " "), "before") {
		t.Errorf("with nothing to send there is nothing to precede: Before must not run; trace = %v", h.trace)
	}
	if got := strings.Join(h.trace, " "); !strings.HasSuffix(got, "effect:cursor") {
		t.Errorf("cursor effect must still run on an empty body; trace = %v", h.trace)
	}
}

func TestEmit_BeforeFailureIsFatalAndStopsTheSend(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("pushover group: 503")
	r := h.report()
	r.Before = func(context.Context) error { h.trace = append(h.trace, "before"); return boom }
	r.Effects = []Effect{h.effect("cursor", nil)}

	err := Emit(t.Context(), r, h.options(false))
	if !errors.Is(err, boom) {
		t.Fatalf("Emit error = %v, want it to wrap the Before error", err)
	}
	if len(h.sent) != 0 || strings.Contains(strings.Join(h.trace, " "), "effect:") {
		t.Errorf("a failed Before must stop the send and the effects; sent = %+v trace = %v", h.sent, h.trace)
	}
}

func TestEmit_FeedWriteFailureIsFatalAndStopsTheEffects(t *testing.T) {
	h := newHarness(t)
	h.sendErr = errors.New("notify: write feed record: s3 down")
	r := h.report()
	r.Effects = []Effect{h.effect("ledger", nil), h.effect("cursor", nil)}

	err := Emit(t.Context(), r, h.options(false))
	if !errors.Is(err, h.sendErr) {
		t.Fatalf("Emit error = %v, want it to wrap the send error", err)
	}
	if strings.Contains(strings.Join(h.trace, " "), "effect:") {
		t.Errorf("a failed feed write must leave the cursor alone so the rerun re-processes; trace = %v", h.trace)
	}
}

func TestEmit_EffectFailureIsReportedAndTheNextEffectStillRuns(t *testing.T) {
	h := newHarness(t)
	r := h.report()
	r.Effects = []Effect{h.effect("ledger", errors.New("disk full")), h.effect("cursor", nil)}

	if err := Emit(t.Context(), r, h.options(false)); err != nil {
		t.Fatalf("an effect failure is never fatal; Emit = %v", err)
	}
	if got := strings.Join(h.trace, " "); !strings.HasSuffix(got, "effect:ledger effect:cursor") {
		t.Errorf("cursor must still run after the ledger fails; trace = %v", h.trace)
	}
	out := h.out.String()
	if !strings.Contains(out, "ledger") || !strings.Contains(out, "disk full") {
		t.Errorf("the failed effect must be named with its error on the run output; got %q", out)
	}
}

func TestEmit_NilSummaryWritesNoFile(t *testing.T) {
	h := newHarness(t)
	r := h.report() // Summary nil

	if err := Emit(t.Context(), r, h.options(false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if _, err := os.Stat(h.summary); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a job with no summary must not touch the summary file; stat = %v", err)
	}
}

func TestEmit_SummaryPathDefaultsToTheGHAEnvVar(t *testing.T) {
	h := newHarness(t)
	envPath := filepath.Join(t.TempDir(), "gha.md")
	t.Setenv("GITHUB_STEP_SUMMARY", envPath)
	r := h.report()
	r.Summary = func(w io.Writer) { _, _ = io.WriteString(w, "## from env\n") }
	o := h.options(false)
	o.SummaryPath = ""

	if err := Emit(t.Context(), r, o); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("summary not written to $GITHUB_STEP_SUMMARY: %v", err)
	}
	if !strings.Contains(string(b), "## from env") {
		t.Errorf("summary = %q", b)
	}
}

func TestEmit_NoSummaryPathAnywhereWritesNothing(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	r := h.report()
	r.Summary = func(w io.Writer) { h.trace = append(h.trace, "summary") }
	o := h.options(false)
	o.SummaryPath = ""

	if err := Emit(t.Context(), r, o); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if strings.Contains(strings.Join(h.trace, " "), "summary") {
		t.Error("with no summary path the renderer must not be called at all")
	}
}

func TestEmit_NilTextPrintsNothing(t *testing.T) {
	h := newHarness(t)
	r := h.report()
	r.Text = nil
	r.Body = ""

	if err := Emit(t.Context(), r, h.options(false)); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if h.out.Len() != 0 {
		t.Errorf("a wire-only job must print nothing; got %q", h.out.String())
	}
	if len(h.recorded) != 1 {
		t.Errorf("a wire-only job must still record; recorded = %v", h.recorded)
	}
}

// The producers' own tests capture os.Stdout around the call (gscheck's
// captureStdout), so the default writer has to be looked up when Emit runs,
// not when the package initialises.
func TestEmit_NilOutIsStdoutAtCallTime(t *testing.T) {
	h := newHarness(t)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = pw
	t.Cleanup(func() { os.Stdout = orig })

	o := h.options(false)
	o.Out = nil
	r := h.report()
	r.Body = ""
	emitErr := Emit(t.Context(), r, o)
	if cerr := pw.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	os.Stdout = orig
	got, rerr := io.ReadAll(pr)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if emitErr != nil {
		t.Fatalf("Emit: %v", emitErr)
	}
	if !strings.Contains(string(got), "three picks") {
		t.Errorf("default writer must be the os.Stdout in force at call time; captured %q", got)
	}
}
