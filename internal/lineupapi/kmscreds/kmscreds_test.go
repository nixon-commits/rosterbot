package kmscreds_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/kmscreds"
)

// fakeKMS models the ONE property that matters: a ciphertext carries the
// encryption context it was sealed with, and a Decrypt whose context differs is
// refused.
//
// A double that ignored the context would pass every test below except the
// cross-tenant one, and that is the test the context exists for. This is the
// s3blobtest lesson again — a fake that rubber-stamps a precondition reports
// success for precisely the case the precondition rejects.
type fakeKMS struct{ decrypts int }

type blob struct {
	Ctx  map[string]string `json:"ctx"`
	Data []byte            `json:"data"`
}

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	b, err := json.Marshal(blob{Ctx: in.EncryptionContext, Data: in.Plaintext})
	if err != nil {
		return nil, err
	}
	return &kms.EncryptOutput{CiphertextBlob: b}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.decrypts++
	var b blob
	if err := json.Unmarshal(in.CiphertextBlob, &b); err != nil {
		return nil, err
	}
	if len(b.Ctx) != len(in.EncryptionContext) {
		return nil, fmt.Errorf("InvalidCiphertextException: encryption context mismatch")
	}
	for k, v := range b.Ctx {
		if in.EncryptionContext[k] != v {
			return nil, fmt.Errorf("InvalidCiphertextException: encryption context mismatch")
		}
	}
	return &kms.DecryptOutput{Plaintext: b.Data}, nil
}

func TestRoundTrip(t *testing.T) {
	api := &fakeKMS{}
	s := kmscreds.NewSealerWithAPI(api, "key-1")
	o := kmscreds.NewOpenerWithAPI(api)
	ctx := context.Background()

	want := []byte(`{"username":"jon","password":"hunter2"}`)
	ct, err := s.Seal(ctx, "alice", want)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(ct, []byte("hunter2")) {
		t.Fatal("the ciphertext contains the plaintext; the fake is not modelling " +
			"encryption closely enough for the rest of these tests to mean anything")
	}
	got, err := o.Open(ctx, "alice", ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

// TestOneTenantCannotOpenAnothersCiphertext is the reason the encryption
// context exists.
//
// The connect task legitimately holds kms:Decrypt — it has to, it is the thing
// that uses credentials. Without a per-tenant context, that single permission
// would let a bug (or a compromised task) decrypt EVERY tenant's password, not
// just the one it is running for. The context makes KMS itself refuse, so the
// blast radius of holding Decrypt is one tenant rather than all of them.
func TestOneTenantCannotOpenAnothersCiphertext(t *testing.T) {
	api := &fakeKMS{}
	s := kmscreds.NewSealerWithAPI(api, "key-1")
	o := kmscreds.NewOpenerWithAPI(api)
	ctx := context.Background()

	ct, err := s.Seal(ctx, "alice", []byte("alice's password"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Open(ctx, "bob", ct); err == nil {
		t.Fatal("bob opened alice's ciphertext. The per-tenant encryption context " +
			"is what limits a single kms:Decrypt grant to one tenant instead of all " +
			"of them")
	}
	// And the owner still can, or the test above would pass for the wrong reason.
	if _, err := o.Open(ctx, "alice", ct); err != nil {
		t.Fatalf("alice cannot open her own ciphertext: %v", err)
	}
}

func TestSealRefusesAnEmptyTenant(t *testing.T) {
	s := kmscreds.NewSealerWithAPI(&fakeKMS{}, "key-1")
	if _, err := s.Seal(context.Background(), "", []byte("x")); err == nil {
		t.Fatal("sealed with no tenant. An empty encryption context binds the " +
			"ciphertext to nobody, so any tenant could open it")
	}
}

// TestSealerHasNoOpenMethod is a compile-time statement rather than a runtime
// one: it documents that the API Lambda, which holds only a Sealer, cannot read
// a credential back even by mistake.
//
// The split lives in three places and they must agree — the IAM in infra.go
// (Encrypt vs Decrypt), these two Go types, and the interfaces in
// lineupapi. This asserts the middle one; infra/identity_grants_test.go asserts
// the first.
func TestSealerHasNoOpenMethod(t *testing.T) {
	var s any = kmscreds.NewSealerWithAPI(&fakeKMS{}, "key-1")
	if _, ok := s.(lineupapi.Opener); ok {
		t.Fatal("the Sealer also satisfies Opener. The API Lambda holds a Sealer, so " +
			"this would hand the internet-facing component the ability to decrypt " +
			"tenants' Fantrax passwords — the exact property the split IAM exists for")
	}
	var o any = kmscreds.NewOpenerWithAPI(&fakeKMS{})
	if _, ok := o.(lineupapi.Sealer); ok {
		t.Error("the Opener also satisfies Sealer; keep the two halves distinct")
	}
}
