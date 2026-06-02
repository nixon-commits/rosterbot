package fantrax

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// May 1 failure (Nico Hoerner — single locked player).
const lockedErrSingle = `roster change rejected: You cannot make those changes because the following players are already locked in this period:<br/><br/><span class="defaultLink"><a class="hand " onclick="showPlayerProfileNew('04pkr', '***'); stopBubble(event)">Nico Hoerner</a> <span title='2nd Base,Infield,Utility (Any Hitter)'>2B,INF</span> - <span title='Chicago Cubs'>CHC</span></span><br/>`

// Apr 26 failure (Colt Emerson + Kristian Campbell — two locked players).
const lockedErrMulti = `roster change rejected: You cannot make those changes because the following players are already locked in this period:<br/><br/><span class="defaultLink"><a class="hand " onclick="showPlayerProfileNew('0***0ub', '***'); stopBubble(event)">Colt Emerson</a> <span title='Shortstop,Infield,Utility (Any Hitter)'>SS,INF</span> - <span title='Seattle Mariners'>SEA</span></span><br/><span class="defaultLink"><a class="hand " onclick="showPlayerProfileNew('0***f0q', '***'); stopBubble(event)">Kristian Campbell</a> <span title='2nd Base,Infield,Utility (Any Hitter)'>2B,INF</span> - <span title='Boston Red Sox'>BOS</span></span><br/>`

func TestParseLockedPlayerNames(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []string
	}{
		{
			name: "single player (May 1 Hoerner)",
			msg:  lockedErrSingle,
			want: []string{"Nico Hoerner"},
		},
		{
			name: "two players (Apr 26 Emerson+Campbell)",
			msg:  lockedErrMulti,
			want: []string{"Colt Emerson", "Kristian Campbell"},
		},
		{
			name: "unrelated error returns nil",
			msg:  "roster change rejected: max actions exceeded",
			want: nil,
		},
		{
			name: "empty string returns nil",
			msg:  "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLockedPlayerNames(tt.msg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseLockedPlayerNames(%q)\n  got:  %#v\n  want: %#v", tt.msg, got, tt.want)
			}
		})
	}
}

// fakeRoster builds a minimal TeamRosterResponse with the given (id, name)
// players in a single table. Each row starts as Reserve so any move shows up
// as a change in the fieldMap.
func fakeRoster(players ...struct{ id, name string }) *models.TeamRosterResponse {
	var rows []models.PlayerRow
	for _, p := range players {
		rows = append(rows, models.PlayerRow{
			Scorer:   models.Player{ScorerID: p.id, Name: p.name},
			StatusID: auth_client.StatusReserve,
		})
	}
	r := &models.TeamRosterResponse{}
	r.Responses = append(r.Responses, struct {
		Data models.TeamRosterResponseData `json:"data"`
	}{
		Data: models.TeamRosterResponseData{
			Tables: []models.RosterTable{{Rows: rows}},
		},
	})
	return r
}

// successResp returns a response with no MainMsg (treated as success).
func successResp() *models.RosterChangeResponse {
	r := &models.RosterChangeResponse{}
	r.Responses = append(r.Responses, struct {
		Data struct {
			FantasyResponse struct {
				MainMsg                  string            `json:"mainMsg,omitempty"`
				MsgType                  string            `json:"msgType"`
				LineupChanges            []interface{}     `json:"lineupChanges"`
				ShowConfirmWindow        bool              `json:"showConfirmWindow"`
				NavItems                 []interface{}     `json:"navItems,omitempty"`
				ShowApplyToFuturePeriods bool              `json:"showApplyToFuturePeriods"`
				RemoveSubmitButton       bool              `json:"removeSubmitButton"`
				ApplyToFuturePeriods     bool              `json:"applyToFuturePeriods"`
				ResourceMap              map[string]string `json:"resourceMap"`
			} `json:"fantasyResponse"`
			TextArray struct {
				Data  []interface{} `json:"data"`
				Model struct {
					RosterLimitPeriodDisplay        string                      `json:"rosterLimitPeriodDisplay"`
					RosterAdjustmentInfo            models.RosterAdjustmentInfo `json:"rosterAdjustmentInfo"`
					FirstIllegalRosterPeriodDisplay string                      `json:"firstIllegalRosterPeriodDisplay"`
					FirstIllegalRosterPeriod        int                         `json:"firstIllegalRosterPeriod"`
					NumIllegalRosterMsgs            int                         `json:"numIllegalRosterMsgs"`
					PlayerPickDeadlinePassed        bool                        `json:"playerPickDeadlinePassed"`
					IllegalRosterMsgs               []string                    `json:"illegalRosterMsgs"`
					IllegalBefore                   bool                        `json:"illegalBefore"`
					ChangeAllowed                   bool                        `json:"changeAllowed"`
				} `json:"model"`
			} `json:"textArray"`
			Commissioner bool `json:"commissioner,omitempty"`
		} `json:"data"`
	}{})
	return r
}

// errResp returns a response whose MainMsg matches the locked-player rejection.
func errResp(mainMsg string) *models.RosterChangeResponse {
	r := successResp()
	r.Responses[0].Data.FantasyResponse.MainMsg = mainMsg
	return r
}

func TestApplyLineupAttempt_LockedPlayerRetry_May1Hoerner(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"04pkr", "Nico Hoerner"},
		struct{ id, name string }{"abcde", "Other Hitter"},
	)
	active := []PlayerSlot{
		{PlayerID: "04pkr", PosID: "003"},
		{PlayerID: "abcde", PosID: "008"},
	}
	var reserve []string

	var attempts []map[string]auth_client.RosterPosition
	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		// Snapshot the fieldMap as-passed so we can inspect ordering.
		snap := make(map[string]auth_client.RosterPosition, len(fm))
		for k, v := range fm {
			snap[k] = v
		}
		attempts = append(attempts, snap)
		if len(attempts) == 1 {
			return errResp(lockedErrSingle), nil
		}
		return successResp(), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, roster, active, reserve); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	if len(attempts) != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", len(attempts))
	}

	// First attempt: Hoerner activated to 2B (003).
	if attempts[0]["04pkr"].StID != auth_client.StatusActive {
		t.Errorf("attempt[0] Hoerner StID=%q, want Active", attempts[0]["04pkr"].StID)
	}
	// Second attempt: Hoerner reverted to original Reserve status (no longer activated).
	if attempts[1]["04pkr"].StID != auth_client.StatusReserve {
		t.Errorf("attempt[1] Hoerner StID=%q, want Reserve (excluded from retry)", attempts[1]["04pkr"].StID)
	}
	// Other hitter still gets activated on the retry.
	if attempts[1]["abcde"].StID != auth_client.StatusActive {
		t.Errorf("attempt[1] Other StID=%q, want Active (still in retry)", attempts[1]["abcde"].StID)
	}
}

func TestApplyLineupAttempt_LockedPlayerRetry_Apr26EmersonCampbell(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"emer", "Colt Emerson"},
		struct{ id, name string }{"camp", "Kristian Campbell"},
		struct{ id, name string }{"keep", "Mike Trout"},
	)
	active := []PlayerSlot{
		{PlayerID: "emer", PosID: "014"},
		{PlayerID: "camp", PosID: "014"},
		{PlayerID: "keep", PosID: "012"},
	}

	var attempts []map[string]auth_client.RosterPosition
	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		snap := make(map[string]auth_client.RosterPosition, len(fm))
		for k, v := range fm {
			snap[k] = v
		}
		attempts = append(attempts, snap)
		if len(attempts) == 1 {
			return errResp(lockedErrMulti), nil
		}
		return successResp(), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	if len(attempts) != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", len(attempts))
	}

	for _, locked := range []string{"emer", "camp"} {
		if attempts[1][locked].StID != auth_client.StatusReserve {
			t.Errorf("attempt[1] %q StID=%q, want Reserve (excluded)", locked, attempts[1][locked].StID)
		}
	}
	if attempts[1]["keep"].StID != auth_client.StatusActive {
		t.Errorf("attempt[1] keep StID=%q, want Active (still in retry)", attempts[1]["keep"].StID)
	}
}

func TestApplyLineupAttempt_LockedPersistsAfterRetry_DoesNotCrash(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"04pkr", "Nico Hoerner"})
	active := []PlayerSlot{{PlayerID: "04pkr", PosID: "003"}}

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		// Both attempts return the locked-player error — the retry parser
		// might miss the name. Function must give up gracefully (logged
		// warning, no crash) rather than infinite-looping or returning err.
		return errResp(lockedErrSingle), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil)
	if err == nil {
		t.Fatal("expected error after retry still fails")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should mention locked players, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("retry must be capped at 1 (2 total attempts), got %d", attempts)
	}
}

func TestApplyLineupAttempt_NonLockedRejection_NoRetry(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha Player"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		return errResp("max actions exceeded for this period"), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil)
	if err == nil {
		t.Fatal("expected error from unrelated rejection")
	}
	if attempts != 1 {
		t.Errorf("non-locked rejection must not retry, got %d attempts", attempts)
	}
}

func TestApplyLineupAttempt_HappyPath(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		return successResp(), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if attempts != 1 {
		t.Errorf("happy path must use exactly 1 attempt, got %d", attempts)
	}
}

func TestApplyLineupAttempt_NoChangesDetected_TreatedAsSuccess(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return errResp("no changes detected"), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil); err != nil {
		t.Errorf("no-change response should be benign success, got: %v", err)
	}
}

func TestApplyLineupAttempt_ExecutorError_Propagates(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	want := errors.New("network broken")
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return nil, want
	}

	err := applyLineupWithLockedPlayerRetry(executor, roster, active, nil)
	if err == nil || !errors.Is(err, want) {
		t.Errorf("expected errors.Is(err, want), got: %v", err)
	}
}

// Ordering helper: parse twice then verify deterministic output. (Smoke test
// for accidental map iteration in the parser.)
func TestParseLockedPlayerNames_Deterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		got := parseLockedPlayerNames(lockedErrMulti)
		want := []string{"Colt Emerson", "Kristian Campbell"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: got %v, want %v", i, got, want)
		}
	}
}

// Bench-list moves are also excluded on retry.
func TestApplyLineupAttempt_LockedReservedPlayer_DroppedFromRetry(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"04pkr", "Nico Hoerner"})

	var attempts []map[string]auth_client.RosterPosition
	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		snap := make(map[string]auth_client.RosterPosition, len(fm))
		for k, v := range fm {
			snap[k] = v
		}
		attempts = append(attempts, snap)
		if len(attempts) == 1 {
			return errResp(lockedErrSingle), nil
		}
		return successResp(), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, roster, nil, []string{"04pkr"}); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	// First attempt staged a Reserve move (unchanged StID since seed roster
	// is already Reserve — but PosID would clear). Just confirm the second
	// attempt left Hoerner in original Reserve state with no PosID change.
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if attempts[1]["04pkr"].StID != auth_client.StatusReserve {
		t.Errorf("attempt[1] Hoerner StID=%q, want Reserve", attempts[1]["04pkr"].StID)
	}
}
