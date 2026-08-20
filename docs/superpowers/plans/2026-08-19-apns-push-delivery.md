# APNs Push Delivery (backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the bot's nine fantasy-event notifications to registered iOS devices via APNs, per user, so the RosterBot app replaces Pushover as the surface a manager actually reads.

**Architecture:** `internal/notify` stops being a Pushover client and becomes a dispatcher: call sites emit an `Event`, the dispatcher writes the durable activity-feed record **first** (its ID is what the push payload carries), then hands the event to best-effort delivery sinks — APNs, and Pushover during the cutover window. Device tokens live in the existing `IDENTITY_TABLE` DynamoDB store beside passkeys, registered through three new session-authenticated API routes. The Fargate task talks to APNs directly over HTTP/2; there is no new compute component.

**Tech Stack:** Go 1.26.1, stdlib only for the new crypto (`crypto/ecdsa`, `crypto/x509`, `encoding/pem`) — **no JWT library is to be added**; `aws-sdk-go-v2` DynamoDB (already a dependency); AWS CDK in Go (`infra/`).

**Spec:** `~/RosterbotApp/docs/superpowers/specs/2026-08-19-apns-push-notifications-design.md`. The plan argues from the spec; read both. Decision IDs below (D1–D5) refer to that document.

**Companion plan:** the iOS client half is
`~/RosterbotApp/docs/superpowers/plans/2026-08-19-apns-push-client.md`. It
consumes the three routes built in Task 3 here. **This plan must land and
deploy first** — the client cannot register a device against an endpoint that
does not exist.

## Global Constraints

- **Exactly nine call sites convert (D1).** `internal/lineuprun/lineuprun.go` lines 282, 437, 542; `internal/claims/run.go:126`; `internal/waivers/run.go:187`; `internal/transactions/transactions.go:145`; `internal/prospects/run.go:302`; `cmd/football_trades.go:426`; `internal/gscheck/gscheck.go:235`. Line numbers are as of 2026-08-19 and will drift — match on the call, not the line.
- **Four call sites MUST NOT convert (D1).** `cmd/connect_feed.go:113` (`notifyOperator`), `cmd/root.go:141` (`cache.Notify`, stale-cache fallback), `internal/gscheck/gscheck.go:122` (GS limit fetch failed), `cmd/shadow.go:131` (projection status). These are the personal operator channel — `CLAUDE.md` names two of them as such — and they must keep calling `notify.SendPushover` directly. **`SendPushover` therefore stays exported.** Converting these would mean a failure that stops the bot reaching the network also stops its own alert about that failure.
- **The feed write happens before delivery, always.** Today `notify.Recorder` fires *inside* `SendPushover`, making the durable record a side effect of a delivery attempt. That ordering must be inverted, not preserved: the record's ID is an input to the APNs payload, so it cannot be produced afterward.
- **A delivery sink's failure never fails the caller.** `Send` returns an error only when the feed write fails. This matches the semantics `notify.Recorder`'s existing comment documents ("Best-effort: ... its outcome never affects SendPushover's result").
- **ES256 JWTs need raw `r||s`, not ASN.1.** Use `ecdsa.Sign` (which returns `r, s *big.Int`) and left-pad each to exactly 32 bytes. **Do not use `ecdsa.SignASN1`** — it returns DER, which APNs rejects with `InvalidProviderToken`, and the failure looks like a credentials problem rather than an encoding one.
- **Store `environment` on the device record; never infer it.** A sandbox token sent to the production APNs host returns `400 BadDeviceToken`, which is byte-identical to the response for a genuinely dead token. Since the sender deletes records on `BadDeviceToken`, inferring the environment would silently delete every development device. The client supplies `environment` and `bundle_id`; the sender routes on them.
- **Test with the file-backed store, not DynamoDB.** `internal/lineupapi/fileuser.go` exists so tests run without AWS. Follow it: every store interface gets both a DDB and a file implementation, and unit tests use the file one. This mirrors the `ddbusertest`/`enrollmenttest` shared-conformance pattern already in the repo.
- **Run tests with `make test`** (equivalently `go test ./internal/...`). Package-scoped runs during development: `go test ./internal/notify/ -run TestName -v`.
- **No new Go module dependencies.** Verified 2026-08-19: `go.mod` has no JWT library, and ES256 signing is ~40 lines of stdlib.

---

### Task 1: Push device model, store interface, and file implementation

**Files:**
- Create: `internal/lineupapi/pushdevice.go`
- Create: `internal/lineupapi/pushdevice_test.go`
- Modify: `internal/lineupapi/fileuser.go` (append the `PushDeviceStore` methods)

**Interfaces:**
- Consumes: `lineupapi.UserID` (existing, `internal/lineupapi/credentials.go`).
- Produces: type `PushDevice`; interface `PushDeviceStore` with `PutPushDevice(ctx, UserID, PushDevice) (PushDevice, error)`, `PushDevices(ctx, UserID) ([]PushDevice, error)`, `DeletePushDevice(ctx, UserID, id string) error`. `FileUserStore` implements all three. Tasks 2, 3 and 5 all depend on these exact signatures.

- [ ] **Step 1: Write the failing test**

Create `internal/lineupapi/pushdevice_test.go`:

```go
package lineupapi_test

import (
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func TestPutPushDeviceIsIdempotentOnToken(t *testing.T) {
	st := lineupapi.NewFileUserStore(t.TempDir())
	ctx := context.Background()

	first, err := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{
		Token: "aabbcc", Environment: "sandbox", BundleID: "dev.rosterbot.app.debug",
		Model: "iPhone 17", CreatedAt: "2026-08-19T00:00:00Z", LastSeenAt: "2026-08-19T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	if first.ID == "" {
		t.Fatal("PutPushDevice must assign an ID")
	}

	// Re-registering the SAME token must update in place, not insert a second
	// row. A device re-registers on every launch (tokens rotate on restore),
	// so an insert-only store would grow one row per launch forever.
	second, err := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{
		Token: "aabbcc", Environment: "sandbox", BundleID: "dev.rosterbot.app.debug",
		Model: "iPhone 17", CreatedAt: "2026-08-20T00:00:00Z", LastSeenAt: "2026-08-20T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("re-registering the same token made a new device: %q then %q", first.ID, second.ID)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("CreatedAt must be preserved across re-registration: got %q want %q", second.CreatedAt, first.CreatedAt)
	}
	if second.LastSeenAt != "2026-08-20T00:00:00Z" {
		t.Errorf("LastSeenAt must advance: got %q", second.LastSeenAt)
	}

	devices, err := st.PushDevices(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("want 1 device after two registrations of one token, got %d", len(devices))
	}
}

func TestPushDevicesAreScopedToTheirUser(t *testing.T) {
	st := lineupapi.NewFileUserStore(t.TempDir())
	ctx := context.Background()

	if _, err := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{Token: "aaa", Environment: "production"}); err != nil {
		t.Fatalf("put u1: %v", err)
	}
	if _, err := st.PutPushDevice(ctx, "u2", lineupapi.PushDevice{Token: "bbb", Environment: "production"}); err != nil {
		t.Fatalf("put u2: %v", err)
	}

	got, err := st.PushDevices(ctx, "u1")
	if err != nil {
		t.Fatalf("list u1: %v", err)
	}
	if len(got) != 1 || got[0].Token != "aaa" {
		t.Fatalf("u1 must see only its own device, got %+v", got)
	}
}

func TestDeletePushDeviceRemovesOnlyThatDevice(t *testing.T) {
	st := lineupapi.NewFileUserStore(t.TempDir())
	ctx := context.Background()

	keep, _ := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{Token: "keep", Environment: "production"})
	drop, _ := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{Token: "drop", Environment: "production"})

	if err := st.DeletePushDevice(ctx, "u1", drop.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := st.PushDevices(ctx, "u1")
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("want only %s to survive, got %+v", keep.ID, got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/lineupapi/ -run TestPutPushDeviceIsIdempotentOnToken -v`
Expected: FAIL — `undefined: lineupapi.PushDevice`.

(If `NewFileUserStore` has a different name or signature, read `internal/lineupapi/fileuser.go` and use the real constructor. Do not invent one.)

- [ ] **Step 3: Write the model and interface**

Create `internal/lineupapi/pushdevice.go`:

```go
package lineupapi

import "context"

// PushDevice is one iOS device registered to receive push notifications.
//
// Environment and BundleID are stored rather than inferred because APNs
// answers 400 BadDeviceToken both for a genuinely dead token and for a
// sandbox token presented to the production host. The sender deletes records
// on BadDeviceToken (see the APNs sender), so guessing the environment would
// make it delete every development device and look correct doing it.
type PushDevice struct {
	ID          string          `json:"id"`
	Token       string          `json:"token"`
	Environment string          `json:"environment"` // sandbox | production
	BundleID    string          `json:"bundle_id"`
	Model       string          `json:"model"`
	CreatedAt   string          `json:"created_at"`  // RFC3339 UTC
	LastSeenAt  string          `json:"last_seen_at"` // RFC3339 UTC

	// Preferences is reserved for per-kind muting and is unused in v1: an
	// empty map means every kind is delivered. It exists now so adding the
	// feature later does not require reshaping stored records.
	Preferences map[string]bool `json:"preferences,omitempty"`
}

// PushDeviceStore is the per-user device registry. Implemented by the DynamoDB
// store (production) and by FileUserStore (local and tests).
type PushDeviceStore interface {
	// PutPushDevice registers a device, or updates it in place when a record
	// with the same Token already exists for this user. It returns the stored
	// record, whose ID the caller needs in order to revoke it later.
	//
	// Idempotency is on Token, not ID: the client re-registers on every launch
	// because APNs tokens rotate on restore and reinstall, and an insert-only
	// store would accumulate one dead row per launch.
	PutPushDevice(ctx context.Context, uid UserID, d PushDevice) (PushDevice, error)

	PushDevices(ctx context.Context, uid UserID) ([]PushDevice, error)
	DeletePushDevice(ctx context.Context, uid UserID, id string) error
}
```

- [ ] **Step 4: Implement the file-backed store**

Append to `internal/lineupapi/fileuser.go`, matching the one-file-per-item
layout already used there (read the existing credential methods first and
follow their naming and error handling exactly):

```go
// pushDeviceDir is one directory per user, one JSON file per device, mirroring
// how this store already lays out credentials.
func (s *FileUserStore) pushDeviceDir(uid UserID) string {
	return filepath.Join(s.dir, "push", string(uid))
}

func (s *FileUserStore) PutPushDevice(ctx context.Context, uid UserID, d PushDevice) (PushDevice, error) {
	existing, err := s.PushDevices(ctx, uid)
	if err != nil {
		return PushDevice{}, err
	}
	for _, e := range existing {
		if e.Token == d.Token {
			// Update in place. CreatedAt is the device's first sighting and
			// must survive; LastSeenAt is what the new registration advances.
			d.ID, d.CreatedAt = e.ID, e.CreatedAt
			break
		}
	}
	if d.ID == "" {
		d.ID = NewPushDeviceID()
	}
	if err := os.MkdirAll(s.pushDeviceDir(uid), 0o755); err != nil {
		return PushDevice{}, err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return PushDevice{}, err
	}
	if err := os.WriteFile(filepath.Join(s.pushDeviceDir(uid), d.ID+".json"), data, 0o644); err != nil {
		return PushDevice{}, err
	}
	return d, nil
}

func (s *FileUserStore) PushDevices(_ context.Context, uid UserID) ([]PushDevice, error) {
	entries, err := os.ReadDir(s.pushDeviceDir(uid))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []PushDevice{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.pushDeviceDir(uid), e.Name()))
		if err != nil {
			continue
		}
		var d PushDevice
		if json.Unmarshal(data, &d) == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *FileUserStore) DeletePushDevice(_ context.Context, uid UserID, id string) error {
	err := os.Remove(filepath.Join(s.pushDeviceDir(uid), id+".json"))
	if os.IsNotExist(err) {
		return nil // deleting an absent device is a success, not an error
	}
	return err
}

// NewPushDeviceID returns an opaque, URL-safe device id. Exported because the
// DynamoDB store (Task 2) mints ids the same way. The APNs token is NOT used
// as the id: it rotates, and it would then appear in the DELETE route's path
// and therefore in access logs.
func NewPushDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
```

Add `crypto/rand`, `encoding/base64`, `encoding/json`, `os`, `path/filepath`,
`sort`, `strings` to that file's imports if not already present.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lineupapi/ -run 'TestPutPushDevice|TestPushDevices|TestDeletePushDevice' -v`
Expected: PASS, 3 tests.

- [ ] **Step 6: Add the compile-time interface assertion**

Append to `internal/lineupapi/pushdevice.go`:

```go
var _ PushDeviceStore = (*FileUserStore)(nil)
```

Run: `go build ./...`
Expected: success. A signature drift between interface and implementation is now a compile error rather than a runtime surprise.

- [ ] **Step 7: Commit**

```bash
git add internal/lineupapi/pushdevice.go internal/lineupapi/pushdevice_test.go internal/lineupapi/fileuser.go
git commit -m "feat(push): PushDevice model, store interface, file implementation"
```

---

### Task 2: DynamoDB implementation of `PushDeviceStore`

**Files:**
- Modify: `internal/lineupapi/ddbuser/ddbuser.go` (append the three methods)
- Modify: `internal/lineupapi/ddbuser/ddbuser_test.go` (append tests)

**Interfaces:**
- Consumes: `lineupapi.PushDevice`, `lineupapi.PushDeviceStore` (Task 1).
- Produces: `*ddbuser.Store` satisfies `lineupapi.PushDeviceStore`. Task 5's wiring depends on this.

- [ ] **Step 1: Read the existing key helpers**

Run: `sed -n '70,120p' internal/lineupapi/ddbuser/ddbuser.go`

Note the conventions you must follow: attribute names are lowercase `pk`/`sk`;
`userPK(id)` returns `"USER#" + id`; `s(v)` wraps a string as an
`AttributeValue`; `st.key(pk, sk)` builds a key map. Device items use
`pk = USER#<uid>`, `sk = PUSHDEVICE#<id>` — the same one-item-per-record layout
as `CRED#`, so a device is found by the same `Query` prefix technique as
`Credentials`.

- [ ] **Step 2: Write the failing test**

Append to `internal/lineupapi/ddbuser/ddbuser_test.go` (use the same in-memory
`API` double the existing tests in that file use — read one of them first and
construct the store identically):

```go
func TestPushDeviceRoundTripAndTokenIdempotence(t *testing.T) {
	st := newTestStore(t) // whatever the existing tests in this file use
	ctx := context.Background()

	first, err := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{
		Token: "tok-a", Environment: "production", BundleID: "dev.rosterbot.app",
		Model: "iPhone 17", CreatedAt: "2026-08-19T00:00:00Z", LastSeenAt: "2026-08-19T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	again, err := st.PutPushDevice(ctx, "u1", lineupapi.PushDevice{
		Token: "tok-a", Environment: "production", BundleID: "dev.rosterbot.app",
		Model: "iPhone 17", CreatedAt: "2026-08-25T00:00:00Z", LastSeenAt: "2026-08-25T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("same token produced two devices: %q then %q", first.ID, again.ID)
	}

	got, err := st.PushDevices(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 device, got %d", len(got))
	}
	if got[0].Environment != "production" || got[0].BundleID != "dev.rosterbot.app" {
		t.Errorf("environment/bundle must round-trip, got %+v", got[0])
	}

	if err := st.DeletePushDevice(ctx, "u1", first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := st.PushDevices(ctx, "u1"); len(got) != 0 {
		t.Fatalf("want 0 after delete, got %d", len(got))
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/lineupapi/ddbuser/ -run TestPushDeviceRoundTrip -v`
Expected: FAIL — `st.PutPushDevice undefined`.

- [ ] **Step 4: Implement**

Append to `internal/lineupapi/ddbuser/ddbuser.go`:

```go
const pushDevicePrefix = "PUSHDEVICE#"

func pushDeviceSK(id string) string { return pushDevicePrefix + id }

func (st *Store) PutPushDevice(ctx context.Context, uid lineupapi.UserID, d lineupapi.PushDevice) (lineupapi.PushDevice, error) {
	// Idempotency is on Token, and the item is keyed by ID, so finding the
	// existing row means reading this user's devices. The set is a handful of
	// items per user, which is why this needs no secondary index.
	existing, err := st.PushDevices(ctx, uid)
	if err != nil {
		return lineupapi.PushDevice{}, err
	}
	for _, e := range existing {
		if e.Token == d.Token {
			d.ID, d.CreatedAt = e.ID, e.CreatedAt
			break
		}
	}
	if d.ID == "" {
		d.ID = lineupapi.NewPushDeviceID()
	}

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return lineupapi.PushDevice{}, err
	}
	item["pk"] = s(userPK(uid))
	item["sk"] = s(pushDeviceSK(d.ID))

	if _, err := st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table),
		Item:      item,
	}); err != nil {
		return lineupapi.PushDevice{}, err
	}
	return d, nil
}

func (st *Store) PushDevices(ctx context.Context, uid lineupapi.UserID) ([]lineupapi.PushDevice, error) {
	out, err := st.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": s(userPK(uid)), ":sk": s(pushDevicePrefix),
		},
	})
	if err != nil {
		return nil, err
	}
	devices := []lineupapi.PushDevice{}
	for _, item := range out.Items {
		var d lineupapi.PushDevice
		if attributevalue.UnmarshalMap(item, &d) == nil {
			devices = append(devices, d)
		}
	}
	return devices, nil
}

func (st *Store) DeletePushDevice(ctx context.Context, uid lineupapi.UserID, id string) error {
	_, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table),
		Key:       st.key(userPK(uid), pushDeviceSK(id)),
	})
	return err
}
```

If `Query` is not already on the `API` interface in this package, add it,
following how `PutItem`/`DeleteItem` are declared there.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/lineupapi/ddbuser/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 6: Assert the interface**

Append to `internal/lineupapi/ddbuser/ddbuser.go`:

```go
var _ lineupapi.PushDeviceStore = (*Store)(nil)
```

Run: `go build ./... && make test`
Expected: build succeeds, full suite passes.

- [ ] **Step 7: Commit**

```bash
git add internal/lineupapi/ddbuser/ internal/lineupapi/pushdevice.go internal/lineupapi/fileuser.go
git commit -m "feat(push): DynamoDB PushDeviceStore implementation"
```

---

### Task 3: Device registration API routes

**Files:**
- Create: `internal/lineupapi/pushroutes.go`
- Create: `internal/lineupapi/pushroutes_test.go`
- Modify: `internal/lineupapi/handler.go` (add three `mux.HandleFunc` lines near line 133, beside the passkey routes; add a `PushDevices` field to `Config`)

**Interfaces:**
- Consumes: `PushDeviceStore` (Task 1), `cfg.sessionUser(r)`, `writeJSON`, `writeErr` (all existing).
- Produces: `POST /v1/push/devices`, `GET /v1/push/devices`, `DELETE /v1/push/devices/{id}`; `Config.PushDevices PushDeviceStore`. The iOS client plan depends on these three routes and on the exact JSON field names below.

- [ ] **Step 1: Write the failing test**

Create `internal/lineupapi/pushroutes_test.go`.

First run `grep -rn 'func newTest' internal/lineupapi/*_test.go` to find the
existing handler-test helpers and use their real names and signatures — the
names below are placeholders for whatever this package already has. If no
session-bearing helper exists, write one modelled on how an existing
session-authenticated handler test (`handleConnect`, `handleMe`, or the passkey
routes) builds its request: construct a `Config` whose `Users` and
`PushDevices` are `FileUserStore` instances over `t.TempDir()`, then attach a
caller the same way that test does. Do not invent a new session mechanism.

```go
package lineupapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterDeviceRequiresASession(t *testing.T) {
	h := newTestHandler(t) // existing helper in this package's tests

	req := httptest.NewRequest(http.MethodPost, "/v1/push/devices",
		strings.NewReader(`{"token":"aa","environment":"sandbox","bundle_id":"dev.rosterbot.app.debug"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The operator bearer token has no UserID by construction, so there is no
	// account a device could belong to. Same rule handleConnect enforces.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without a session, got %d", rec.Code)
	}
}

func TestRegisterDeviceRejectsUnknownEnvironment(t *testing.T) {
	h, _ := newTestHandlerWithSession(t, "u1")

	req := httptest.NewRequest(http.MethodPost, "/v1/push/devices",
		strings.NewReader(`{"token":"aa","environment":"staging","bundle_id":"dev.rosterbot.app"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Only sandbox and production exist. Accepting anything else would store a
	// record the sender cannot route, which surfaces later as an undeliverable
	// device rather than as a rejected registration.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for an unknown environment, got %d", rec.Code)
	}
}

func TestRegisterThenListThenRevoke(t *testing.T) {
	h, _ := newTestHandlerWithSession(t, "u1")

	req := httptest.NewRequest(http.MethodPost, "/v1/push/devices",
		strings.NewReader(`{"token":"aa","environment":"sandbox","bundle_id":"dev.rosterbot.app.debug","model":"iPhone 17"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var reg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.ID == "" {
		t.Fatal("register must return the device id; the client needs it to revoke on sign-out")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/push/devices", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), reg.ID) {
		t.Fatalf("list: want the registered device, got %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/push/devices/"+reg.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", rec.Code)
	}
}

func TestListNeverLeaksTheAPNsToken(t *testing.T) {
	h, _ := newTestHandlerWithSession(t, "u1")

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/push/devices",
		strings.NewReader(`{"token":"secret-token-value","environment":"sandbox","bundle_id":"dev.rosterbot.app.debug"}`)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/push/devices", nil))

	// The listing exists so a person can see and revoke their devices. The raw
	// token is a delivery credential and has no business in that view.
	if strings.Contains(rec.Body.String(), "secret-token-value") {
		t.Fatalf("device listing leaked the APNs token: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/lineupapi/ -run 'TestRegisterDevice|TestRegisterThenList|TestListNeverLeaks' -v`
Expected: FAIL — 404s, because the routes do not exist yet.

- [ ] **Step 3: Implement the handlers**

Create `internal/lineupapi/pushroutes.go`:

```go
package lineupapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// maxPushTokenLen bounds the stored token. An APNs device token is 64 hex
// characters today and 100+ under some configurations; this is a sanity bound
// against a client posting an unbounded body into durable storage.
const maxPushTokenLen = 512

type registerDeviceIn struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	BundleID    string `json:"bundle_id"`
	Model       string `json:"model"`
}

// pushDeviceOut is the listing shape. It deliberately omits Token: this view
// exists so a person can identify and revoke their own devices, and the raw
// token is a delivery credential, not an identifier for humans.
type pushDeviceOut struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	BundleID    string `json:"bundle_id"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

func (cfg Config) handleRegisterPushDevice(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.sessionUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var in registerDeviceIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Token == "" || len(in.Token) > maxPushTokenLen {
		writeErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	if in.Environment != "sandbox" && in.Environment != "production" {
		writeErr(w, http.StatusBadRequest, "environment must be sandbox or production")
		return
	}
	if in.BundleID == "" {
		writeErr(w, http.StatusBadRequest, "bundle_id is required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	stored, err := cfg.PushDevices.PutPushDevice(r.Context(), u.ID, PushDevice{
		Token:       in.Token,
		Environment: in.Environment,
		BundleID:    in.BundleID,
		Model:       in.Model,
		CreatedAt:   now,
		LastSeenAt:  now,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device store unavailable")
		return
	}
	// The id is the whole point of the response: the client stores it and
	// sends it back to DELETE on sign-out.
	writeJSON(w, http.StatusOK, map[string]any{"id": stored.ID})
}

func (cfg Config) handleListPushDevices(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.sessionUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	devices, err := cfg.PushDevices.PushDevices(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device store unavailable")
		return
	}
	out := []pushDeviceOut{}
	for _, d := range devices {
		out = append(out, pushDeviceOut{
			ID: d.ID, Environment: d.Environment, BundleID: d.BundleID,
			Model: d.Model, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (cfg Config) handleRevokePushDevice(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.sessionUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Scoped to the caller by construction: the key is (their user id, this
	// device id), so a caller supplying someone else's id deletes nothing.
	if err := cfg.PushDevices.DeletePushDevice(r.Context(), u.ID, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Register the routes and the Config field**

In `internal/lineupapi/handler.go`, add to the `Config` struct beside the other
stores:

```go
	PushDevices PushDeviceStore
```

and beside the passkey routes (around line 133):

```go
	mux.HandleFunc("POST /v1/push/devices", cfg.handleRegisterPushDevice)
	mux.HandleFunc("GET /v1/push/devices", cfg.handleListPushDevices)
	mux.HandleFunc("DELETE /v1/push/devices/{id}", cfg.handleRevokePushDevice)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lineupapi/ -run 'TestRegisterDevice|TestRegisterThenList|TestListNeverLeaks' -v`
Expected: PASS, 4 tests.

Then: `make test`
Expected: full suite green.

- [ ] **Step 6: Commit**

```bash
git add internal/lineupapi/pushroutes.go internal/lineupapi/pushroutes_test.go internal/lineupapi/handler.go
git commit -m "feat(push): device registration, listing and revocation routes"
```

---

### Task 4: APNs provider JWT (stdlib ES256)

**Files:**
- Create: `internal/apns/token.go`
- Create: `internal/apns/token_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `apns.ParseAuthKey(pemBytes []byte) (*ecdsa.PrivateKey, error)`; `apns.NewTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *TokenSource`; `(*TokenSource).Token(now time.Time) (string, error)`. Task 5 consumes all three.

- [ ] **Step 1: Write the failing test**

Create `internal/apns/token_test.go`:

```go
package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), key
}

func TestTokenIsVerifiableES256WithRawSignature(t *testing.T) {
	pemBytes, want := testKeyPEM(t)
	key, err := ParseAuthKey(pemBytes)
	if err != nil {
		t.Fatalf("ParseAuthKey: %v", err)
	}
	if !key.Equal(want) {
		t.Fatal("parsed key differs from the generated one")
	}

	ts := NewTokenSource(key, "KEYID123", "8KBU54NP6U")
	tok, err := ts.Token(time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}

	var hdr struct{ Alg, Kid, Typ string }
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("header: %v", err)
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEYID123" || hdr.Typ != "JWT" {
		t.Errorf("header = %+v", hdr)
	}

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Iss != "8KBU54NP6U" || claims.Iat != 1_800_000_000 {
		t.Errorf("claims = %+v", claims)
	}

	// THE important assertion. JWT ES256 requires a raw 64-byte r||s
	// signature. ecdsa.SignASN1 returns DER instead, which APNs rejects as
	// InvalidProviderToken -- a message that reads like a wrong key rather
	// than a wrong encoding, so this is very expensive to diagnose in prod.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature must be 64 raw bytes (r||s), got %d -- ASN.1 DER was almost certainly used", len(sig))
	}

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("signature does not verify against the signing key")
	}
}

func TestTokenIsCachedThenRefreshedAfterTheWindow(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	key, _ := ParseAuthKey(pemBytes)
	ts := NewTokenSource(key, "K", "T")

	base := time.Unix(1_800_000_000, 0)
	first, _ := ts.Token(base)

	// Apple rejects provider tokens refreshed more often than once per 20
	// minutes, so within the window the SAME token must come back.
	same, _ := ts.Token(base.Add(19 * time.Minute))
	if same != first {
		t.Error("token must be reused inside the refresh window")
	}

	// And it must not be used past its 1-hour validity.
	later, _ := ts.Token(base.Add(61 * time.Minute))
	if later == first {
		t.Error("token must be regenerated once the validity window has passed")
	}
}

func TestParseAuthKeyRejectsNonECKeys(t *testing.T) {
	if _, err := ParseAuthKey([]byte("not a pem block")); err == nil {
		t.Error("want an error for malformed PEM")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/apns/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Create `internal/apns/token.go`:

```go
// Package apns sends push notifications through Apple Push Notification
// service using provider-token (JWT) authentication.
package apns

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ParseAuthKey reads the PKCS#8 PEM that Apple issues as a .p8 file.
func ParseAuthKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("apns: auth key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse auth key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: auth key is %T, want *ecdsa.PrivateKey", parsed)
	}
	return key, nil
}

// tokenValidity is Apple's hard ceiling on a provider token's lifetime.
// refreshFloor is its matching floor: refreshing more often than once per 20
// minutes is rejected. Regenerating at 50 minutes sits safely inside both.
const (
	tokenValidity = time.Hour
	refreshAfter  = 50 * time.Minute
)

// TokenSource mints and caches the provider token. One per process: a Fargate
// task is short-lived enough that it will usually mint exactly one.
type TokenSource struct {
	key            *ecdsa.PrivateKey
	keyID, teamID  string

	mu     sync.Mutex
	cached string
	issued time.Time
}

func NewTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *TokenSource {
	return &TokenSource{key: key, keyID: keyID, teamID: teamID}
}

func (ts *TokenSource) Token(now time.Time) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.cached != "" && now.Sub(ts.issued) < refreshAfter {
		return ts.cached, nil
	}
	tok, err := ts.sign(now)
	if err != nil {
		return "", err
	}
	ts.cached, ts.issued = tok, now
	return tok, nil
}

func (ts *TokenSource) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": ts.keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": ts.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))

	// ecdsa.Sign, NOT ecdsa.SignASN1. JWS ES256 (RFC 7518 s3.4) is the raw
	// concatenation of r and s, each left-padded to the curve's byte length.
	// SignASN1 returns a DER SEQUENCE, which APNs rejects as
	// InvalidProviderToken -- a message that points at the key rather than at
	// the encoding, making it very expensive to diagnose from production.
	r, s, err := ecdsa.Sign(rand.Reader, ts.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return signingInput + "." + enc.EncodeToString(sig), nil
}
```

Note `big.Int.FillBytes` handles the left-padding: it writes the value
right-aligned into the given slice and panics if it does not fit, which for a
P-256 scalar it always will.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/apns/ -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/apns/
git commit -m "feat(apns): provider-token JWT signing with stdlib ES256"
```

---

### Task 5: APNs sender with dead-token pruning

**Files:**
- Create: `internal/apns/client.go`
- Create: `internal/apns/client_test.go`

**Interfaces:**
- Consumes: `TokenSource` (Task 4); `lineupapi.PushDevice`, `lineupapi.PushDeviceStore` (Task 1).
- Produces: `apns.Client` with `New(tokens *TokenSource, httpClient *http.Client) *Client` and `Push(ctx, d lineupapi.PushDevice, p Payload) error`; `apns.Payload{Title, Body, NotificationID string}`; sentinel `apns.ErrDeviceGone`. Task 7's sink consumes these.

- [ ] **Step 1: Write the failing test**

Create `internal/apns/client_test.go`:

```go
package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := New(NewTokenSource(key, "K", "T"), srv.Client())
	c.hostOverride = srv.URL // test seam; production derives the host from environment
	return c, srv
}

func TestPushSendsCorrectTopicAndPayload(t *testing.T) {
	var gotPath, gotTopic, gotAuth, gotPushType string
	var body map[string]any

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTopic = r.URL.Path, r.Header.Get("apns-topic")
		gotAuth, gotPushType = r.Header.Get("authorization"), r.Header.Get("apns-push-type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	})

	err := c.Push(context.Background(),
		lineupapi.PushDevice{Token: "devicetoken", Environment: "production", BundleID: "dev.rosterbot.app"},
		Payload{Title: "Fantrax Lineup", Body: "3 changes applied", NotificationID: "notif-1"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if gotPath != "/3/device/devicetoken" {
		t.Errorf("path = %q", gotPath)
	}
	// apns-topic IS the bundle id, and it must come from the device record --
	// the debug and release builds have different ones.
	if gotTopic != "dev.rosterbot.app" {
		t.Errorf("apns-topic = %q, want the device's bundle_id", gotTopic)
	}
	if gotPushType != "alert" {
		t.Errorf("apns-push-type = %q", gotPushType)
	}
	if !strings.HasPrefix(gotAuth, "bearer ") {
		t.Errorf("authorization = %q", gotAuth)
	}

	aps, _ := body["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "Fantrax Lineup" || alert["body"] != "3 changes applied" {
		t.Errorf("alert = %+v", alert)
	}
	// The feed id is what makes tap-to-open work; without it the tap has no
	// destination and the notification is a dead end.
	if body["notification_id"] != "notif-1" {
		t.Errorf("notification_id = %v, want notif-1", body["notification_id"])
	}
}

func TestPushRoutesSandboxTokensToTheSandboxHost(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c := New(NewTokenSource(key, "K", "T"), http.DefaultClient)

	if got := c.host(lineupapi.PushDevice{Environment: "sandbox"}); got != sandboxHost {
		t.Errorf("sandbox device routed to %q", got)
	}
	if got := c.host(lineupapi.PushDevice{Environment: "production"}); got != productionHost {
		t.Errorf("production device routed to %q", got)
	}
}

func TestPushReportsGoneForDeadTokens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"410 unregistered", http.StatusGone, `{"reason":"Unregistered"}`},
		{"400 bad device token", http.StatusBadRequest, `{"reason":"BadDeviceToken"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			err := c.Push(context.Background(),
				lineupapi.PushDevice{Token: "t", Environment: "production", BundleID: "b"}, Payload{})
			if !errors.Is(err, ErrDeviceGone) {
				t.Fatalf("want ErrDeviceGone, got %v", err)
			}
		})
	}
}

func TestPushDoesNotReportGoneForOtherFailures(t *testing.T) {
	// A 500 or a throttle is transient. Treating it as ErrDeviceGone would
	// make the caller delete a live device over a temporary Apple outage.
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"reason":"InternalServerError"}`)
		})
		err := c.Push(context.Background(),
			lineupapi.PushDevice{Token: "t", Environment: "production", BundleID: "b"}, Payload{})
		if err == nil {
			t.Fatalf("status %d: want an error", status)
		}
		if errors.Is(err, ErrDeviceGone) {
			t.Fatalf("status %d must not be treated as a dead token", status)
		}
	}
}

func TestBadRequestOtherThanBadDeviceTokenIsNotGone(t *testing.T) {
	// A 400 for a malformed payload is our bug, not a dead device. Deleting
	// the device would hide the real defect and lose a live registration.
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"reason":"PayloadTooLarge"}`)
	})
	err := c.Push(context.Background(),
		lineupapi.PushDevice{Token: "t", Environment: "production", BundleID: "b"}, Payload{})
	if errors.Is(err, ErrDeviceGone) {
		t.Fatal("PayloadTooLarge must not delete the device")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/apns/ -run TestPush -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

Create `internal/apns/client.go`:

```go
package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

const (
	productionHost = "https://api.push.apple.com"
	sandboxHost    = "https://api.sandbox.push.apple.com"
)

// ErrDeviceGone means APNs will never accept this token again and the caller
// should delete the device record. It is returned ONLY for 410/Unregistered
// and 400/BadDeviceToken -- never for transient failures, because deleting a
// live device over an Apple outage is unrecoverable from our side.
var ErrDeviceGone = errors.New("apns: device token is no longer valid")

// Payload is the app-facing content of one notification. Body is a summary,
// not the full message: NotificationID points at the activity-feed record that
// holds the complete text, which is what the app opens on tap.
type Payload struct {
	Title          string
	Body           string
	NotificationID string
}

type Client struct {
	tokens *TokenSource
	http   *http.Client

	// hostOverride redirects every request at a test server. Empty in
	// production, where host() picks by the device's environment.
	hostOverride string
}

func New(tokens *TokenSource, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{tokens: tokens, http: httpClient}
}

func (c *Client) host(d lineupapi.PushDevice) string {
	if c.hostOverride != "" {
		return c.hostOverride
	}
	if d.Environment == "sandbox" {
		return sandboxHost
	}
	return productionHost
}

func (c *Client) Push(ctx context.Context, d lineupapi.PushDevice, p Payload) error {
	body, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{"title": p.Title, "body": p.Body},
			"sound": "default",
		},
		"notification_id": p.NotificationID,
	})
	if err != nil {
		return fmt.Errorf("apns: marshal payload: %w", err)
	}

	tok, err := c.tokens.Token(time.Now())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.host(d)+"/3/device/"+d.Token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// apns-topic is the bundle id, and it comes from the DEVICE, not from a
	// constant: debug and release builds register different bundle ids.
	req.Header.Set("apns-topic", d.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apns: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var apnsErr struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &apnsErr)

	if resp.StatusCode == http.StatusGone || apnsErr.Reason == "Unregistered" ||
		(resp.StatusCode == http.StatusBadRequest && apnsErr.Reason == "BadDeviceToken") {
		return fmt.Errorf("apns: %s: %w", apnsErr.Reason, ErrDeviceGone)
	}
	return fmt.Errorf("apns: status %d: %s", resp.StatusCode, apnsErr.Reason)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/apns/ -v`
Expected: PASS, 8 tests (3 from Task 4 plus 5 here, counting subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/apns/client.go internal/apns/client_test.go
git commit -m "feat(apns): sender with sandbox/production routing and dead-token detection"
```

---

### Task 6: The `notify` dispatcher

**Files:**
- Create: `internal/notify/dispatch.go`
- Create: `internal/notify/dispatch_test.go`
- Modify: `internal/notify/pushover.go` (documentation only — see Step 5)

**Interfaces:**
- Consumes: `lineupapi.Change` (existing).
- Produces: `notify.Event`; `notify.FeedSink` interface with `Write(ctx, Event) (string, error)`; `notify.Sink` interface with `Name() string` and `Deliver(ctx, Event, feedID string) error`; `notify.Dispatcher` with `Send(ctx, Event) error`; package globals `notify.Default *Dispatcher` and `notify.Send(ctx, Event) error`. Task 7 wires and calls these.

- [ ] **Step 1: Write the failing test**

Create `internal/notify/dispatch_test.go`:

```go
package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/notify"
)

type fakeFeed struct {
	written []notify.Event
	id      string
	err     error
}

func (f *fakeFeed) Write(_ context.Context, e notify.Event) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.written = append(f.written, e)
	return f.id, nil
}

type fakeSink struct {
	name    string
	gotID   string
	gotEvt  notify.Event
	calls   int
	err     error
}

func (s *fakeSink) Name() string { return s.name }
func (s *fakeSink) Deliver(_ context.Context, e notify.Event, feedID string) error {
	s.calls++
	s.gotEvt, s.gotID = e, feedID
	return s.err
}

func TestFeedRecordSurvivesADeliveryFailure(t *testing.T) {
	// THE central regression. Before this design the feed write happened
	// INSIDE SendPushover, so it was a side effect of a delivery attempt. If
	// that ordering ever comes back, events vanish from the activity feed
	// whenever APNs has a bad day -- and nothing reports it, because the
	// symptom is a record nobody knows to look for.
	feed := &fakeFeed{id: "notif-1"}
	dead := &fakeSink{name: "apns", err: errors.New("apns exploded")}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{dead}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup", Title: "t", Message: "m"}); err != nil {
		t.Fatalf("a delivery failure must not fail Send: %v", err)
	}
	if len(feed.written) != 1 {
		t.Fatalf("feed record must be written regardless of delivery, got %d", len(feed.written))
	}
}

func TestSendFailsOnlyWhenTheFeedWriteFails(t *testing.T) {
	feed := &fakeFeed{err: errors.New("s3 down")}
	sink := &fakeSink{name: "apns"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{sink}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup"}); err == nil {
		t.Fatal("a failed feed write is the one durable obligation and must be returned")
	}
	if sink.calls != 0 {
		t.Error("no sink may be called once the feed write has failed: the payload has no id to carry")
	}
}

func TestEverySinkReceivesTheFeedID(t *testing.T) {
	feed := &fakeFeed{id: "notif-42"}
	a := &fakeSink{name: "apns"}
	b := &fakeSink{name: "pushover"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{a, b}}

	evt := notify.Event{Kind: "waivers", Title: "Waivers", Message: "2 claims"}
	if err := d.Send(context.Background(), evt); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, s := range []*fakeSink{a, b} {
		if s.calls != 1 {
			t.Errorf("%s called %d times, want 1", s.name, s.calls)
		}
		if s.gotID != "notif-42" {
			t.Errorf("%s got feed id %q, want notif-42", s.name, s.gotID)
		}
		if s.gotEvt.Kind != "waivers" {
			t.Errorf("%s got kind %q", s.name, s.gotEvt.Kind)
		}
	}
}

func TestOneSinkFailureDoesNotSuppressTheOthers(t *testing.T) {
	feed := &fakeFeed{id: "n"}
	broken := &fakeSink{name: "apns", err: errors.New("nope")}
	healthy := &fakeSink{name: "pushover"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{broken, healthy}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if healthy.calls != 1 {
		t.Error("a sink listed after a failing one must still be delivered to")
	}
}

func TestSendWithNoDispatcherConfiguredIsANoOp(t *testing.T) {
	// Local runs and tests have no feed configured. Emitting an event must
	// not panic there; it simply goes nowhere.
	notify.Default = nil
	if err := notify.Send(context.Background(), notify.Event{Kind: "lineup"}); err != nil {
		t.Fatalf("want a silent no-op, got %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/notify/ -v`
Expected: FAIL — `undefined: notify.Dispatcher`.

- [ ] **Step 3: Implement**

Create `internal/notify/dispatch.go`:

```go
package notify

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Event is one thing worth telling a manager about: a lineup applied, waiver
// claims filed, a trade graded.
//
// Kind is stated by the call site rather than inferred from Title. The old
// path guessed it with lineupapi.KindFromTitle's substring matching, which
// only worked because the titles happened not to collide.
type Event struct {
	Kind    string // lineup|waivers|claims|transactions|prospects|gs-check|alert
	Title   string
	Message string
	Changes []lineupapi.Change // optional; lineup only
}

// FeedSink writes the durable activity-feed record and returns its id.
//
// It is separate from Sink and runs first because its id is an INPUT to every
// delivery: the push payload carries it so tapping the notification can open
// that item. A feed write that happened afterwards could not supply it.
type FeedSink interface {
	Write(ctx context.Context, e Event) (id string, err error)
}

// Sink is one best-effort delivery channel.
type Sink interface {
	Name() string
	Deliver(ctx context.Context, e Event, feedID string) error
}

// Dispatcher fans an event out: durable record first, then delivery.
type Dispatcher struct {
	Feed  FeedSink
	Sinks []Sink
}

// Send writes the feed record and then delivers to every sink.
//
// It returns an error ONLY when the feed write fails, which is the single
// durable obligation. A sink's failure is logged and swallowed, matching the
// best-effort contract the old Recorder hook documented -- a manager's lineup
// run must not fail because Apple had a bad minute.
func (d *Dispatcher) Send(ctx context.Context, e Event) error {
	if d == nil || d.Feed == nil {
		return nil
	}
	id, err := d.Feed.Write(ctx, e)
	if err != nil {
		return fmt.Errorf("notify: write feed record: %w", err)
	}
	for _, s := range d.Sinks {
		if err := s.Deliver(ctx, e, id); err != nil {
			fmt.Fprintf(os.Stderr, "warning: notify sink %s: %v\n", s.Name(), err)
		}
	}
	return nil
}

// Default is the process-wide dispatcher, set once by cmd.initShared. It
// mirrors the existing Recorder/cache.Notify/OutputRecorder globals rather
// than threading a dispatcher through every call chain. Nil outside a
// configured deployment (local runs, tests), where Send is a no-op.
var Default *Dispatcher

// Send emits an event through the process-wide dispatcher.
func Send(ctx context.Context, e Event) error { return Default.Send(ctx, e) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/notify/ -v`
Expected: PASS, 5 tests.

- [ ] **Step 5: Document why `SendPushover` survives**

Replace the `Recorder` doc comment in `internal/notify/pushover.go` — the hook
is being retired in Task 7, but the function is not:

```go
// Recorder is retained ONLY for the four operator sends that still call
// SendPushover directly (see the plan's Global Constraints). Fantasy events no
// longer pass through here: they go through Dispatcher, which writes the feed
// record itself, first, so that the record's id can be carried in the push
// payload. Do not reintroduce a feed write inside a delivery function.
var Recorder func(title, message string)

// SendPushover sends a push notification via the Pushover API.
//
// Still exported and still used, by design: the personal operator channel
// (connect blocked, cache stale fallback, GS limit fetch failure, projection
// status) reports on the bot's own health and deliberately does not depend on
// our APNs path -- an alert about the bot being unable to reach the network
// must not travel over a channel that needs the network in the same way.
func SendPushover(userKey, apiToken, title, message string) error {
```

- [ ] **Step 6: Commit**

```bash
git add internal/notify/
git commit -m "feat(notify): event dispatcher with feed-first ordering"
```

---

### Task 7: Wire the dispatcher and convert the nine call sites

**Files:**
- Create: `internal/notify/sinks.go` (the APNs and Pushover `Sink` implementations, plus the feed sink)
- Create: `internal/notify/sinks_test.go`
- Modify: `cmd/notifications.go` (replace `installNotificationRecorder` with dispatcher construction)
- Modify: `cmd/root.go` (call the new installer from `initShared`)
- Modify: the nine call sites listed in Global Constraints

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces: `notify.FeedWriterSink`, `notify.APNsSink`, `notify.PushoverSink`; `cmd.installNotifyDispatcher()`.

- [ ] **Step 1: Write the failing test for the APNs sink**

Create `internal/notify/sinks_test.go`:

```go
package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

type stubPusher struct {
	sent []lineupapi.PushDevice
	err  error
}

func (s *stubPusher) Push(_ context.Context, d lineupapi.PushDevice, _ apns.Payload) error {
	s.sent = append(s.sent, d)
	return s.err
}

type memDevices struct {
	byUser  map[lineupapi.UserID][]lineupapi.PushDevice
	deleted []string
}

func (m *memDevices) PutPushDevice(context.Context, lineupapi.UserID, lineupapi.PushDevice) (lineupapi.PushDevice, error) {
	panic("not used")
}
func (m *memDevices) PushDevices(_ context.Context, uid lineupapi.UserID) ([]lineupapi.PushDevice, error) {
	return m.byUser[uid], nil
}
func (m *memDevices) DeletePushDevice(_ context.Context, _ lineupapi.UserID, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

func TestAPNsSinkFansOutToEveryDeviceOfTheTenant(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "d1", Token: "a"}, {ID: "d2", Token: "b"}},
	}}
	pusher := &stubPusher{}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	if err := sink.Deliver(context.Background(), notify.Event{Kind: "lineup", Title: "T", Message: "M"}, "notif-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(pusher.sent) != 2 {
		t.Fatalf("want a push per device, got %d", len(pusher.sent))
	}
}

func TestAPNsSinkPrunesGoneDevices(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "dead", Token: "a"}},
	}}
	pusher := &stubPusher{err: apns.ErrDeviceGone}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	_ = sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n")

	if len(devices.deleted) != 1 || devices.deleted[0] != "dead" {
		t.Fatalf("a device APNs reports gone must be deleted, deleted=%v", devices.deleted)
	}
}

func TestAPNsSinkKeepsDevicesOnTransientErrors(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "live", Token: "a"}},
	}}
	pusher := &stubPusher{err: errors.New("503 service unavailable")}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	_ = sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n")

	if len(devices.deleted) != 0 {
		t.Fatalf("a transient failure must never delete a device, deleted=%v", devices.deleted)
	}
}

func TestAPNsSinkWithNoDevicesIsASilentSuccess(t *testing.T) {
	sink := notify.NewAPNsSink(&stubPusher{}, &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{}}, "u1")
	if err := sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n"); err != nil {
		t.Fatalf("a tenant with no registered devices is normal, not an error: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/notify/ -run TestAPNsSink -v`
Expected: FAIL — `undefined: notify.NewAPNsSink`.

- [ ] **Step 3: Implement the sinks**

Create `internal/notify/sinks.go`:

```go
package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// --- Feed sink -------------------------------------------------------------

// FeedWriterSink persists the durable activity-feed record. It replaces the
// old Recorder hook, which fired inside SendPushover.
type FeedWriterSink struct {
	Writer lineupapi.NotificationWriter
	UserID string
	RunID  string
	Now    func() time.Time // nil means time.Now
}

func (f *FeedWriterSink) Write(ctx context.Context, e Event) (string, error) {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	t := now().UTC()
	id := fmt.Sprintf("%d", t.UnixNano())

	return id, f.Writer.PutNotification(ctx, lineupapi.Notification{
		ID:        id,
		Kind:      e.Kind,
		Status:    lineupapi.ClassifyStatus(e.Kind, e.Title, e.Message),
		Title:     e.Title,
		Message:   e.Message,
		CreatedAt: t.Format(time.RFC3339),
		RunID:     f.RunID,
		UserID:    f.UserID,
		Changes:   e.Changes,
	})
}

// --- APNs sink -------------------------------------------------------------

// Pusher is the slice of *apns.Client this sink needs, named here so tests can
// substitute it without an HTTP server.
type Pusher interface {
	Push(ctx context.Context, d lineupapi.PushDevice, p apns.Payload) error
}

type APNsSink struct {
	pusher  Pusher
	devices lineupapi.PushDeviceStore
	uid     lineupapi.UserID
}

func NewAPNsSink(p Pusher, devices lineupapi.PushDeviceStore, uid lineupapi.UserID) *APNsSink {
	return &APNsSink{pusher: p, devices: devices, uid: uid}
}

func (a *APNsSink) Name() string { return "apns" }

func (a *APNsSink) Deliver(ctx context.Context, e Event, feedID string) error {
	devices, err := a.devices.PushDevices(ctx, a.uid)
	if err != nil {
		return fmt.Errorf("apns sink: list devices: %w", err)
	}
	// A tenant with no registered devices is the normal state before anyone
	// installs the app. Not an error.
	for _, d := range devices {
		err := a.pusher.Push(ctx, d, apns.Payload{
			Title:          e.Title,
			Body:           summarize(e.Message),
			NotificationID: feedID,
		})
		switch {
		case err == nil:
		case errors.Is(err, apns.ErrDeviceGone):
			// Only this sentinel prunes. A transient failure must never
			// delete a live registration -- see apns.ErrDeviceGone.
			if derr := a.devices.DeletePushDevice(ctx, a.uid, d.ID); derr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not prune dead device %s: %v\n", d.ID, derr)
			}
		default:
			fmt.Fprintf(os.Stderr, "warning: apns push to %s: %v\n", d.ID, err)
		}
	}
	return nil
}

// maxBodyRunes bounds the notification body. The payload carries a SUMMARY --
// the full message lives in the feed record the notification opens -- and a
// lock screen shows roughly four lines regardless.
const maxBodyRunes = 178

func summarize(message string) string {
	r := []rune(message)
	if len(r) <= maxBodyRunes {
		return message
	}
	return string(r[:maxBodyRunes-1]) + "…"
}

// --- Pushover sink ---------------------------------------------------------

// PushoverSink carries fantasy events during the cutover window only. It is
// installed when PUSHOVER_FANTASY_DUAL_SEND is set; removing that variable
// completes the migration with no deploy. See the spec's Cutover section.
type PushoverSink struct {
	UserKey, APIToken string
}

func (p *PushoverSink) Name() string { return "pushover" }

func (p *PushoverSink) Deliver(_ context.Context, e Event, _ string) error {
	return SendPushover(p.UserKey, p.APIToken, e.Title, e.Message)
}
```

- [ ] **Step 4: Run the sink tests to verify they pass**

Run: `go test ./internal/notify/ -v`
Expected: PASS, 9 tests.

- [ ] **Step 5: Commit the sinks before touching call sites**

```bash
git add internal/notify/sinks.go internal/notify/sinks_test.go
git commit -m "feat(notify): feed, APNs and Pushover sinks"
```

- [ ] **Step 6: Replace the recorder installer with dispatcher construction**

Rewrite `cmd/notifications.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// installNotifyDispatcher builds the process-wide dispatcher.
//
// The feed sink is required; APNs and Pushover are added when their config is
// present. A deployment with no APNs key still records every event in the
// activity feed, which is the behaviour every local run and test relies on.
func installNotifyDispatcher() {
	w, err := statestore.FromEnv().Notifications()
	if err != nil {
		return // no durable feed configured; Send stays a no-op
	}
	uid := statestore.Tenant()

	d := &notify.Dispatcher{
		Feed: &notify.FeedWriterSink{
			Writer: w,
			UserID: uid,
			RunID:  os.Getenv("RUN_ID"), // set by entrypoint.sh; links feed -> run
		},
	}

	if sink := apnsSink(lineupapi.UserID(uid)); sink != nil {
		d.Sinks = append(d.Sinks, sink)
	}
	// Cutover window only: see the spec's Cutover section. Unsetting this
	// variable stops fantasy events reaching Pushover, with no code change and
	// without touching PUSHOVER_USER_KEY, which the operator channel still needs.
	if os.Getenv("PUSHOVER_FANTASY_DUAL_SEND") != "" {
		if u, tkn := os.Getenv("PUSHOVER_USER_KEY"), os.Getenv("PUSHOVER_API_TOKEN"); u != "" && tkn != "" {
			d.Sinks = append(d.Sinks, &notify.PushoverSink{UserKey: u, APIToken: tkn})
		}
	}

	notify.Default = d
}

// apnsSink returns nil when APNs is not configured, which is the normal state
// locally and in every test.
func apnsSink(uid lineupapi.UserID) notify.Sink {
	keyPEM, keyID := os.Getenv("APNS_AUTH_KEY"), os.Getenv("APNS_KEY_ID")
	teamID, table := os.Getenv("APNS_TEAM_ID"), os.Getenv("IDENTITY_TABLE")
	if keyPEM == "" || keyID == "" || teamID == "" || table == "" || uid == "" {
		return nil
	}
	key, err := apns.ParseAuthKey([]byte(keyPEM))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: APNS_AUTH_KEY unusable, push disabled: %v\n", err)
		return nil
	}
	devices, err := ddbPushDeviceStore(table) // see Step 7
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: device store unavailable, push disabled: %v\n", err)
		return nil
	}
	client := apns.New(apns.NewTokenSource(key, keyID, teamID), &http.Client{Timeout: 30 * time.Second})
	return notify.NewAPNsSink(client, devices, uid)
}
```

- [ ] **Step 7: Add the store constructor**

Add `ddbPushDeviceStore` to `cmd/notifications.go`, building a
`*ddbuser.Store` the same way `cmd/connect.go` does around line 53 (read it
first and match its construction exactly):

```go
func ddbPushDeviceStore(table string) (lineupapi.PushDeviceStore, error) {
	// Not `return ddbuser.New(...)` directly: Go has no implicit multi-value
	// conversion, so returning (*Store, error) where (PushDeviceStore, error)
	// is declared does not compile.
	st, err := ddbuser.New(context.Background(), table)
	if err != nil {
		return nil, err
	}
	return st, nil
}
```

- [ ] **Step 8: Call it from `initShared`**

In `cmd/root.go`, replace the `installNotificationRecorder()` call (around line
148) with `installNotifyDispatcher()`, and update the comment above it:

```go
	// Fan every fantasy event out: durable activity-feed record first (its id
	// is what a push payload carries), then APNs, then Pushover during the
	// cutover window. The four operator sends below and elsewhere still call
	// SendPushover directly and deliberately bypass this.
	installNotifyDispatcher()
```

**Leave the `cache.Notify` block above it exactly as it is.** It is one of the
four operator sends and must keep using Pushover.

- [ ] **Step 9: Convert the nine call sites**

For each, replace the `SendPushover` call with a `notify.Send`. The `Kind`
values are fixed by the app's `NotificationKind` enum — using a value outside
this set makes the app decode it as `.unknown` and render a generic bell icon:

| File | Kind | Title (unchanged) |
| --- | --- | --- |
| `internal/lineuprun/lineuprun.go` (Fantrax Lineup) | `lineup` | `Fantrax Lineup` |
| `internal/lineuprun/lineuprun.go` (IL Start Alert) | `lineup` | `IL Start Alert` |
| `internal/lineuprun/lineuprun.go` (decision alert) | `alert` | `dec.Alert.Title` |
| `internal/claims/run.go` | `claims` | `Waiver Claims` |
| `internal/waivers/run.go` | `waivers` | `pushoverTitle` |
| `internal/transactions/transactions.go` | `transactions` | `Trade Alert` |
| `internal/prospects/run.go` | `prospects` | `RosterBot: Prospect Alerts` |
| `cmd/football_trades.go` | `transactions` | unchanged |
| `internal/gscheck/gscheck.go` (line 235) | `gs-check` | `Fantrax GS Alert` |

Example, for the lineup send:

```go
// before
if err := notify.SendPushover(userKey, apiToken, "Fantrax Lineup", message); err != nil {

// after
if err := notify.Send(ctx, notify.Event{
	Kind:    "lineup",
	Title:   "Fantrax Lineup",
	Message: message,
}); err != nil {
```

Where a `context.Context` is not already in scope, thread the caller's in
rather than reaching for `context.Background()`. Where the surrounding code
guarded on `userKey != "" && apiToken != ""`, delete that guard: an unconfigured
dispatcher is already a no-op, and keeping the guard would tie fantasy delivery
to a Pushover variable the operator channel owns.

**`gs-check` (line 235) is the exception:** it must reach BOTH channels
permanently (D3). Emit the event *and* keep its existing group-key
`SendPushover` call:

```go
if err := notify.Send(ctx, notify.Event{Kind: "gs-check", Title: "Fantrax GS Alert", Message: shortSummary}); err != nil {
	return fmt.Errorf("record gs alert: %w", err)
}
// The league broadcast reaches managers who are not app users and therefore
// cannot be ported to APNs -- see D3. Retained deliberately, permanently.
if err := notify.SendPushover(cfg.PushoverGroupKey, cfg.PushoverAPIToken, "Fantrax GS Alert", shortSummary); err != nil {
	return fmt.Errorf("send pushover: %w", err)
}
```

- [ ] **Step 10: Verify no fantasy site still calls SendPushover**

Run:

```bash
grep -rn 'notify\.SendPushover(' --include='*.go' cmd/ internal/ | grep -v '_test' | grep -v 'internal/notify/'
```

Expected: exactly **five** lines — `cmd/connect_feed.go`, `cmd/root.go`,
`cmd/shadow.go`, `internal/gscheck/gscheck.go:122`, and the retained
`gscheck.go` group broadcast. Any other hit is an unconverted fantasy site.

- [ ] **Step 11: Run the full suite**

Run: `make test`
Expected: PASS. If `KindFromTitle` tests fail, they are asserting the old
inference path; leave `KindFromTitle` itself in place (historical records still
need it) and update only tests that assumed new sends route through it.

- [ ] **Step 12: Commit**

```bash
git add -A
git commit -m "feat(notify): route the nine fantasy events through the dispatcher"
```

---

### Task 8: Infrastructure — SSM secrets and task definition

**Files:**
- Modify: `infra/infra.go` (task definition secrets, around lines 343–372)

**Interfaces:**
- Consumes: the env var names read in Task 7 (`APNS_AUTH_KEY`, `APNS_KEY_ID`, `APNS_TEAM_ID`, `PUSHOVER_FANTASY_DUAL_SEND`).
- Produces: a deployed task definition that can send pushes.

- [ ] **Step 1: Confirm the manual prerequisites are done**

These cannot be automated and the deploy is useless without them (spec,
"Prerequisites only a human can do"):

1. An APNs auth key exists in the Apple Developer portal (Keys → APNs), the
   `.p8` has been downloaded (Apple serves it exactly once), and the Key ID is
   recorded.
2. `/rosterbot/APNS_AUTH_KEY` and `/rosterbot/APNS_KEY_ID` exist as SSM
   parameters in `us-west-1`, beside the existing `/rosterbot/*` values.
3. Push Notifications is enabled on **both** App IDs (`dev.rosterbot.app` and
   `dev.rosterbot.app.debug`) and the provisioning profiles are regenerated.

Verify 2 with:

```bash
aws ssm get-parameter --name /rosterbot/APNS_KEY_ID --region us-west-1 --query 'Parameter.Name'
```

Do **not** print `APNS_AUTH_KEY`'s value.

- [ ] **Step 2: Add the secrets to the task definition**

In `infra/infra.go`, in the `Secrets` map on the bot container (around line
343, beside `PUSHOVER_*`):

```go
			"APNS_AUTH_KEY": secret("APNS_AUTH_KEY"),
			"APNS_KEY_ID":   secret("APNS_KEY_ID"),
```

`APNS_TEAM_ID` is the Apple team id `8KBU54NP6U` — not a secret, and stable —
so it goes in the container's `Environment` map, not `Secrets`. Note the
warning already in this file around line 307: a name must appear in exactly one
of `Environment` or `Secrets`, never both, and `cdk synth` does not catch the
mistake.

- [ ] **Step 3: Grant the task role read access**

Extend the existing SSM read grant for the task role (near line 266, which
already covers the `/rosterbot/*` parameters). If it is a wildcard over
`/rosterbot/*`, nothing to do — confirm by reading it rather than assuming.

- [ ] **Step 4: Add the cutover flag**

Add to the same `Environment` map, so the first deploy runs dual-send:

```go
			// Cutover window: fantasy events go to BOTH Pushover and APNs.
			// Delete this entry (and redeploy) to complete the migration.
			// Deliberately NOT keyed off PUSHOVER_USER_KEY, which the operator
			// channel still reads -- see the spec's Cutover section.
			"PUSHOVER_FANTASY_DUAL_SEND": jsii.String("1"),
```

- [ ] **Step 5: Synthesize and diff**

Run:

```bash
cd infra && cdk synth >/dev/null && cdk diff
```

Expected: the diff shows two new task-definition secrets and two new
environment variables, and **no change to the ops-alert Lambda**. If the
Lambda appears in the diff, something in Task 7 touched the operator path —
stop and re-read Global Constraints.

- [ ] **Step 6: Commit**

```bash
git add infra/infra.go
git commit -m "feat(infra): APNs credentials and cutover flag on the bot task"
```

- [ ] **Step 7: Deploy and verify end to end**

Deploy, then register a device from the iOS client (its plan's Task 3) and
trigger a real run. Confirm: the activity feed shows the event, the device
receives a push, and the Pushover message still arrives (dual-send). Then
confirm the operator channel is untouched by checking that a stale-cache
fallback still pages Pushover.

Only after several days of clean delivery, remove `PUSHOVER_FANTASY_DUAL_SEND`
and redeploy.

---

## Verification

Run before opening a PR:

```bash
make test && go vet ./... && cd infra && cdk synth >/dev/null
```

The end-to-end path cannot be verified by tests alone — APNs is a live external
service and the pruning branch only fires against real dead tokens. Step 7 of
Task 8 is the real verification, and it needs the iOS client to exist.
