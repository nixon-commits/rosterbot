package jobwire

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
	Gate     *BacktestGateOut  `json:"gs_gate,omitempty"`
	// Shape is the roster-shape companion to Gate. It reached backtest
	// --json from the day it was written but not this wire type, so the
	// dashboard rendered a backtest run with no roster-shape section at all
	// — the same gap rosterbot-5cx closed for the gate, one section over
	// (rosterbot-c21e). nil under the same --skip-projections condition,
	// since it reads the same projection snapshots the gate does.
	Shape *BacktestShapeOut `json:"roster_shape,omitempty"`
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

// BacktestGateOut is the weekly game-start gate summary on the wire.
//
// The dashboard's run viewer is a generic JSON-to-DOM renderer that prints
// object keys verbatim as table headers, so these json names ARE the labels a
// reader sees. Two of them are load-bearing for that reason:
//
//   - suppressed_pts_gross / protected_pts_gross say GROSS in the key itself.
//     Neither is a net weekly change: a suppressed start's budget was spent on
//     a higher-ranked one, and a protected start is value that stayed deployed.
//     A key named "pts_lost" would be read as a subtraction from the week's
//     score, which it is not.
//   - days_with_snapshot and days_stale stay separate. A stale --matchup
//     pre-write always carries no suppressions, so folding it into the measured
//     count would report a gap in the run history as a quiet, well-behaved week.
type BacktestGateOut struct {
	Days               int                  `json:"days"`
	DaysWithSnapshot   int                  `json:"days_with_snapshot"`
	DaysStale          int                  `json:"days_stale,omitempty"`
	SuppressedStarts   int                  `json:"suppressed_starts"`
	SuppressedPtsGross float64              `json:"suppressed_pts_gross"`
	ProtectedStarts    int                  `json:"protected_starts,omitempty"`
	ProtectedPtsGross  float64              `json:"protected_pts_gross,omitempty"`
	FloorMin           int                  `json:"floor_min,omitempty"`
	FloorMax           int                  `json:"floor_max,omitempty"`
	ByDate             []BacktestGateDayOut `json:"by_date,omitempty"`
}

type BacktestGateDayOut struct {
	Date              string  `json:"date"`
	SuppressedStarts  int     `json:"suppressed_starts"`
	PtsGross          float64 `json:"pts_gross"`
	ProtectedStarts   int     `json:"protected_starts,omitempty"`
	ProtectedPtsGross float64 `json:"protected_pts_gross,omitempty"`
}

// BacktestShapeOut is the roster-shape summary on the wire — the analytical
// companion to BacktestGateOut, and subject to the same rule: the dashboard's
// run viewer prints these json keys verbatim as labels, so the key names carry
// the interpretation or nothing does. Three of them are load-bearing.
//
//   - The two sides are DIFFERENT TYPES naming their rate differently, and
//     that asymmetry is deliberate. A hitter appears in essentially every game
//     his club plays, so his side's rate is a real "how much of what you owned
//     did you field". A starter counts as deployable only on days he was a
//     probable start, so the pitcher denominator is that day's probables —
//     which MLB rotation cadence holds under the six P slots almost always,
//     making the rate read ~100% whether the staff is seven deep or thirteen
//     (measured at exactly 100.0% against a thirteen-man staff, rosterbot-8dl).
//     One shared `fielded_pct` key on both sides would set those two beside
//     each other as if they were the same measurement, which is precisely the
//     misreading 8dl exists to close. Do not "simplify" them back together.
//   - Rotation rides along, because weekly capacity is the frame in which the
//     surplus question the pitcher rate structurally cannot answer becomes
//     answerable.
//   - The coverage counts ride on BOTH sides. The pitcher denominator has been
//     measured as low as 2 of 13 rostered player-days, and a rate on that base
//     is a near-single-player fact wearing a percentage; without the counts a
//     reader cannot see that. They are named _player_days rather than _count
//     because that is what they sum — a player counts once per day he appears,
//     not once for the window.
type BacktestShapeOut struct {
	Days             int `json:"days"`
	DaysWithSnapshot int `json:"days_with_snapshot"`
	DaysStale        int `json:"days_stale,omitempty"`
	DaysPreSchema    int `json:"days_pre_schema,omitempty"`

	HitterSlots  int `json:"hitter_slots"`
	PitcherSlots int `json:"pitcher_slots"`

	// Zero means no counted day recorded a cap at all, which is why both are
	// omitempty: a rendered "gs_cap_max_per_week: 0" reads as a cap of none
	// rather than an absent one — the distinction FormatGateSummary draws
	// between "no GS minimum configured" and "floor 0".
	GSCapMinPerWeek int `json:"gs_cap_min_per_week,omitempty"`
	GSCapMaxPerWeek int `json:"gs_cap_max_per_week,omitempty"`

	Hitters  BacktestHitterShapeOut  `json:"hitters"`
	Pitchers BacktestPitcherShapeOut `json:"pitchers"`

	Rotation *BacktestRotationOut `json:"rotation_supply,omitempty"`
}

// BacktestSideShapeOut is the part of one side's shape that means the same
// thing for both roles. It is embedded by the two side types rather than used
// as a field, so each side names its own rate; see BacktestShapeOut.
type BacktestSideShapeOut struct {
	OwnedPts   float64 `json:"owned_pts"`
	FieldedPts float64 `json:"fielded_pts"`
	// StrandedPts is owned minus fielded. It shares a unit and a window with
	// the other side's and must never be summed with it: a benched hitter
	// starts tomorrow, while a start declined above the weekly cap is dead,
	// because game-start budget is use-it-or-lose-it. Nesting the two sides in
	// separate objects rather than flattening them into hitter_/pitcher_
	// prefixed keys is part of what keeps them from reading as addends.
	StrandedPts float64 `json:"stranded_pts"`

	DeployablePlayerDays int `json:"deployable_player_days"`
	RosteredPlayerDays   int `json:"rostered_player_days"`
}

type BacktestHitterShapeOut struct {
	BacktestSideShapeOut
	// FieldedPctOfOwned names its own denominator because each side is
	// normalized against its OWN owned value and never against the other — so
	// the gap between the two sides is not the league's slot ratio. nil when
	// the side had no deployable value at all, which must render as absent and
	// never as 0%: nothing to field is the opposite reading from everything
	// stranded.
	FieldedPctOfOwned *float64 `json:"fielded_pct_of_owned,omitempty"`
}

type BacktestPitcherShapeOut struct {
	BacktestSideShapeOut
	// ProbableStartCompliancePct is this side's fielded rate, named for what it
	// measures rather than for how it is computed — see BacktestShapeOut. nil
	// follows the same absent-not-zero rule as the hitter side's.
	ProbableStartCompliancePct *float64 `json:"probable_start_compliance_pct,omitempty"`
}

// BacktestRotationOut is weekly rotation capacity: how many starts the staff
// supplies against how many the league permits. Above 100% is rotation the
// roster owns and cannot deploy.
//
// The whole object is omitted rather than sent with a missing ratio when no GS
// cap was recorded, matching backtest.RotationSupply.SupplyRate and the stdout
// renderer, which prints nothing in that case: a bare "supplies 13.2
// starts/wk" invites the reader to supply their own denominator for the one
// quantity that actually moves.
type BacktestRotationOut struct {
	MeanSPCount         float64 `json:"mean_sp_count"`
	SupplyStartsPerWeek float64 `json:"supply_starts_per_week"`
	GSCapPerWeek        int     `json:"gs_cap_per_week"`
	SupplyPctOfCap      float64 `json:"supply_pct_of_cap"`
}

// GradeResult is the grade job output (what was written to the Analysis Store).
type GradeResult struct {
	Dates       []string `json:"dates"`
	RowsWritten int      `json:"rows_written"`

	// WindowNotes is the run's own account of the range it chose and, when the
	// --max-window cap bound, what it declined to re-grade.
	//
	// It rides the structured result rather than stdout because stdout does not
	// reach anyone on the run that matters. cmd/ledger.go captures log_tail
	// ONLY when a run FAILED, and a capped grade run SUCCEEDS — so printing the
	// cap was "named rather than silently dropped" into a channel with no
	// reader, which is this repo's no-silent-caps rule satisfied in letter and
	// broken in substance (rosterbot-n30). The dashboard's run viewer renders
	// object keys verbatim, so the json name is the label an operator reads.
	WindowNotes []string `json:"window_notes,omitempty"`
}
