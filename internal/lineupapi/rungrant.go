package lineupapi

// Refusal reasons for AuthorizeRun. They are CLASSES, like the ConnErr ones:
// they reach an operator alert and a dashboard banner, and they must be stable
// enough to key on. Each names a different remedy, which is the whole point of
// keeping them apart:
//
//   - NoConnection / NotVerified — the user has not finished connecting.
//   - NeedsReconnect             — it worked and then broke; only a human fixes it.
//   - NoSession / NoCredentials / NoTeam — a verified record missing something
//     it should not be missing, i.e. our bug, not theirs.
//   - WrongTenant                — a record was loaded for the wrong user.
const (
	RunDeniedNoConnection   = "no_connection"
	RunDeniedNotVerified    = "not_verified"
	RunDeniedNeedsReconnect = "needs_reconnect"
	RunDeniedNoSession      = "no_session"
	RunDeniedNoCredentials  = "no_credentials"
	RunDeniedNoTeam         = "no_team"
	RunDeniedWrongTenant    = "wrong_tenant"
)

// RunGrant is permission for a job to act as one tenant, carrying everything
// that decision produced: which team, and the sealed material to reach it.
//
// The team comes from the RECORD, never from the environment. That is the
// point: on Fargate the deployment's own FANTRAX_* secrets are present in every
// task's environment, so a job that fell back to them would keep working while
// silently acting as the OPERATOR against whatever team id it was handed. A
// grant is the only thing that says which roster a run may touch.
//
// The zero value is inert and every refusal returns exactly it, so a caller
// that ignores the reason string still gets nothing usable.
type RunGrant struct {
	UserID UserID
	TeamID string
	FXRM   []byte // sealed session cookie
	Creds  []byte // sealed username/password, for the one permitted re-login
}

// Valid reports whether this grant authorizes anything.
func (g RunGrant) Valid() bool {
	return g.UserID != "" && g.TeamID != "" && len(g.FXRM) > 0 && len(g.Creds) > 0
}

// AuthorizeRun decides whether a job may run for uid, given that tenant's
// connection record (nil when there is none).
//
// THE NO-RETRY RULE LIVES HERE, and costs nothing extra. rosterbot-crq.17 asks
// for two things — refuse to run unless verified, and do not retry after a
// failed re-login — and they are one check. A failed re-login writes
// needs_reconnect, and this function then refuses every subsequent scheduled
// run because needs_reconnect is not verified. No cooldown timer, no attempt
// counter, nothing to reset: the same shape as opsalert.Streak deriving its
// whole decision from the append-only ledger.
//
// That stickiness is not fastidiousness. The Lineup job runs ~14 times a day;
// retrying a rejected password on each would lock a pilot out of their own
// Fantrax account using credentials they handed us, and would train Cloudflare
// to treat our egress IP as a bot.
func AuthorizeRun(uid UserID, conn *FantraxConnection) (RunGrant, string) {
	if conn == nil {
		return RunGrant{}, RunDeniedNoConnection
	}
	// Checked before status: a record for the wrong tenant may well be
	// verified, and acting on it is the failure where one user's job drives
	// another user's roster.
	if conn.UserID != uid {
		return RunGrant{}, RunDeniedWrongTenant
	}
	if conn.Status == ConnNeedsReconnect {
		return RunGrant{}, RunDeniedNeedsReconnect
	}
	if conn.Status != ConnVerified {
		return RunGrant{}, RunDeniedNotVerified
	}
	// The remaining three describe a verified record missing something it
	// should have. They are our bug rather than the user's, so they are named
	// separately instead of collapsing into "not verified" — an operator
	// seeing no_session should go looking at the connect task, not tell the
	// user to re-enter a password that is fine.
	if len(conn.FXRMCiphertext) == 0 {
		return RunGrant{}, RunDeniedNoSession
	}
	if len(conn.CredsCiphertext) == 0 {
		return RunGrant{}, RunDeniedNoCredentials
	}
	if conn.TeamID == "" {
		return RunGrant{}, RunDeniedNoTeam
	}
	return RunGrant{
		UserID: uid,
		TeamID: conn.TeamID,
		FXRM:   conn.FXRMCiphertext,
		Creds:  conn.CredsCiphertext,
	}, ""
}
