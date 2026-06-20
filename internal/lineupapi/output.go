package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RunOutput is the GET /v1/runs/{id}/output body: a job-type discriminator plus
// the job-specific result object. Stored verbatim at runs/<id>/output.json.
type RunOutput struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// MarshalOutput serializes a job result into the {type, data} envelope. Indented
// for curl-ability; the iOS decoder is whitespace-agnostic.
func MarshalOutput(jobType string, data any) ([]byte, error) {
	return json.MarshalIndent(RunOutput{Type: jobType, Data: data}, "", "  ")
}

// --- Per-job wire results (snake_case; decoded by the iOS client) ---

// ProspectsResult is the prospects job output. Alerts carry a `kind` the client
// partitions into call-up vs breakout views; upgrades are drop→add suggestions.
type ProspectsResult struct {
	Alerts   []ProspectAlertOut   `json:"alerts"`
	Upgrades []ProspectUpgradeOut `json:"upgrades"`
}

type ProspectAlertOut struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	Pos      string `json:"pos,omitempty"`
	Kind     string `json:"kind"`
	Priority string `json:"priority"`
	Detail   string `json:"detail"`
	Rank     int    `json:"rank,omitempty"`
}

type ProspectUpgradeOut struct {
	Source   string `json:"source"`
	Drop     string `json:"drop"`
	DropRank int    `json:"drop_rank"`
	Add      string `json:"add"`
	AddRank  int    `json:"add_rank"`
	RankGap  int    `json:"rank_gap"`
	NearTerm bool   `json:"near_term"`
}

// WaiversResult is the waivers job output.
type WaiversResult struct {
	Picks []WaiverPickOut `json:"picks"`
	Total int             `json:"total"`
}

type WaiverPickOut struct {
	Name         string  `json:"name"`
	Team         string  `json:"team"`
	Pos          string  `json:"pos"`
	IsPitcher    bool    `json:"is_pitcher"`
	Signal       string  `json:"signal,omitempty"`
	ProjectedFPG float64 `json:"projected_pts_per_game"`
	DropName     string  `json:"drop_name,omitempty"`
	Gap          float64 `json:"gap,omitempty"`
	Xwoba        float64 `json:"xwoba,omitempty"`
	Woba         float64 `json:"woba,omitempty"`
	BarrelPct    float64 `json:"barrel_pct,omitempty"`
	HardHitPct   float64 `json:"hard_hit_pct,omitempty"`
	Era          float64 `json:"era,omitempty"`
	Xera         float64 `json:"xera,omitempty"`
	Rank         int     `json:"rank"`
}

// ClaimsResult is the claims job output.
type ClaimsResult struct {
	Claims []ClaimOut `json:"claims"`
}

type ClaimOut struct {
	Team      string `json:"team"`
	ClaimType string `json:"claim_type"`
	Added     string `json:"added"`
	AddedPos  string `json:"added_pos,omitempty"`
	Dropped   string `json:"dropped,omitempty"`
	NetValue  int    `json:"net_value"`
	Signal    string `json:"signal,omitempty"`
}

// TransactionsResult is the transactions (trade monitor) job output.
type TransactionsResult struct {
	Trades []TradeOut `json:"trades"`
}

type TradeOut struct {
	Teams       []string         `json:"teams"`
	Players     []TradePlayerOut `json:"players"`
	ProcessedAt string           `json:"processed_at"`
}

type TradePlayerOut struct {
	Name      string `json:"name"`
	FromTeam  string `json:"from_team"`
	Pos       string `json:"pos,omitempty"`
	Valuation int    `json:"valuation"`
}

// GSCheckResult is the gs-check job output.
type GSCheckResult struct {
	Period     string           `json:"period,omitempty"`
	Violations []GSViolationOut `json:"violations"`
}

type GSViolationOut struct {
	Team   string `json:"team"`
	Kind   string `json:"kind"`
	Used   int    `json:"used"`
	Limit  int    `json:"limit"`
	OverBy int    `json:"over_by,omitempty"`
}

// BacktestResult is the backtest job output.
type BacktestResult struct {
	Start    string            `json:"start"`
	End      string            `json:"end"`
	Days     []BacktestDayOut  `json:"days"`
	Accuracy *BacktestAccuracy `json:"accuracy,omitempty"`
}

type BacktestDayOut struct {
	Date    string  `json:"date"`
	Actual  float64 `json:"actual"`
	Optimal float64 `json:"optimal"`
	Gap     float64 `json:"gap"`
}

type BacktestAccuracy struct {
	MAE        float64               `json:"mae"`
	Bias       float64               `json:"bias"`
	RMSE       float64               `json:"rmse"`
	N          int                   `json:"n"`
	ByPosition []BacktestPositionOut `json:"by_position,omitempty"`
}

type BacktestPositionOut struct {
	Bucket string  `json:"bucket"`
	N      int     `json:"n"`
	MAE    float64 `json:"mae"`
	Bias   float64 `json:"bias"`
}

// GradeResult is the grade job output (what was written to the Analysis Store).
type GradeResult struct {
	Dates       []string `json:"dates"`
	RowsWritten int      `json:"rows_written"`
}

// --- Store interfaces + local file adapter + global hook ---

// OutputStore is the read side for captured job output: fetch the stored bytes
// for a run id. ok=false means 404; err means a backend failure (502).
type OutputStore interface {
	GetOutput(ctx context.Context, runID string) ([]byte, bool, error)
}

// OutputWriter is the write side: persist the marshaled envelope for a run id.
type OutputWriter interface {
	PutOutput(ctx context.Context, runID string, data []byte) error
}

// FileOutputStore is a local-filesystem OutputStore+OutputWriter, one file per
// run at <dir>/<runID>.json. Used by `serve` and local job runs.
type FileOutputStore struct {
	dir string
}

func NewFileOutputStore(dir string) *FileOutputStore { return &FileOutputStore{dir: dir} }

func (s *FileOutputStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

func (s *FileOutputStore) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *FileOutputStore) PutOutput(_ context.Context, runID string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

// OutputRecorder is a nil-safe global hook (mirrors notify.Recorder). cmd sets
// it to a closure that marshals {type,data} and writes it under the RUN_ID env
// var. Jobs call RecordOutput; when the hook is unset (tests, local runs without
// a run id) the call is a no-op, so nothing else has to change.
var OutputRecorder func(jobType string, data any)

// RecordOutput hands a job's typed result to the installed recorder. Safe to
// call unconditionally; no-op when no recorder is installed.
func RecordOutput(jobType string, data any) {
	if OutputRecorder != nil {
		OutputRecorder(jobType, data)
	}
}

var (
	_ OutputStore  = (*FileOutputStore)(nil)
	_ OutputWriter = (*FileOutputStore)(nil)
)
