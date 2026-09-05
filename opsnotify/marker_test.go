package main

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// fakeMarkers installs an in-memory marker store and restores the previous one.
func fakeMarkers(t *testing.T) *s3blobtest.Fake {
	t.Helper()
	fake := s3blobtest.New()
	prev := markers
	markers = &markerStore{blob: fake.Blob("state", markerPrefix)}
	t.Cleanup(func() { markers = prev })
	return fake
}

// errBlob fails every operation, standing in for an unwritable or unreachable
// marker prefix.
type errBlob struct{ readErr, writeErr error }

func (b errBlob) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, b.readErr
}
func (b errBlob) Put(context.Context, string, []byte) error { return b.writeErr }

func TestSendOnce_SendsTheFirstTimeAndStaysQuietAfter(t *testing.T) {
	got := capture(t)
	fakeMarkers(t)
	a := alert{key: "task/abc", note: "version=3", title: "T", body: "B"}

	for i := 0; i < 3; i++ {
		if err := sendOnce(context.Background(), markers, a); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends across 3 deliveries, want 1: %v", len(*got), *got)
	}
}

// A different alert must still get through — deduplication is per identity, not
// a global mute.
func TestSendOnce_DistinctKeysBothSend(t *testing.T) {
	got := capture(t)
	fakeMarkers(t)
	ctx := context.Background()

	if err := sendOnce(ctx, markers, alert{key: "task/a", title: "T", body: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := sendOnce(ctx, markers, alert{key: "task/b", title: "T", body: "B"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("got %d sends, want 2: %v", len(*got), *got)
	}
}

// The empty title is the existing "stay quiet" signal, and it must not burn a
// marker — otherwise the first None verdict for a task would suppress a real
// alert that a later delivery of the same task should still produce.
func TestSendOnce_QuietVerdictLeavesNoMarker(t *testing.T) {
	got := capture(t)
	fake := fakeMarkers(t)

	if err := sendOnce(context.Background(), markers, alert{key: "task/a", title: "", body: ""}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0", len(*got))
	}
	if len(fake.Keys()) != 0 {
		t.Errorf("wrote markers %v, want none", fake.Keys())
	}
}

// The critical ordering property: a failed send must not leave a marker behind,
// or the retry it asks for would be suppressed and the alert lost for good.
func TestSendOnce_FailedSendLeavesNoMarkerAndPropagates(t *testing.T) {
	prev := send
	sendErr := errors.New("pushover down")
	send = func(context.Context, string, string) error { return sendErr }
	t.Cleanup(func() { send = prev })

	fake := fakeMarkers(t)
	err := sendOnce(context.Background(), markers, alert{key: "task/a", title: "T", body: "B"})
	if !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want %v (async-invoke must retry)", err, sendErr)
	}
	if len(fake.Keys()) != 0 {
		t.Errorf("wrote markers %v after a failed send; the retry would be muted", fake.Keys())
	}
}

// A broken marker store must degrade to duplicate alerts, never to silence.
func TestSendOnce_MarkerStoreFailuresNeverSuppressAnAlert(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		got := capture(t)
		prev := markers
		markers = &markerStore{blob: errBlob{readErr: errors.New("s3 down")}}
		t.Cleanup(func() { markers = prev })

		if err := sendOnce(context.Background(), markers, alert{key: "task/a", title: "T", body: "B"}); err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 {
			t.Errorf("got %d sends, want 1", len(*got))
		}
	})

	t.Run("write error does not fail the invocation", func(t *testing.T) {
		got := capture(t)
		prev := markers
		markers = &markerStore{blob: errBlob{writeErr: errors.New("s3 down")}}
		t.Cleanup(func() { markers = prev })

		if err := sendOnce(context.Background(), markers, alert{key: "task/a", title: "T", body: "B"}); err != nil {
			t.Fatalf("a marker write failure must not trigger an async-invoke retry: %v", err)
		}
		if len(*got) != 1 {
			t.Errorf("got %d sends, want 1", len(*got))
		}
	})
}

// A nil store is a working configuration (STATE_BUCKET unset): every alert goes
// out, exactly as before deduplication existed.
func TestSendOnce_NilStoreSendsEveryTime(t *testing.T) {
	got := capture(t)
	prev := markers
	markers = nil
	t.Cleanup(func() { markers = prev })

	for i := 0; i < 2; i++ {
		if err := sendOnce(context.Background(), nil, alert{key: "task/a", title: "T", body: "B"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(*got) != 2 {
		t.Errorf("got %d sends, want 2", len(*got))
	}
}
