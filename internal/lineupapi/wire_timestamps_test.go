package lineupapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// nanoTime is the fixture every timestamp test in this file feeds, and the
// NON-ZERO nanosecond count is the whole reason it is a package-level var
// rather than an inline time.Date.
//
// A whole-second fixture marshals identically through a raw time.Time field, so
// a test built on one passes against the defect and proves nothing. That is not
// a hypothetical: the sites converted here divide into ones fed a UTC-midnight
// date (which have only ever emitted the safe shape, so the bug is latent) and
// ones fed time.Now() (measured 1000/1000 non-zero nanos, so the bug is live) —
// and a whole-second fixture cannot tell those two apart either.
var nanoTime = time.Date(2026, 8, 24, 21, 47, 27, 902729184, time.UTC)

// fractionalTimestamp matches an RFC3339 timestamp carrying a fractional
// second, which is exactly what the iOS client's
// ISO8601DateFormatter([.withInternetDateTime]) refuses to parse — returning
// nil, which is typically not an error there but a staleness check that
// silently never fires.
var fractionalTimestamp = regexp.MustCompile(`"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+`)

// assertNoFractionalTimestamp is the end-to-end half of this file: it asserts on
// the BYTES a client receives, not on a Go value, because the defect lives
// entirely in encoding/json's choice of layout for time.Time.
func assertNoFractionalTimestamp(t *testing.T, what string, body []byte) {
	t.Helper()
	if m := fractionalTimestamp.Find(body); m != nil {
		t.Errorf("%s emitted %s… — a fractional RFC3339 second, which the iOS "+
			"client's strict ISO8601 formatter decodes to nil (rosterbot-4e1j)\nbody: %s",
			what, m, body)
	}
}

// --- the backstop -----------------------------------------------------------

// TestWireTypes_CarryNoRawTimeTime walks every /v1 response type by REFLECTION
// and fails on any time.Time reachable from one.
//
// It is a backstop, not a replacement for the per-endpoint assertions below.
// The wiretime.Time conversions make the wrong shape unreachable at the sites
// that hold their own value, but some fields can only be fixed at the boundary
// (ArtifactStatus.LastModified is copied from a real time.Time off S3 object
// metadata; MembershipOut.AddedAt is copied from a durable record that must keep
// its time.Time), and for those the guard is what proves the conversion actually
// happened rather than being assumed.
//
// It walks TYPES rather than a populated value on purpose: a nil pointer or an
// empty slice on a fixture would silently skip a whole subtree, and the field
// this catches is by definition one nobody remembered to populate. The
// consequence is that a `Data any` field (RunOutput) is opaque to it, which is
// why every concrete per-job result type is named as its own root below.
//
// Modelled on cmd/output_results_test.go's TestBacktestToWireResult_RoundsEveryFloat,
// including its vacuity guard: a walk that finds nothing to check has to fail
// loudly, or it reports green against anything.
// WHAT THIS GUARD CANNOT SEE, stated because a partial guard read as a total one
// is worse than no guard (rosterbot-4e1j review).
//
// It walks Go TYPES, so it covers only endpoints that marshal one here. The five
// serveBlob passthrough routes — GET /v1/trades, /v1/trades/values,
// /v1/pool/available, /v1/reports/{name}, /v1/runs/{id}/progress — hand back
// bytes produced in internal/{tradeboard,availablepool,report,lineupgap,
// recaplog,progress}, and a raw time.Time added in any of those ships green past
// this test. That is not hypothetical: GET /v1/reports/views was measured
// emitting "generatedAt":"2026-08-26T21:08:27.746239Z" while this guard passed.
//
// Extending the walk to reach them would make lineupapi import all six producer
// packages purely for a test, which is why each instead owns its own shape —
// four by formatting to a string at construction, recaplog by wiretime.Time,
// each with its own wire-shape test. docs/ios-api-contract.md says so out loud
// so a client author does not read this guard as covering the whole surface.
func TestWireTypes_CarryNoRawTimeTime(t *testing.T) {
	// Every type that reaches a client as (part of) a /v1 response body.
	// Adding a route means adding its body here — that is the maintenance cost
	// this guard buys, and it is one line.
	roots := []any{
		// GET /v1/lineup/today, /v1/lineup/preview
		LineupResponse{},
		// GET /v1/runs, /v1/runs/{id}
		RunsResponse{}, RunDetail{},
		// GET /v1/runs/{id}/output — Data is `any`, so the concrete results follow.
		RunOutput{},
		jobwire.ProspectsResult{}, jobwire.WaiversResult{}, jobwire.ClaimsResult{}, jobwire.TransactionsResult{},
		jobwire.GSCheckResult{}, jobwire.BacktestResult{}, jobwire.GradeResult{},
		// GET /v1/notifications, /v1/jobs, POST /v1/jobs/{name}
		NotificationsResponse{}, JobsResponse{}, JobResponse{},
		// GET /v1/infra
		InfraStatus{},
		// GET /v1/me
		MeResponse{},
		// GET /v1/connect (the has-a-record body; the other is {"connected":false})
		connectStatusOut{},
		// GET /v1/memberships (and the POST/DELETE twins, same body)
		MembershipOut{},
		// GET /v1/tenants, POST /v1/tenants/invite, /v1/tenants/{id}/recovery
		TenantsResponse{}, inviteResponse{},
		// GET /v1/auth/passkeys
		passkeyOut{},
		// GET /v1/push/devices
		pushDeviceOut{},
		// GET /v1/sleeper/leagues
		SleeperLeague{},
	}

	timeType := reflect.TypeOf(time.Time{})
	wireType := reflect.TypeOf(wiretime.Time{})

	var wireFields int
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		switch rt {
		case timeType:
			t.Errorf("%s is a time.Time on a /v1 response body. encoding/json "+
				"marshals it as RFC3339Nano, whose fraction is variable-length and "+
				"vanishes when the nanosecond count happens to be zero — so this "+
				"field's shape depends on which call path supplies it. Use "+
				"wiretime.Time, or convert at the boundary if the underlying type "+
				"is a durable record (rosterbot-4e1j).", path)
			return
		case wireType:
			wireFields++
			return
		}
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(rt.Elem(), path+"[]")
		case reflect.Map:
			walk(rt.Elem(), path+"[k]")
		case reflect.Struct:
			// Recursive types (none today, but Slot->Player is one edge away
			// from being one) would otherwise loop forever.
			if seen[rt] {
				return
			}
			seen[rt] = true
			for i := range rt.NumField() {
				f := rt.Field(i)
				if !f.IsExported() {
					// Unexported fields never marshal, so they are not on the
					// wire — and wiretime.Time's own unexported time.Time is
					// exactly such a field. It is short-circuited above anyway.
					continue
				}
				name := f.Name
				if f.Anonymous {
					name = f.Type.Name()
				}
				walk(f.Type, path+"."+name)
			}
		}
	}
	for _, r := range roots {
		rt := reflect.TypeOf(r)
		walk(rt, rt.Name())
	}

	if wireFields == 0 {
		t.Fatal("walked every /v1 response type and found no wiretime.Time at all; " +
			"the walk is broken and this guard would pass against anything")
	}
}

// --- the sites, end to end --------------------------------------------------

// TestMe_LastVerifiedAtHasNoFractionalSecond drives GET /v1/me with a connection
// verified at a non-round instant — which is the only kind the connect task
// produces, since it stamps time.Now().
func TestMe_LastVerifiedAtHasNoFractionalSecond(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "alice", Email: "a@e.test", Role: RoleMember, Status: UserActive}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Connections: &memConnections{conn: &FantraxConnection{
			UserID: "alice", Status: ConnVerified, TeamID: "team-7",
			LastVerifiedAt: nanoTime, UpdatedAt: nanoTime,
		}}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "alice", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "GET /v1/me", rec.Body.Bytes())

	var body struct {
		Fantrax struct {
			LastVerifiedAt string `json:"last_verified_at"`
		} `json:"fantrax"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if got, want := body.Fantrax.LastVerifiedAt, "2026-08-24T21:47:27Z"; got != want {
		t.Errorf("last_verified_at = %q, want %q", got, want)
	}
}

// TestConnectStatus_LastVerifiedAtHasNoFractionalSecond covers the site the
// reflection walk could not have found: this body was an inline map[string]any
// handing out the stored time.Time directly, so there was no type to walk. It is
// a named struct now, which is what puts it inside the backstop's reach.
func TestConnectStatus_LastVerifiedAtHasNoFractionalSecond(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "alice", Email: "a@e.test", Role: RoleMember, Status: UserActive}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Connections: &memConnections{conn: &FantraxConnection{
			UserID: "alice", Status: ConnVerified, TeamID: "team-7",
			LastVerifiedAt: nanoTime, UpdatedAt: nanoTime,
		}}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/connect", "alice", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/connect = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "GET /v1/connect", rec.Body.Bytes())

	// The settings page keys off `connected` and `status`; assert the shape did
	// not shift when the map became a struct.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	for k, want := range map[string]any{
		"connected": true, "status": "verified", "team_id": "team-7",
		"last_error": "", "last_verified_at": "2026-08-24T21:47:27Z",
	} {
		if body[k] != want {
			t.Errorf("%s = %v, want %v", k, body[k], want)
		}
	}
}

// TestTenants_LastVerifiedAtHasNoFractionalSecond is the admin-tab twin of the
// test above: the same stored value, the same client, a different response.
func TestTenants_LastVerifiedAtHasNoFractionalSecond(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "admin1", Email: "op@e.test", Role: RoleAdmin, Status: UserActive}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Connections: &memConnections{conn: &FantraxConnection{
			UserID: "admin1", Status: ConnVerified, LastVerifiedAt: nanoTime,
		}}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/tenants = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "GET /v1/tenants", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"last_verified_at":"2026-08-24T21:47:27Z"`) {
		t.Errorf("tenants row missing the canonical last_verified_at: %s", rec.Body)
	}
}

// TestTenantInvite_ExpiresAtHasNoFractionalSecond covers the one site in this
// bead that has NEVER emitted the parseable shape: expires_at is
// time.Now().UTC().Add(ttl), so its nanosecond count is non-zero on every mint.
func TestTenantInvite_ExpiresAtHasNoFractionalSecond(t *testing.T) {
	h, secret, _ := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/invite", "admin1",
		`{"email":"new@e.test","team_id":"team-9"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "POST /v1/tenants/invite", rec.Body.Bytes())

	var body struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	// Parsed with the STRICT layout the client uses, not RFC3339Nano: that is
	// the assertion, since RFC3339Nano would accept the broken shape too.
	if _, err := time.Parse("2006-01-02T15:04:05Z07:00", body.ExpiresAt); err != nil {
		t.Errorf("expires_at = %q is not strict RFC3339: %v", body.ExpiresAt, err)
	}
}

// TestListPasskeys_CreatedAtHasNoFractionalSecond. The stored CredentialMeta
// keeps its time.Time — it is a durable record read back by Go — so this proves
// the conversion happens at the response boundary rather than in the store.
func TestListPasskeys_CreatedAtHasNoFractionalSecond(t *testing.T) {
	h, secret, users := adminFixture(t)
	cred := webauthn.Credential{ID: []byte("cred-1")}
	if err := users.PutCredential(context.Background(), "bob", cred); err != nil {
		t.Fatal(err)
	}
	if err := users.PutCredentialMeta(context.Background(), "bob", cred.ID,
		CredentialMeta{Name: "phone", CreatedAt: nanoTime}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/auth/passkeys", "bob", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/auth/passkeys = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "GET /v1/auth/passkeys", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"created_at":"2026-08-24T21:47:27Z"`) {
		t.Errorf("passkey row missing the canonical created_at: %s", rec.Body)
	}

	// And the store still holds full precision: converting at the boundary must
	// not have quietly truncated the durable record.
	metas, err := users.CredentialMetas(context.Background(), "bob")
	if err != nil {
		t.Fatalf("CredentialMetas: %v", err)
	}
	if got := metas[CredentialKey(cred.ID)].CreatedAt; !got.Equal(nanoTime) {
		t.Errorf("stored CreatedAt = %v, want %v — the durable record must keep "+
			"the precision it was written with", got, nanoTime)
	}
}

// TestMemberships_AddedAtHasNoFractionalSecond. Membership is stored INSIDE the
// user record (JSON on disk, attributevalue in DynamoDB), so it keeps its
// time.Time and MembershipsOut is what makes the response safe. The Fantrax row
// is projected from User.CreatedAt and the Sleeper row carries its own AddedAt,
// so both paths are exercised by one request.
func TestMemberships_AddedAtHasNoFractionalSecond(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "bob", Email: "b@e.test", Role: RoleMember, Status: UserActive,
		TeamID: "team-7", CreatedAt: nanoTime,
		Memberships: []Membership{{
			Platform: PlatformSleeper, LeagueID: "123", TeamID: "u9",
			DisplayName: "Dynasty", AddedAt: nanoTime,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/memberships", "bob", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/memberships = %d: %s", rec.Code, rec.Body)
	}
	assertNoFractionalTimestamp(t, "GET /v1/memberships", rec.Body.Bytes())

	var body struct {
		Memberships []struct {
			Platform string `json:"platform"`
			AddedAt  string `json:"added_at"`
		} `json:"memberships"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(body.Memberships) != 2 {
		t.Fatalf("memberships = %d, want 2 (projected Fantrax + stored Sleeper): %s",
			len(body.Memberships), rec.Body)
	}
	for _, m := range body.Memberships {
		if m.AddedAt != "2026-08-24T21:47:27Z" {
			t.Errorf("%s added_at = %q, want the canonical shape", m.Platform, m.AddedAt)
		}
	}

	// The stored record keeps full precision — this conversion is a projection,
	// not a migration.
	u, ok, err := users.GetUser(context.Background(), "bob")
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if got := u.Memberships[0].AddedAt; !got.Equal(nanoTime) {
		t.Errorf("stored AddedAt = %v, want %v", got, nanoTime)
	}
}

// TestInfra_TimestampsHaveNoFractionalSecond covers both of GET /v1/infra's
// timestamps at once, and they fail for different reasons: generated_at is the
// request moment (live on every response), while last_modified is copied off S3
// object metadata, whose precision is whatever S3 recorded.
func TestInfra_TimestampsHaveNoFractionalSecond(t *testing.T) {
	art := layout.Cache
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		art.S3Prefix: {Objects: 3, Bytes: 99, LastModified: nanoTime},
	}}
	st := buildStatus(context.Background(), lister, []layout.Artifact{art}, nanoTime.Add(time.Minute))

	body, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertNoFractionalTimestamp(t, "GET /v1/infra", body)
	for _, want := range []string{
		`"generated_at":"2026-08-24T21:48:27Z"`,
		`"last_modified":"2026-08-24T21:47:27Z"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("infra status missing %s: %s", want, body)
		}
	}

	// The staleness arithmetic still runs on the untruncated listing value, so
	// the display conversion cannot have moved a health verdict.
	if len(st.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(st.Artifacts))
	}
	if got, want := st.Artifacts[0].AgeSeconds, 60.0; fmt.Sprintf("%.3f", got) != fmt.Sprintf("%.3f", want) {
		t.Errorf("age_seconds = %v, want %v", got, want)
	}
}
