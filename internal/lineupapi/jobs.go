package lineupapi

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Param describes one tunable flag the app can present as a form field and the
// backend will validate before turning into argv. Type drives both rendering
// and validation: bool (switch), int (stepper, Min/Max), enum (picker,
// Options), text (validated against Pattern).
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

// dateOrRange matches a single date or a YYYY-MM-DD:YYYY-MM-DD range.
var dateOrRange = `^\d{4}-\d{2}-\d{2}(:\d{4}-\d{2}-\d{2})?$`

// csvCodes matches a comma-separated list of alphanumeric codes (e.g. OF,SP).
var csvCodes = `^[A-Za-z0-9]+(,[A-Za-z0-9]+)*$`

var projectionSystems = []string{"steamer", "depthcharts", "thebatx", "steamer-ros", "depthcharts-ros", "thebatx-ros"}

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
			{Name: "dates", Label: "Custom date / range", Type: "text", Pattern: dateOrRange,
				Help: "Used when Period = custom. e.g. 2026-04-01 or 2026-04-01:2026-04-07"},
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
			{Name: "dates", Label: "Window", Type: "text", Pattern: dateOrRange,
				Help: "Date range to grade, e.g. 2026-04-13:2026-04-19 (default: last completed week)"},
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
		if !regexp.MustCompile(dateOrRange).MatchString(d) {
			return nil, fmt.Errorf("custom period needs a valid date or range (got %q)", d)
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
	default:
		return "", false, fmt.Errorf("unsupported param type %q", p.Type)
	}
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

// JobSpecList returns the job schemas, sorted, for GET /v1/jobs.
func JobSpecList() []JobSpec {
	out := make([]JobSpec, 0, len(jobSpecs))
	for _, s := range jobSpecs {
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
type JobRunner interface {
	Run(ctx context.Context, command []string) (id string, err error)
}

// commandString renders args for display.
func commandString(args []string) string { return strings.Join(args, " ") }
