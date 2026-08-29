package alertmarker_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
)

// fakeStore is an in-memory Store whose failure modes are switchable, so every
// degrade branch of the discipline is reachable.
type fakeStore struct {
	m        map[string][]byte
	getErr   error
	pubErr   error
	puts     int
	lastNote []byte
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string][]byte{}} }

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	b, ok := s.m[key]
	return b, ok, nil
}

func (s *fakeStore) Publish(key string, data []byte) error {
	if s.pubErr != nil {
		return s.pubErr
	}
	s.puts++
	s.lastNote = append([]byte(nil), data...)
	s.m[key] = s.lastNote
	return nil
}

func TestSendOnce_SendsThenMarks_AndSecondCallSkips(t *testing.T) {
	st := newFakeStore()
	m := alertmarker.New(st)
	sends := 0
	send := func() error { sends++; return nil }

	sent, err := m.SendOnce(context.Background(), "k", []byte("note"), send)
	if err != nil || !sent {
		t.Fatalf("first SendOnce = (%v, %v), want (true, nil)", sent, err)
	}
	if string(st.m["k"]) != "note" {
		t.Fatalf("marker body = %q, want %q", st.m["k"], "note")
	}

	sent, err = m.SendOnce(context.Background(), "k", []byte("note"), send)
	if err != nil || sent {
		t.Fatalf("second SendOnce = (%v, %v), want (false, nil)", sent, err)
	}
	if sends != 1 {
		t.Fatalf("sends = %d, want 1 — the second call must be deduplicated", sends)
	}
}

func TestSendOnce_FailedSendIsNotMarked(t *testing.T) {
	st := newFakeStore()
	m := alertmarker.New(st)

	sent, err := m.SendOnce(context.Background(), "k", []byte("n"), func() error {
		return errors.New("pushover down")
	})
	if err == nil || sent {
		t.Fatalf("SendOnce = (%v, %v), want (false, error)", sent, err)
	}
	if _, found := st.m["k"]; found {
		t.Fatal("a failed send was marked — the next run would stay silent about an alert that never went out")
	}

	// The retry on the next run must go through.
	sent, err = m.SendOnce(context.Background(), "k", []byte("n"), func() error { return nil })
	if err != nil || !sent {
		t.Fatalf("retry SendOnce = (%v, %v), want (true, nil)", sent, err)
	}
}

func TestSendOnce_MarkerReadFailureDegradesToDuplicate(t *testing.T) {
	st := newFakeStore()
	st.m["k"] = []byte("already sent")
	st.getErr = errors.New("s3 unreachable")

	var logged []string
	m := alertmarker.New(st, alertmarker.WithLogf(func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	}))

	sends := 0
	sent, err := m.SendOnce(context.Background(), "k", []byte("n"), func() error { sends++; return nil })
	if err != nil || !sent || sends != 1 {
		t.Fatalf("SendOnce under read failure = (%v, %v, sends=%d), want a duplicate send", sent, err, sends)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "treating as unsent") {
		t.Fatalf("read failure not logged through WithLogf: %v", logged)
	}
}

func TestSend_MarkerWriteFailureIsLoggedNeverReturned(t *testing.T) {
	st := newFakeStore()
	st.pubErr = errors.New("s3 write refused")

	var logged []string
	m := alertmarker.New(st, alertmarker.WithLogf(func(f string, a ...any) {
		logged = append(logged, fmt.Sprintf(f, a...))
	}))

	if err := m.Send("k", []byte("n"), func() error { return nil }); err != nil {
		t.Fatalf("Send = %v; a mark failure must degrade to a duplicate, not fail the alert", err)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "alert again") {
		t.Fatalf("write failure not logged: %v", logged)
	}
}

func TestNilMarkerAndNilStore_AlertEveryRun(t *testing.T) {
	for name, m := range map[string]*alertmarker.Marker{
		"nil marker": nil,
		"nil store":  alertmarker.New(nil),
	} {
		sends := 0
		sent, err := m.SendOnce(context.Background(), "k", []byte("n"), func() error { sends++; return nil })
		if err != nil || !sent || sends != 1 {
			t.Fatalf("%s: SendOnce = (%v, %v, sends=%d), want send-every-run", name, sent, err, sends)
		}
		// And again — no dedup without a store.
		if sent, _ := m.SendOnce(context.Background(), "k", []byte("n"), func() error { sends++; return nil }); !sent || sends != 2 {
			t.Fatalf("%s: second call deduplicated with no store to record in", name)
		}
		if tok, found := m.Token(context.Background(), "k"); found || tok != "" {
			t.Fatalf("%s: Token = (%q, %v), want none", name, tok, found)
		}
		m.Record("k", []byte("n")) // must not panic
	}
}

func TestSendOnChange_SkipsSameEpisode_SendsNewOne(t *testing.T) {
	st := newFakeStore()
	m := alertmarker.New(st)
	sends := 0
	send := func() error { sends++; return nil }

	if sent, err := m.SendOnChange(context.Background(), "k", []byte("ep1"), send); err != nil || !sent {
		t.Fatalf("first episode = (%v, %v), want sent", sent, err)
	}
	if sent, _ := m.SendOnChange(context.Background(), "k", []byte("ep1"), send); sent {
		t.Fatal("same episode re-sent")
	}
	// The body may round-trip with whitespace; the comparison must trim.
	st.m["k"] = []byte("  ep1\n")
	if sent, _ := m.SendOnChange(context.Background(), "k", []byte("ep1"), send); sent {
		t.Fatal("whitespace around the recorded token defeated the comparison")
	}
	if sent, err := m.SendOnChange(context.Background(), "k", []byte("ep2"), send); err != nil || !sent {
		t.Fatalf("new episode = (%v, %v), want sent", sent, err)
	}
	if sends != 2 {
		t.Fatalf("sends = %d, want 2", sends)
	}
}

func TestToken_TrimsAndReportsExistence(t *testing.T) {
	st := newFakeStore()
	st.m["k"] = []byte("  tok\n")
	m := alertmarker.New(st)

	tok, found := m.Token(context.Background(), "k")
	if !found || tok != "tok" {
		t.Fatalf("Token = (%q, %v), want (tok, true)", tok, found)
	}
	// An empty body still exists — Token-comparing sites read it as cleared,
	// existence sites read it as sent; both need the two facts separately.
	st.m["e"] = nil
	if tok, found := m.Token(context.Background(), "e"); !found || tok != "" {
		t.Fatalf("empty marker Token = (%q, %v), want (\"\", true)", tok, found)
	}
}

func TestRecord_EmptyNoteClearsForTokenComparers(t *testing.T) {
	st := newFakeStore()
	m := alertmarker.New(st)
	m.Record("k", []byte("standing"))
	m.Record("k", nil)
	tok, found := m.Token(context.Background(), "k")
	if !found || tok != "" {
		t.Fatalf("after clearing, Token = (%q, %v), want (\"\", true)", tok, found)
	}
}

func TestWithKeyDisplay_SanitizesLoggedKeys(t *testing.T) {
	st := newFakeStore()
	st.getErr = errors.New("boom")
	var logged []string
	m := alertmarker.New(st,
		alertmarker.WithLogf(func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }),
		alertmarker.WithKeyDisplay(func(string) string { return "<redacted>" }),
	)
	m.Token(context.Background(), "raw\nkey")
	if len(logged) == 0 || !strings.Contains(logged[0], "<redacted>") || strings.Contains(logged[0], "raw") {
		t.Fatalf("key not routed through WithKeyDisplay: %v", logged)
	}
}
