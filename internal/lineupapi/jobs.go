package lineupapi

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Param describes one tunable flag the app can present as a form field and the
// backend will validate before turning into argv. Type drives both rendering
// and validation: bool (switch), int (stepper, Min/Max), enum (picker,
// Options), text (validated against Pattern), date (a single calendar day),
// daterange (a day or an inclusive span, joined with ":").
//
// date and daterange exist as their own types rather than as text with a
// pattern because the pattern only ever described the shape. `2026-02-31` and
// `2026-13-01` both match dateOrRange and both fail in the CLI several minutes
// into an ECS task — so these two parse the value as a real calendar date, and
// daterange additionally rejects a backwards span. They also give the client
// something to render a native picker from, which is the difference between
// typing a range by hand and choosing one.
type Param struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
	Min     *int     `json:"min,omitempty"`
	Max     *int     `json:"max,omitempty"`
	Help    string   `json:"help,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
}

// JobSpec is one triggerable job plus the params the app may set. Base + build
// stay server-side (unexported) so the CLI mapping is never client-controlled.
type JobSpec struct {
	Name        string  `json:"name"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Mutating    bool    `json:"mutating"` // true => app should confirm (changes real state)
	Params      []Param `json:"params"`

	base  []string
	build func(spec JobSpec, params map[string]string) ([]string, error)
}

// JobsResponse is the GET /v1/jobs body — the schema the app renders forms from.
type JobsResponse struct {
	Jobs []JobSpec `json:"jobs"`
}

func intp(n int) *int { return &n }

// csvCodes matches a comma-separated list of alphanumeric codes (e.g. OF,SP).
var csvCodes = `^[A-Za-z0-9]+(,[A-Za-z0-9]+)*$`

var projectionSystems = []string{"steamer", "depthcharts", "thebatx", "atc", "steamer-ros", "depthcharts-ros", "thebatx-ros", "atc-ros"}

// jobSpecs is the allowlist. Each spec's build closure is the ONLY way params
// become argv, so an unknown name or an invalid value can never reach the CLI.
var jobSpecs = map[string]JobSpec{
	"optimize": {
		Name: "optimize", Label: "Optimize Lineup", Mutating: true,
		Description: "Set the optimal lineup. Applies changes to your real Fantrax roster.",
		Params: []Param{
			{Name: "period", Label: "Period", Type: "enum", Default: "matchup",
				Options: []string{"today", "matchup", "all", "custom"},
				Help:    "today, the rest of this matchup week, the whole season, or a custom date/range"},
			{Name: "dates", Label: "Custom date / range", Type: "daterange",
				Help: "Used when Period = custom"},
			{Name: "projections", Label: "Projection system", Type: "enum", Options: projectionSystems},
			{Name: "dry_run", Label: "Dry run (preview only)", Type: "bool"},
		},
		build: buildOptimize,
	},
	"backtest": {
		Name: "backtest", Label: "Backtest",
		Description: "Grade past lineups + projection accuracy. Read-only.",
		base:        []string{"backtest"},
		Params: []Param{
			{Name: "dates", Label: "Window", Type: "daterange",
				Help: "Defaults to the last completed week"},
			{Name: "skip_projections", Label: "Skip projection grading", Type: "bool"},
			{Name: "recency_experiment", Label: "Recency strategy comparison", Type: "bool"},
		},
	},
	"waivers": {
		Name: "waivers", Label: "Waivers", Mutating: true,
		Description: "Statcast-driven free-agent picks. Sends a push.",
		base:        []string{"waivers"},
		Params: []Param{
			{Name: "top", Label: "How many", Type: "int", Default: "15", Min: intp(1), Max: intp(100)},
			{Name: "positions", Label: "Positions", Type: "text", Pattern: csvCodes, Help: "e.g. OF,SP"},
			{Name: "dry_run", Label: "Dry run (no push)", Type: "bool"},
		},
	},
	"prospects": {
		Name: "prospects", Label: "Prospects", Description: "Prospect call-up / breakout report.",
		base:   []string{"prospects"},
		Params: []Param{{Name: "dry_run", Label: "Dry run (no push)", Type: "bool"}},
	},
	"claims": {
		Name: "claims", Label: "Claims", Mutating: true, Description: "League-wide waiver/FA claim recap. Sends a push.",
		base: []string{"claims"},
		Params: []Param{
			{Name: "dry_run", Label: "Dry run (no push)", Type: "bool"},
			{Name: "no_signals", Label: "Skip Statcast signals", Type: "bool"},
		},
	},
	"gs-check": {
		Name: "gs-check", Label: "GS Check", Mutating: true, Description: "League-wide game-start violation check. Sends a push.",
		base: []string{"gs-check"},
		Params: []Param{
			{Name: "dry_run", Label: "Dry run (no push)", Type: "bool"},
			{Name: "force", Label: "Force (ignore period-end gate)", Type: "bool"},
		},
	},
	"transactions": {
		Name: "transactions", Label: "Transactions", Mutating: true, Description: "Recent trades with HKB valuations. Sends a push.",
		base:   []string{"transactions"},
		Params: []Param{{Name: "dry_run", Label: "Dry run (no push)", Type: "bool"}},
	},
	"grade": {
		Name: "grade", Label: "Grade", Description: "Write graded snapshots to the Analysis Store.",
		base:   []string{"grade"},
		Params: []Param{{Name: "dry_run", Label: "Dry run", Type: "bool"}},
	},
	"recap-site": {
		Name: "recap-site", Label: "Recap Site", Description: "Rebuild the weekly recap site.",
		base: []string{"recap-site", "--out", "dist"},
	},

	// The jobs below run on their own EventBridge schedules and were previously
	// only reachable by waiting for the next one. None of them touch Fantrax or
	// send a push, so none is Mutating — matching `grade`, which likewise writes
	// durable analysis data. `--dry-run` is a persistent root flag, so every
	// command accepts it even where cobra declares no local flag of its own.
	"team-values": {
		Name: "team-values", Label: "Team Values",
		Description: "Append today's per-team HKB dynasty value to the Team Value Store. Feeds the Value tab.",
		base:        []string{"team-values"},
		Params: []Param{
			// --date only chooses the partition; the data captured is always
			// current HKB + rosters, which is why this is not a backfill.
			{Name: "date", Label: "Partition date", Type: "date",
				Help: "Which day's partition to write. Data is always current, so this does not backfill the past."},
			{Name: "dry_run", Label: "Dry run", Type: "bool"},
		},
	},
	"archive": {
		Name: "archive", Label: "Archive",
		Description: "Snapshot today's HKB, projections, Savant and prospect data. Append-only, kept forever.",
		base:        []string{"archive"},
		Params: []Param{
			{Name: "date", Label: "Capture date", Type: "date", Help: "Defaults to today (UTC)"},
			{Name: "dry_run", Label: "Dry run (fetch and report sizes only)", Type: "bool"},
		},
	},
	"shadow": {
		Name: "shadow", Label: "Shadow Capture",
		Description: "Capture projections from all four rest-of-season systems for the model comparison.",
		base:        []string{"shadow"},
		Params: []Param{
			{Name: "dates", Label: "Capture date", Type: "date", Help: "Defaults to today"},
		},
	},
	"projection-site": {
		Name: "projection-site", Label: "Projection Site",
		Description: "Rebuild the Projections, Value and Views data the dashboard reads.",
		base:        []string{"projection-site"},
	},
	"version-check": {
		Name: "version-check", Label: "Version Check",
		Description: "Probe Fantrax with the pinned API version. Read-only; fails if the version has gone stale.",
		base:        []string{"version-check"},
	},
}

// genericBuild maps validated params onto base args via each param's flag form
// (--name for hyphenated names). Used by every job except optimize.
func genericBuild(spec JobSpec, params map[string]string) ([]string, error) {
	args := append([]string{}, spec.base...)
	for _, p := range spec.Params {
		v := params[p.Name]
		if v == "" {
			v = p.Default
		}
		if v == "" {
			continue
		}
		flag := "--" + strings.ReplaceAll(p.Name, "_", "-")
		val, emitValue, err := validateParam(p, v)
		if err != nil {
			return nil, err
		}
		if p.Type == "bool" {
			if emitValue { // bool true
				args = append(args, flag)
			}
			continue
		}
		args = append(args, flag, val)
	}
	return args, nil
}

// buildOptimize handles optimize's period -> mutually-exclusive flags.
func buildOptimize(spec JobSpec, params map[string]string) ([]string, error) {
	args := []string{"optimize"}
	period := params["period"]
	if period == "" {
		period = "matchup"
	}
	switch period {
	case "today":
		// today is the optimizer's default; no flag.
	case "matchup":
		args = append(args, "--matchup")
	case "all":
		args = append(args, "--dates", "all")
	case "custom":
		d := params["dates"]
		if err := checkDateRange(d); err != nil {
			return nil, fmt.Errorf("custom period needs a valid date or range: %w", err)
		}
		args = append(args, "--dates", d)
	default:
		return nil, fmt.Errorf("invalid period: %s", period)
	}
	if pr := params["projections"]; pr != "" {
		if !contains(projectionSystems, pr) {
			return nil, fmt.Errorf("invalid projection system: %s", pr)
		}
		args = append(args, "--projections", pr)
	}
	if isTrue(params["dry_run"]) {
		args = append(args, "--dry-run")
	}
	return args, nil
}

// validateParam validates one value against its param. Returns the value to
// emit, whether to emit it (bool true), and any error. Text values are pattern-
// checked and must not look like a flag.
func validateParam(p Param, v string) (string, bool, error) {
	switch p.Type {
	case "bool":
		return "", isTrue(v), nil
	case "int":
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", false, fmt.Errorf("%s must be a number", p.Name)
		}
		if (p.Min != nil && n < *p.Min) || (p.Max != nil && n > *p.Max) {
			return "", false, fmt.Errorf("%s out of range", p.Name)
		}
		return strconv.Itoa(n), true, nil
	case "enum":
		if !contains(p.Options, v) {
			return "", false, fmt.Errorf("%s must be one of %s", p.Name, strings.Join(p.Options, ", "))
		}
		return v, true, nil
	case "text":
		if strings.HasPrefix(v, "-") {
			return "", false, fmt.Errorf("%s has an invalid value", p.Name)
		}
		if p.Pattern != "" && !regexp.MustCompile(p.Pattern).MatchString(v) {
			return "", false, fmt.Errorf("%s has an invalid format", p.Name)
		}
		return v, true, nil
	case "date":
		if err := checkDate(v); err != nil {
			return "", false, fmt.Errorf("%s: %w", p.Name, err)
		}
		return v, true, nil
	case "daterange":
		if err := checkDateRange(v); err != nil {
			return "", false, fmt.Errorf("%s: %w", p.Name, err)
		}
		return v, true, nil
	default:
		return "", false, fmt.Errorf("unsupported param type %q", p.Type)
	}
}

// checkDate parses v as a real calendar day. time.Parse is strict about
// component ranges, so this rejects 2026-02-31 and 2026-13-01, which a shape
// regex accepts and the CLI only discovers once the task is already running.
func checkDate(v string) error {
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return fmt.Errorf("%q is not a valid date (expected YYYY-MM-DD)", v)
	}
	return nil
}

// checkDateRange accepts a single day or "start:end". An end before its start
// is rejected here rather than left to produce an empty result the caller would
// read as "nothing happened that week".
func checkDateRange(v string) error {
	start, end, isRange := strings.Cut(v, ":")
	if err := checkDate(start); err != nil {
		return err
	}
	if !isRange {
		return nil
	}
	if err := checkDate(end); err != nil {
		return err
	}
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	if e.Before(s) {
		return fmt.Errorf("range ends (%s) before it starts (%s)", end, start)
	}
	return nil
}

func isTrue(v string) bool { return v == "true" || v == "1" }
func contains(xs []string, x string) bool {
	for _, e := range xs {
		if e == x {
			return true
		}
	}
	return false
}

// BuildJobArgs validates params against the named job's schema and returns the
// argv to run. ok=false means the job name is unknown (404/400); a non-nil
// error means validation failed (400).
func BuildJobArgs(name string, params map[string]string) (args []string, ok bool, err error) {
	spec, found := jobSpecs[name]
	if !found {
		return nil, false, nil
	}
	b := spec.build
	if b == nil {
		b = genericBuild
	}
	a, err := b(spec, params)
	return a, true, err
}

// JobSpecList returns the job schemas, sorted, for GET /v1/jobs. Params is
// normalized to a non-nil slice so every job marshals "params" as [] (never
// null), giving the client one consistent shape regardless of whether a job
// declares any params.
func JobSpecList() []JobSpec {
	out := make([]JobSpec, 0, len(jobSpecs))
	for _, s := range jobSpecs {
		if s.Params == nil {
			s.Params = []Param{}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// JobNames returns the sorted allowlist (for error messages).
func JobNames() []string {
	names := make([]string, 0, len(jobSpecs))
	for n := range jobSpecs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RunStore is the read side of the run ledger (GET /v1/runs, /v1/runs/{id}).
type RunStore interface {
	List(ctx context.Context, limit int) ([]Run, error)
	Get(ctx context.Context, id string) (*RunDetail, bool, error)
}

// JobRunner launches a job asynchronously (ECS RunTask) and returns the run id.
//
// caller IS A PARAMETER RATHER THAN AN ENVIRONMENT CONVENTION, and that is the
// whole fix for the worst defect this API has had. The scheduled dispatcher put
// the tenant into the task's environment; this launcher did not, and nothing
// could notice — ROSTERBOT_USER_ID lives on the SHARED task definition, so an
// omitted override does not fail or yield an empty prefix, it silently resolves
// to a real, working, PRIVILEGED tenant. A member launching `optimize` got a
// task that decrypted the OPERATOR's Fantrax credentials and applied a lineup
// to the OPERATOR's roster, with every layer reporting success.
//
// As an argument the compiler rejects a launcher that forgets it. Same move
// this repo already made twice: WeeklyPeriod/DailyPeriod as distinct types
// after two period-axis bugs, and Version on lineupapi.Identity rather than in
// the method signature.
//
// An EMPTY caller is the bearer-token path, which has no UserID by construction
// (break-glass admin that deliberately never touches the user store). An
// implementation resolves that to the deployment's default tenant; it is not an
// error and must not be turned into one.
type JobRunner interface {
	Run(ctx context.Context, caller UserID, command []string) (id string, err error)
}

// commandString renders args for display.
func commandString(args []string) string { return strings.Join(args, " ") }

// maxJobDuration bounds how long a RUNNING ledger entry is trusted as
// genuinely still in-flight. entrypoint.sh only flips a run's status to
// SUCCESS/FAILED after the bot command exits — a hard crash (OOM kill, task
// stopped) before that point leaves the entry RUNNING forever with nothing
// to reap it. Past this bound a RUNNING entry is treated as stale/abandoned
// rather than live, so a single crashed task can't permanently block manual
// triggers for that job name. Generously above every job's real runtime
// (single-digit minutes at most).
const maxJobDuration = 2 * time.Hour

// inFlightRun returns the most recent RUNNING run of the given job name
// still within maxJobDuration of now, or nil if none. Matches on the run
// Command's first token (the base job name, e.g. "claims"), ignoring flags —
// the race this guards against (e.g. two concurrent claims runs reading the
// same cursor before either writes) isn't specific to one param combination.
//
// Best-effort: a nil RunStore or a List error skips the check (fail-open)
// rather than blocking a legitimate manual trigger on a ledger read hiccup —
// this is a race-reduction guard, not a strict distributed lock.
func inFlightRun(ctx context.Context, runs RunStore, name string, now time.Time) *Run {
	if runs == nil {
		return nil
	}
	list, err := runs.List(ctx, defaultRunsLimit)
	if err != nil {
		return nil
	}
	for i := range list {
		r := &list[i]
		if r.Status != "RUNNING" {
			continue
		}
		fields := strings.Fields(r.Command)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		started, err := time.Parse(time.RFC3339, r.StartedAt)
		if err != nil || now.Sub(started) > maxJobDuration {
			continue
		}
		return r
	}
	return nil
}
