package notify

import (
	"context"
	"testing"
)

func TestTagTitle(t *testing.T) {
	for _, tc := range []struct {
		name, label, title, want string
	}{
		{"empty label leaves title unchanged", "", "Lineup applied", "Lineup applied"},
		{"whitespace-only label leaves title unchanged", "   ", "Lineup applied", "Lineup applied"},
		{"non-empty label is prefixed", "Jon's Team", "Lineup applied", "[Jon's Team] Lineup applied"},
		{"surrounding whitespace on the label is trimmed", "  Testers  ", "Lineup applied", "[Testers] Lineup applied"},
		{"a label that already carries brackets is not double-bracketed", "[Testers]", "Lineup applied", "[Testers] Lineup applied"},
		{"brackets plus surrounding whitespace both trim", "  [Testers]  ", "Lineup applied", "[Testers] Lineup applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tagTitle(tc.label, tc.title)
			if got != tc.want {
				t.Errorf("tagTitle(%q, %q) = %q, want %q", tc.label, tc.title, got, tc.want)
			}
		})
	}
}

// TestPushoverSinkDeliver_AppliesTheTenantLabel proves the label is actually
// wired into Deliver's send, not just correct in tagTitle's own isolated
// table test — a helper that is right in isolation and never reaches the
// call site would leave every real push untagged. It overrides the package
// seam rather than hitting the real Pushover API.
func TestPushoverSinkDeliver_AppliesTheTenantLabel(t *testing.T) {
	orig := sendPushover
	defer func() { sendPushover = orig }()

	var gotTitle string
	sendPushover = func(_, _, title, _ string) error {
		gotTitle = title
		return nil
	}

	sink := &PushoverSink{UserKey: "u", APIToken: "t", TenantLabel: "Jon's Team"}
	if err := sink.Deliver(context.Background(), Event{Title: "Lineup applied", Message: "m"}, "feed-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if want := "[Jon's Team] Lineup applied"; gotTitle != want {
		t.Errorf("Deliver sent title %q, want %q", gotTitle, want)
	}
}

// TestPushoverSinkDeliver_NoLabelLeavesTitleUnchanged pins the healthy/normal
// case: an empty TenantLabel (single-tenant deployments, local dev, and the
// operator's own tenant) must send the title byte-identical to before this
// field existed.
func TestPushoverSinkDeliver_NoLabelLeavesTitleUnchanged(t *testing.T) {
	orig := sendPushover
	defer func() { sendPushover = orig }()

	var gotTitle string
	sendPushover = func(_, _, title, _ string) error {
		gotTitle = title
		return nil
	}

	sink := &PushoverSink{UserKey: "u", APIToken: "t"}
	if err := sink.Deliver(context.Background(), Event{Title: "Lineup applied", Message: "m"}, "feed-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if want := "Lineup applied"; gotTitle != want {
		t.Errorf("Deliver sent title %q, want %q (no tag expected)", gotTitle, want)
	}
}
