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
type Player struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Team   string   `json:"team"`
	Pos    []string `json:"pos"`
	Proj   float64  `json:"proj"`
	Status string   `json:"status"`
}

// Slot is one lineup slot. Player is nil for an empty/open slot (rendered as
// JSON null), e.g. an unfilled active slot or a vacant bench row.
type Slot struct {
	Slot   string  `json:"slot"`
	Player *Player `json:"player"`
}

// LineupResponse is the full GET /v1/lineup/today body.
type LineupResponse struct {
	Date            string   `json:"date"`
	LeagueID        string   `json:"league_id"`
	TeamID          string   `json:"team_id"`
	Slots           []Slot   `json:"slots"`
	ProjectedPoints float64  `json:"projected_points"`
	Warnings        []string `json:"warnings"`
}

// Marshal is the one place the response is serialized, so the producer (what we
// store) and any test (what we assert) agree byte-for-byte. Indented for human
// curl-ability; the iOS decoder is whitespace-agnostic.
func Marshal(r LineupResponse) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Run is one execution of a backend job (scheduled or manually triggered), as
// recorded in the run ledger and exposed by GET /v1/runs.
type Run struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
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

// ProgressSnapshot is the GET /v1/runs/{id}/progress body — live phase progress
// for a run. Mirrors internal/progress.Snapshot. Phase detail only; the run's
// authoritative status comes from the ledger (GET /v1/runs).
type ProgressSnapshot struct {
	Phase     string          `json:"phase"`
	Pct       int             `json:"pct"`
	Phases    []ProgressPhase `json:"phases"`
	Status    string          `json:"status"`
	UpdatedAt string          `json:"updated_at"`
}

type ProgressPhase struct {
	Name  string `json:"name"`
	State string `json:"state"`
}
