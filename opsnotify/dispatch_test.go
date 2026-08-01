package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// capture swaps the Pushover seam for a recorder and restores it after the test.
func capture(t *testing.T) *[]string {
	t.Helper()
	prev := send
	var got []string
	send = func(title, message string) error {
		got = append(got, title+"|"+message)
		return nil
	}
	t.Cleanup(func() { send = prev })
	return &got
}

const codeBuildEvent = `{
  "version": "0",
  "detail-type": "CodeBuild Build State Change",
  "source": "aws.codebuild",
  "detail": {
    "build-status": "FAILED",
    "project-name": "Build",
    "additional-information": {
      "source-version": "abc1234def",
      "logs": {"deep-link": "https://logs.example/x"}
    }
  }
}`

func TestDispatch_RoutesCodeBuild(t *testing.T) {
	got := capture(t)
	if err := dispatch(context.Background(), json.RawMessage(codeBuildEvent)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "Rosterbot build FAILED") {
		t.Errorf("send = %q", (*got)[0])
	}
}

// An unrecognised source must be a quiet no-op, not an error — returning an
// error would make EventBridge retry it forever.
func TestDispatch_IgnoresUnknownDetailType(t *testing.T) {
	got := capture(t)
	ev := `{"detail-type":"EC2 Instance State-change Notification","source":"aws.ec2","detail":{}}`
	if err := dispatch(context.Background(), json.RawMessage(ev)); err != nil {
		t.Fatalf("unknown detail-type returned an error: %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

func TestDispatch_RejectsMalformedEnvelope(t *testing.T) {
	if err := dispatch(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("want an error for a malformed envelope, got nil")
	}
}
