// Package lineupapi owns the wire contract for the read-only HTTP lineup API
// (GET /v1/lineup/today). It is the single source of truth for the JSON shape
// the iOS client decodes, kept in its own leaf package so neither the producer
// (cmd/optimize) nor the Lambda handler has to reach into cmd-internal types.
//
// The flow is precompute-then-serve: the hourly optimize run builds a
// LineupResponse from the lineup it already computed and publishes the JSON to
// object storage; the handler just authenticates and returns those bytes. No
// optimizer work happens on the request path.
package lineupapi

import "encoding/json"

// Player is one rostered player as the API exposes it.
//
// Field order here defines JSON key order (encoding/json preserves struct
// order), and the json tags are the snake_case contract the iOS client decodes
// with keyDecodingStrategy=.convertFromSnakeCase. Do not rename without bumping
// the API version.
// Age and HKBValue are the dynasty enrichment, joined from HKB by normalized
// name (see lineuprun.LoadHKBMeta). Both are omitempty and both are zero for
// the same reason — HKB has no row for this player — so a client must treat
// zero as "unknown", not as "newborn" or "worthless". That happens routinely: a
// fresh call-up can be missing from HKB's rankings for days, and the enrichment
// is skipped entirely when the scrape fails, which must not change how the rest
// of the lineup reads.
type Player struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Team     string   `json:"team"`
	Pos      []string `json:"pos"`
	Age      float64  `json:"age,omitempty"`
	HKBValue int      `json:"hkb_value,omitempty"`
	Proj     float64  `json:"proj"`
	Status   string   `json:"status"`
	// Role is "hitter" or "pitcher", and exists because Proj means a different
	// thing in each: measured on the 2026-08-15 lineup, hitters span 2.92-5.48
	// and pitchers 2.85-19.89, so any client comparing the two on one scale
	// puts every hitter in the bottom sixth of it.
	//
	// It is stated rather than left to be inferred from Pos. Build fills the
	// response from two separate optimizer lists and already knows which is
	// which; a two-way player appears in BOTH, producing two rows that carry
	// identical eligibility, so Pos cannot distinguish his hitter row from his
	// pitcher row and a client inferring the role would shade a ~4-point row on
	// a 20-point scale. Not omitempty: a client must be able to tell "this
	// lineup predates the field" (absent) from any value it might carry.
	Role string `json:"role"`
}

// Role values for Player.Role.
const (
	RoleHitter  = "hitter"
	RolePitcher = "pitcher"
)

// Slot is one lineup slot. Player is nil for an empty/open slot (rendered as
// JSON null), e.g. an unfilled active slot or a vacant bench row.
type Slot struct {
	Slot   string  `json:"slot"`
	Player *Player `json:"player"`
}

// LineupResponse is the full GET /v1/lineup/today (and /v1/lineup/preview) body.
type LineupResponse struct {
	Date            string   `json:"date"`
	LeagueID        string   `json:"league_id"`
	TeamID          string   `json:"team_id"`
	Slots           []Slot   `json:"slots"`
	ProjectedPoints float64  `json:"projected_points"`
	Warnings        []string `json:"warnings"`

	// GeneratedAt is when the optimizer computed this lineup (RFC3339 UTC).
	//
	// It exists so a client holding both the applied lineup and a preview can
	// tell which one is newer — the only way to decide whether a preview
	// supersedes what is on Fantrax or merely predates it. Without it the two
	// blobs are unorderable and the client would have to guess.
	//
	// omitempty, and empty on every blob published before this field: a client
	// must read absence as "older than anything timestamped", which is what it
	// is.
	GeneratedAt string `json:"generated_at,omitempty"`
}

// Marshal is the one place the response is serialized, so the producer (what we
// store) and any test (what we assert) agree byte-for-byte. Indented for human
// curl-ability; the iOS decoder is whitespace-agnostic.
func Marshal(r LineupResponse) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Run is one execution of a backend job (scheduled or manually triggered), as
// recorded in the run ledger and exposed by GET /v1/runs.
//
// UserID is whose run it was under per-tenant fan-out, where N tenants run the
// same command string into this one ledger. It is omitempty and empty on every
// record written before fan-out, which is exactly what lets internal/opsalert
// key its decisions on (command, user_id) without re-grading history: see
// opsalert.Record, whose duplication of these fields opsalert_contract_test.go
// guards.
type Run struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	UserID    string `json:"user_id,omitempty"`   // empty = pre-fan-out single tenant
	Status    string `json:"status"`              // RUNNING | SUCCESS | FAILED
	ExitCode  *int   `json:"exit_code,omitempty"` // nil while RUNNING
	StartedAt string `json:"started_at"`          // RFC3339 UTC
	EndedAt   string `json:"ended_at,omitempty"`  // empty while RUNNING
	Trigger   string `json:"trigger"`             // schedule | manual
}

// RunDetail is a Run plus its captured log tail (GET /v1/runs/{id}). The stored
// ledger object is a RunDetail; the list endpoint returns just the Run portion.
type RunDetail struct {
	Run
	LogTail string `json:"log_tail,omitempty"`
}

// RunsResponse is the GET /v1/runs body.
type RunsResponse struct {
	Runs []Run `json:"runs"`
}

// JobResponse is the POST /v1/jobs/{name} body (202 Accepted).
type JobResponse struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Status  string `json:"status"` // always RUNNING
}

// The GET /v1/runs/{id}/progress body is served as the raw stored bytes of an
// internal/progress.Snapshot (see handleRunProgress) — phase detail only; the
// run's authoritative status comes from the ledger (GET /v1/runs). There is no
// separate wire type here on purpose: a hand-mirrored struct nothing
// marshals/unmarshals would only drift from progress.Snapshot, the single
// source of truth for that shape.
