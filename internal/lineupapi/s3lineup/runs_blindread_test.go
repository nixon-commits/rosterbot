package s3lineup

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// blindReads wraps the in-memory double so every GetObject fails while
// ListObjectsV2 keeps working — the shape of a transient S3 read burst, and of
// a caller's deadline firing after the listing has already returned.
type blindReads struct {
	*s3blobtest.Fake
	err error
}

func (b blindReads) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, b.err
}

// TestReadNewest_AnUnreadableLedgerIsNotAnEmptyOne.
//
// readNewest skips a record it cannot read, which is right: one malformed
// object must not blank the dashboard's run list. Applied to EVERY record it
// stops being a skip and becomes a false statement — the listing found N keys
// and the function returns (nil, nil), which is the same value a tenant who
// has genuinely never run produces.
//
// That value is not merely ambiguous, it is acted on. Config.runSummary
// (internal/lineupapi/tenants.go) returns nil only on an error, so a nil error
// gives it TenantRuns{Window: 0}; web/dashboard/tenants.js renders a zero
// window as a green "Never run" badge and attentionFrom finds no last_failure,
// so the row reports nobody needs to act. An operator scanning the admin page
// during an S3 wobble is told their testers have never run anything, on the
// one screen built to find what is failing. GET /v1/runs has the same problem
// one surface over: an empty list with a 200.
//
// So the rule this pins is that emptiness must be OBSERVED, not inferred from
// a failure to look.
func TestReadNewest_AnUnreadableLedgerIsNotAnEmptyOne(t *testing.T) {
	seed := s3blobtest.New()
	writable := &RunsStore{blob: seed.Blob("b", "runs/")}
	for _, id := range []string{"r-old", "r-new"} {
		if err := writable.PutRun(context.Background(), lineupapi.RunDetail{
			Run: lineupapi.Run{ID: id, Command: "grade", Status: "FAILED"},
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if got := len(seed.Keys()); got != 2 {
		t.Fatalf("seeded %d objects, want 2 — the fixture, not the code, is wrong", got)
	}

	readErr := errors.New("s3: ServiceUnavailable")
	blind := &RunsStore{blob: s3blob.NewWithClient(blindReads{Fake: seed, err: readErr}, "b", "runs/")}

	runs, err := blind.List(context.Background(), 25)
	if err == nil {
		t.Errorf("List returned a nil error over a ledger whose every record was unreadable: "+
			"%d records, indistinguishable from a tenant that has never run", len(runs))
	}
	if !errors.Is(err, readErr) {
		t.Errorf("error = %v, want it to wrap the underlying read failure so the cause is diagnosable", err)
	}
	if len(runs) != 0 {
		t.Errorf("returned %d records alongside the error; want none", len(runs))
	}
}

// TestReadNewest_ATrulyEmptyLedgerStaysEmpty is the other half, and it is what
// stops the fix above from turning every fresh tenant into an error. A tenant
// with no runs lists zero keys, so there is nothing that could have failed to
// read: that is an observed emptiness and must stay (nil, nil).
func TestReadNewest_ATrulyEmptyLedgerStaysEmpty(t *testing.T) {
	empty := s3blobtest.New()
	blind := &RunsStore{blob: s3blob.NewWithClient(
		blindReads{Fake: empty, err: errors.New("s3: ServiceUnavailable")}, "b", "runs/")}

	runs, err := blind.List(context.Background(), 25)
	if err != nil {
		t.Errorf("List errored on a ledger with no records: %v — a fresh tenant is not a failure", err)
	}
	if len(runs) != 0 {
		t.Errorf("returned %d records from an empty ledger", len(runs))
	}
}

// TestReadNewest_APartiallyReadableLedgerStillServesWhatItHas keeps the
// original skip rule honest. The fix must not escalate the case it was written
// for: one unreadable object among several still yields the rest, because a
// run list missing one row beats no run list at all.
func TestReadNewest_APartiallyReadableLedgerStillServesWhatItHas(t *testing.T) {
	seed := s3blobtest.New()
	s := &RunsStore{blob: seed.Blob("b", "runs/")}
	if err := s.PutRun(context.Background(), lineupapi.RunDetail{
		Run: lineupapi.Run{ID: "r-good", Command: "grade", Status: "SUCCESS"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A key that lists but holds bytes no decoder will accept.
	if err := seed.Blob("b", "runs/").PutJSON(context.Background(), "zzz-corrupt.json",
		[]byte("{not json")); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	runs, err := s.List(context.Background(), 25)
	if err != nil {
		t.Fatalf("List errored with a readable record present: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "r-good" {
		t.Errorf("got %d records %v, want just r-good — one corrupt object must not blank the list",
			len(runs), runs)
	}
}

// TestReadNewest_ACancelledContextIsReportedNotSkipped covers the budget-expiry
// path specifically. Config.fillRunSummaries bounds the admin page with a
// context deadline; when it fires mid-read the SDK returns ctx.Err() for every
// remaining object. Treating those as skips produced the same false "Never run"
// as above, on precisely the request that was slow enough to be worth reading
// carefully.
func TestReadNewest_ACancelledContextIsReportedNotSkipped(t *testing.T) {
	seed := s3blobtest.New()
	s := &RunsStore{blob: seed.Blob("b", "runs/")}
	if err := s.PutRun(context.Background(), lineupapi.RunDetail{
		Run: lineupapi.Run{ID: "r-1", Command: "grade", Status: "FAILED"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runs, err := s.List(ctx, 25)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled; a deadline that fired must not read as an empty ledger", err)
	}
	if len(runs) != 0 {
		t.Errorf("returned %d records after cancellation", len(runs))
	}
}
