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

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	c := New(NewTokenSource(key, "K", "T"), srv.Client())
	c.hostOverride = srv.URL // test seam; production derives the host from environment
	return c
}

func TestPushSendsCorrectTopicAndPayload(t *testing.T) {
	var gotPath, gotTopic, gotAuth, gotPushType string
	var body map[string]any

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	// apns-topic IS the bundle id, and it must come from the device record —
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

func TestPushRoutesByTheDeviceEnvironment(t *testing.T) {
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
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
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
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"reason":"PayloadTooLarge"}`)
	})
	err := c.Push(context.Background(),
		lineupapi.PushDevice{Token: "t", Environment: "production", BundleID: "b"}, Payload{})
	if err == nil {
		t.Fatal("want an error for a rejected payload")
	}
	if errors.Is(err, ErrDeviceGone) {
		t.Fatal("PayloadTooLarge must not delete the device")
	}
}
