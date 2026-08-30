package fantrax

import (
	"bytes"
	"errors"
	"log"
	"os"
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

	if err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, reserve); err != nil {
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

	if err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil); err != nil {
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
	// A second, unlocked change keeps the retry payload non-empty, so the
	// persistent-lock path (the retry POST itself failing again) is reachable.
	roster := fakeRoster(
		struct{ id, name string }{"04pkr", "Nico Hoerner"},
		struct{ id, name string }{"other", "Other Hitter"},
	)
	active := []PlayerSlot{
		{PlayerID: "04pkr", PosID: "003"},
		{PlayerID: "other", PosID: "008"},
	}

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		// Both attempts return the locked-player error — the retry parser
		// might miss the name. Function must give up gracefully (logged
		// warning, no crash) rather than infinite-looping or returning err.
		return errResp(lockedErrSingle), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil)
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

	err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil)
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

	if err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil); err != nil {
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

	if err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil); err != nil {
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

	err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil)
	if err == nil || !errors.Is(err, want) {
		t.Errorf("expected errors.Is(err, want), got: %v", err)
	}
}

// lockedErrFor builds a locked-player rejection naming the given player so
// parseLockedPlayerNames + excludeByName resolve it against the test roster.
func lockedErrFor(name string) string {
	return `You cannot make those changes because the following players are already locked in this period:<br/><br/><span class="defaultLink"><a class="hand " onclick="x">` + name + `</a></span><br/>`
}

// rosterWith builds a roster snapshot where each player carries an explicit
// StatusID + PosID — used to model the *post-apply* state a verify re-fetch sees.
func rosterWith(players ...struct{ id, st, pos string }) *models.TeamRosterResponse {
	var rows []models.PlayerRow
	for _, p := range players {
		rows = append(rows, models.PlayerRow{
			Scorer:   models.Player{ScorerID: p.id, Name: p.id},
			StatusID: p.st,
			PosID:    p.pos,
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

// After a locked-player retry, a benign response must NOT be trusted blindly:
// re-fetch and confirm the non-excluded intended changes actually landed. Here
// the paired (non-locked) swap silently failed — verify must surface an error.
func TestApplyLineup_PostRetryVerify_SilentFailureSurfaced(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"lock", "Locked Guy"},
		struct{ id, name string }{"swap", "Swap Guy"},
	)
	active := []PlayerSlot{
		{PlayerID: "lock", PosID: "003"},
		{PlayerID: "swap", PosID: "008"},
	}

	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		if fm["lock"].StID == auth_client.StatusActive {
			return errResp(lockedErrFor("Locked Guy")), nil // first attempt: lock is locked
		}
		return successResp(), nil // retry: benign, but swap didn't really land
	}
	// Re-fetch shows swap STILL on the bench — the change never took effect.
	fetch := func() (*models.TeamRosterResponse, error) {
		return rosterWith(
			struct{ id, st, pos string }{"lock", auth_client.StatusReserve, ""},
			struct{ id, st, pos string }{"swap", auth_client.StatusReserve, ""},
		), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, nil)
	if err == nil {
		t.Fatal("expected verify error — the non-excluded swap silently failed to land")
	}
	if !strings.Contains(err.Error(), "swap") {
		t.Errorf("error should name the unmet player, got: %v", err)
	}
}

// When the re-fetch confirms the non-excluded changes landed, verify passes.
func TestApplyLineup_PostRetryVerify_ChangesLanded_Success(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"lock", "Locked Guy"},
		struct{ id, name string }{"swap", "Swap Guy"},
	)
	active := []PlayerSlot{
		{PlayerID: "lock", PosID: "003"},
		{PlayerID: "swap", PosID: "008"},
	}

	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		if fm["lock"].StID == auth_client.StatusActive {
			return errResp(lockedErrFor("Locked Guy")), nil
		}
		return successResp(), nil
	}
	// Re-fetch shows swap active in slot 008 — the intended change landed.
	fetch := func() (*models.TeamRosterResponse, error) {
		return rosterWith(
			struct{ id, st, pos string }{"lock", auth_client.StatusReserve, ""},
			struct{ id, st, pos string }{"swap", auth_client.StatusActive, "008"},
		), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, nil); err != nil {
		t.Fatalf("expected success when verify confirms changes landed, got: %v", err)
	}
}

// captureLog redirects the standard logger for the duration of fn and returns
// everything written to it. Flags are zeroed so assertions match on message
// text alone, not the timestamp prefix.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(origFlags)
	}()
	fn()
	return buf.String()
}

// A passing verify must say so. Returning nil silently makes "verify ran and
// confirmed N changes" indistinguishable from "verify never ran" in production
// logs — the same false-confidence shape rosterbot-48z was filed about.
func TestApplyLineup_PostRetryVerify_Success_LogsConfirmation(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"lock", "Locked Guy"},
		struct{ id, name string }{"swap", "Swap Guy"},
		struct{ id, name string }{"bench", "Bench Guy"},
	)
	active := []PlayerSlot{
		{PlayerID: "lock", PosID: "003"},
		{PlayerID: "swap", PosID: "008"},
	}
	reserve := []string{"bench"}

	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		if fm["lock"].StID == auth_client.StatusActive {
			return errResp(lockedErrFor("Locked Guy")), nil
		}
		return successResp(), nil
	}
	// Both non-excluded changes landed: swap active in 008, bench on reserve.
	fetch := func() (*models.TeamRosterResponse, error) {
		return rosterWith(
			struct{ id, st, pos string }{"lock", auth_client.StatusReserve, ""},
			struct{ id, st, pos string }{"swap", auth_client.StatusActive, "008"},
			struct{ id, st, pos string }{"bench", auth_client.StatusReserve, ""},
		), nil
	}

	var err error
	out := captureLog(t, func() {
		err = applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, reserve)
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !strings.Contains(out, "post-retry verify") {
		t.Errorf("a passing verify must log that it ran, got: %q", out)
	}
	// 2 non-excluded changes confirmed (swap→active, bench→reserve); "lock"
	// was excluded by the retry and is not counted.
	if !strings.Contains(out, "2 change(s) confirmed") {
		t.Errorf("log must name how many changes were confirmed, got: %q", out)
	}
}

// A caller that wires no fetcher silently disables the rosterbot-48z
// protection. That must not look identical to a clean run in production logs:
// once a retry has excluded players, a nil fetcher gets its own warning.
func TestApplyLineup_PostRetryVerify_NilFetch_WarnsProtectionDisabled(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"lock", "Locked Guy"},
		struct{ id, name string }{"swap", "Swap Guy"},
	)
	active := []PlayerSlot{
		{PlayerID: "lock", PosID: "003"},
		{PlayerID: "swap", PosID: "008"},
	}

	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		if fm["lock"].StID == auth_client.StatusActive {
			return errResp(lockedErrFor("Locked Guy")), nil
		}
		return successResp(), nil
	}

	var err error
	out := captureLog(t, func() {
		err = applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil)
	})
	if err != nil {
		t.Fatalf("nil fetch must degrade gracefully, not error: %v", err)
	}
	if !strings.Contains(out, "verify DISABLED") {
		t.Errorf("a retry with no fetcher must warn that verify was disabled, got: %q", out)
	}
	if strings.Contains(out, "confirmed landed") {
		t.Errorf("nothing was verified — must not claim confirmation, got: %q", out)
	}
}

// The clean happy path (no locked-player retry) has nothing to verify, so it
// must stay silent — this runs hourly and a per-run line would be pure noise.
func TestApplyLineup_NoRetry_VerifyLogsNothing(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return successResp(), nil
	}

	var err error
	out := captureLog(t, func() {
		err = applyLineupWithLockedPlayerRetry(executor, nil, roster, active, nil)
	})
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if out != "" {
		t.Errorf("clean apply with no retry must log nothing, got: %q", out)
	}
}

// A verify re-fetch that itself errors can't positively confirm the outcome:
// soft-fail (no hard error) rather than turning a reported apply into a failure.
func TestApplyLineup_PostRetryVerify_FetchError_SoftFails(t *testing.T) {
	roster := fakeRoster(
		struct{ id, name string }{"lock", "Locked Guy"},
		struct{ id, name string }{"swap", "Swap Guy"},
	)
	active := []PlayerSlot{
		{PlayerID: "lock", PosID: "003"},
		{PlayerID: "swap", PosID: "008"},
	}

	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		if fm["lock"].StID == auth_client.StatusActive {
			return errResp(lockedErrFor("Locked Guy")), nil
		}
		return successResp(), nil
	}
	fetch := func() (*models.TeamRosterResponse, error) {
		return nil, errors.New("roster refetch broken")
	}

	if err := applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, nil); err != nil {
		t.Fatalf("verify fetch error should soft-fail, got: %v", err)
	}
}

// No locked-player retry (no exclusions) → verify is skipped entirely; the
// fetcher must never be called on the clean happy path (no extra API call).
func TestApplyLineup_NoRetry_VerifySkipped(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"p1", "Alpha"})
	active := []PlayerSlot{{PlayerID: "p1", PosID: "014"}}

	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return successResp(), nil
	}
	fetchCalled := false
	fetch := func() (*models.TeamRosterResponse, error) {
		fetchCalled = true
		return rosterWith(), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, nil); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if fetchCalled {
		t.Error("verify must not re-fetch when no locked-player retry occurred")
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

// When every intended change involves a locked player, there is nothing left
// to retry: a no-op retry POST can only come back benign, which would report
// success for changes that never landed (the rosterbot-48z masking shape).
// The function must skip the POST and return an honest error instead.
func TestApplyLineupAttempt_AllChangesLocked_NoRetryPost(t *testing.T) {
	roster := fakeRoster(struct{ id, name string }{"04pkr", "Nico Hoerner"})

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		return errResp(lockedErrSingle), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, nil, roster, nil, []string{"04pkr"})
	if err == nil {
		t.Fatal("expected an error — the only intended change involves a locked player")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should say the changes depend on locked players, got: %v", err)
	}
	if attempts != 1 {
		t.Errorf("a retry with no effective changes must not be posted; got %d attempts", attempts)
	}
}

// lockedErrCavalli is the 2026-08-29 rejection shape, naming Cade Cavalli.
const lockedErrCavalli = `roster change rejected: You cannot make those changes because the following players are already locked in this period:<br/><br/><span class="defaultLink"><a class="hand " onclick="showPlayerProfileNew('cav', '***'); stopBubble(event)">Cade Cavalli</a> <span title='Starting Pitcher'>SP</span> - <span title='Washington Nationals'>WSH</span></span><br/>`

// fakeRosterRows builds a TeamRosterResponse from explicit rows, so tests can
// place players in active slots (fakeRoster seeds everyone as Reserve).
func fakeRosterRows(rows ...models.PlayerRow) *models.TeamRosterResponse {
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

// The 2026-08-29 incident, second act: the optimizer benched Cade Cavalli
// (locked) to free the P slot for Gage Jump. Excluding Cavalli from the retry
// leaves Jump's promotion pointing at a slot that is still occupied — a
// payload Fantrax can never accept (it answered with an unresolvable confirm
// prompt, and the hourly run repeated the rejection all evening). A promotion
// whose slot was to be freed by an excluded bench move must be dropped with
// it; with nothing effective left, the retry must not be posted at all.
func TestApplyLineupAttempt_RetryDropsActivationStrandedByLockedBench(t *testing.T) {
	roster := fakeRosterRows(
		models.PlayerRow{Scorer: models.Player{ScorerID: "cav", Name: "Cade Cavalli"}, StatusID: auth_client.StatusActive, PosID: "017"},
		models.PlayerRow{Scorer: models.Player{ScorerID: "jump", Name: "Gage Jump"}, StatusID: auth_client.StatusReserve},
	)
	active := []PlayerSlot{{PlayerID: "jump", PosID: "017"}}
	reserve := []string{"cav"}

	var attempts int
	executor := func(_ map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		attempts++
		return errResp(lockedErrCavalli), nil
	}

	err := applyLineupWithLockedPlayerRetry(executor, nil, roster, active, reserve)
	if err == nil {
		t.Fatal("expected an error — every intended change depends on the locked player's slot")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error should say the changes depend on locked players, got: %v", err)
	}
	if attempts != 1 {
		t.Errorf("the stranded promotion must be dropped and the empty retry not posted; got %d attempts", attempts)
	}
}

// A change that does not depend on the locked player's slot still lands on the
// retry: only the stranded promotion is dropped alongside the exclusion.
func TestApplyLineupAttempt_RetryKeepsChangesIndependentOfTheLockedSlot(t *testing.T) {
	roster := fakeRosterRows(
		models.PlayerRow{Scorer: models.Player{ScorerID: "cav", Name: "Cade Cavalli"}, StatusID: auth_client.StatusActive, PosID: "017"},
		models.PlayerRow{Scorer: models.Player{ScorerID: "jump", Name: "Gage Jump"}, StatusID: auth_client.StatusReserve},
		models.PlayerRow{Scorer: models.Player{ScorerID: "oth", Name: "Slumping Bat"}, StatusID: auth_client.StatusActive, PosID: "012"},
	)
	active := []PlayerSlot{{PlayerID: "jump", PosID: "017"}}
	reserve := []string{"cav", "oth"}

	var attempts []map[string]auth_client.RosterPosition
	executor := func(fm map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		snap := make(map[string]auth_client.RosterPosition, len(fm))
		for k, v := range fm {
			snap[k] = v
		}
		attempts = append(attempts, snap)
		if len(attempts) == 1 {
			return errResp(lockedErrCavalli), nil
		}
		return successResp(), nil
	}
	fetch := func() (*models.TeamRosterResponse, error) {
		return fakeRosterRows(
			models.PlayerRow{Scorer: models.Player{ScorerID: "cav", Name: "Cade Cavalli"}, StatusID: auth_client.StatusActive, PosID: "017"},
			models.PlayerRow{Scorer: models.Player{ScorerID: "jump", Name: "Gage Jump"}, StatusID: auth_client.StatusReserve},
			models.PlayerRow{Scorer: models.Player{ScorerID: "oth", Name: "Slumping Bat"}, StatusID: auth_client.StatusReserve},
		), nil
	}

	if err := applyLineupWithLockedPlayerRetry(executor, fetch, roster, active, reserve); err != nil {
		t.Fatalf("the bench of an unrelated player should still land, got: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if got := attempts[1]["cav"].StID; got != auth_client.StatusActive {
		t.Errorf("retry must leave the locked player untouched, cav StID=%q", got)
	}
	if got := attempts[1]["jump"].StID; got != auth_client.StatusReserve {
		t.Errorf("retry must drop the stranded promotion, jump StID=%q", got)
	}
	if got := attempts[1]["oth"].StID; got != auth_client.StatusReserve {
		t.Errorf("retry must keep the independent bench move, oth StID=%q", got)
	}
}
