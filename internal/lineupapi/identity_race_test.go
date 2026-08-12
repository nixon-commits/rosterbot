package lineupapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

// gatedIdentityStore holds every caller at its first read until `want` of them
// have read, guaranteeing they are all working from the same version. Without
// it the race is real but rare, and a flaky test that passes by accident is
// worse than none: this makes the interleaving that produced rosterbot-crq.2
// happen on every run.
type gatedIdentityStore struct {
	inner   IdentityStore
	want    int32
	reads   int32
	once    sync.Once
	release chan struct{}
}

func newGatedIdentityStore(inner IdentityStore, want int32) *gatedIdentityStore {
	return &gatedIdentityStore{inner: inner, want: want, release: make(chan struct{})}
}

func (g *gatedIdentityStore) GetIdentity(ctx context.Context) (*Identity, bool, error) {
	id, ok, err := g.inner.GetIdentity(ctx)
	if atomic.AddInt32(&g.reads, 1) >= g.want {
		g.once.Do(func() { close(g.release) })
	}
	<-g.release // already-closed on every retry, so only the first round gates
	return id, ok, err
}

func (g *gatedIdentityStore) PutIdentity(ctx context.Context, id *Identity) error {
	return g.inner.PutIdentity(ctx, id)
}

// TestMutateIdentity_ConcurrentRegisterAndLoginBothSurvive is the regression
// test for rosterbot-crq.2. Before optimistic concurrency, a registration and
// a login that read the same record both wrote it back blind: the second write
// restored its own snapshot and erased whatever the first had added. The
// registration returned 200, the passkey was simply not there afterwards, and
// nothing anywhere logged a word about it.
//
// Both mutations must be observable in the final record. The store here is the
// real FileIdentityStore, not a double, so the compare-and-swap being tested is
// the one that ships.
func TestMutateIdentity_ConcurrentRegisterAndLoginBothSurvive(t *testing.T) {
	ctx := context.Background()
	store := NewFileIdentityStore(t.TempDir())

	if err := store.PutIdentity(ctx, &Identity{
		WebAuthnUserID: []byte("handle"),
		Credentials: []webauthn.Credential{
			{ID: []byte("device-A"), Authenticator: webauthn.Authenticator{SignCount: 1}},
		},
	}); err != nil {
		t.Fatalf("seed PutIdentity: %v", err)
	}

	cfg := Config{Identities: newGatedIdentityStore(store, 2)}

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() { // registration: enrol a second passkey
		defer wg.Done()
		_, err := cfg.mutateIdentity(ctx, func(cur *Identity) error {
			cur.Credentials = append(cur.Credentials, webauthn.Credential{ID: []byte("device-B")})
			return nil
		})
		errs <- err
	}()

	wg.Add(1)
	go func() { // login: bump device-A's sign counter
		defer wg.Done()
		_, err := cfg.mutateIdentity(ctx, func(cur *Identity) error {
			for i := range cur.Credentials {
				if string(cur.Credentials[i].ID) == "device-A" {
					cur.Credentials[i].Authenticator.SignCount = 42
				}
			}
			return nil
		})
		errs <- err
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("mutateIdentity: %v — the loser must retry and succeed, not fail", err)
		}
	}

	got, ok, err := store.GetIdentity(ctx)
	if err != nil || !ok {
		t.Fatalf("GetIdentity: ok=%v err=%v", ok, err)
	}

	var sawB bool
	var counterA uint32
	for _, c := range got.Credentials {
		switch string(c.ID) {
		case "device-B":
			sawB = true
		case "device-A":
			counterA = c.Authenticator.SignCount
		}
	}
	if !sawB {
		t.Error("device-B is missing — the registration was silently dropped by the concurrent login")
	}
	if counterA != 42 {
		t.Errorf("device-A SignCount = %d, want 42 — the login's counter update was silently dropped by the concurrent registration", counterA)
	}
	if len(got.Credentials) != 2 {
		t.Errorf("credentials = %+v, want exactly two — a retried mutation must not append twice", got.Credentials)
	}
}

// TestMutateIdentity_GivesUpAfterBoundedRetries pins the loop as bounded. An
// endlessly-conflicting store must surface a conflict, not hang the request.
func TestMutateIdentity_GivesUpAfterBoundedRetries(t *testing.T) {
	cfg := Config{Identities: alwaysConflicting{}}
	_, err := cfg.mutateIdentity(context.Background(), func(*Identity) error { return nil })
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("mutateIdentity against a permanently-conflicting store = %v, want ErrIdentityConflict", err)
	}
}

type alwaysConflicting struct{}

func (alwaysConflicting) GetIdentity(context.Context) (*Identity, bool, error) {
	return &Identity{WebAuthnUserID: []byte("h"), Version: "v"}, true, nil
}
func (alwaysConflicting) PutIdentity(context.Context, *Identity) error { return ErrIdentityConflict }

// TestMutateIdentity_ReportsMissingRecord: every mutation site establishes the
// record first, so absence here means it was deleted mid-request. Revocation
// turns this into a 404 rather than a 500.
func TestMutateIdentity_ReportsMissingRecord(t *testing.T) {
	cfg := Config{Identities: NewFileIdentityStore(t.TempDir())}
	_, err := cfg.mutateIdentity(context.Background(), func(*Identity) error { return nil })
	if !errors.Is(err, errNoIdentity) {
		t.Fatalf("mutateIdentity against an empty store = %v, want errNoIdentity", err)
	}
}

// TestLoadOrCreateIdentity_LosingTheCreateRaceAdoptsTheWinner: two concurrent
// register/begin requests each mint an independent random WebAuthnUserID.
// go-webauthn bakes the handle from begin into the ceremony session and
// requires it to match at finish, so the loser overwriting the winner would
// break the ceremony already running against the winner's handle. The loser
// must adopt, not retry.
func TestLoadOrCreateIdentity_LosingTheCreateRaceAdoptsTheWinner(t *testing.T) {
	ctx := context.Background()
	store := NewFileIdentityStore(t.TempDir())
	cfg := Config{Identities: newGatedIdentityStore(store, 2)}

	var wg sync.WaitGroup
	handles := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := cfg.loadOrCreateIdentity(ctx)
			if err != nil {
				handles <- "error: " + err.Error()
				return
			}
			handles <- string(id.WebAuthnUserID)
		}()
	}
	wg.Wait()
	close(handles)

	var got []string
	for h := range handles {
		got = append(got, h)
	}
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("handles = %q, want both callers to see one identical handle", got)
	}

	stored, ok, err := store.GetIdentity(ctx)
	if err != nil || !ok {
		t.Fatalf("GetIdentity: ok=%v err=%v", ok, err)
	}
	if string(stored.WebAuthnUserID) != got[0] {
		t.Fatalf("stored handle %x differs from the one handed to callers — the loser overwrote the winner", stored.WebAuthnUserID)
	}
}
