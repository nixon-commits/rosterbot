package statestore

import (
	"context"
	"testing"
)

func TestArtifactLayout(t *testing.T) {
	cases := []struct {
		name         string
		a            artifact
		wantPrefix   string
		wantLocalDir string
	}{
		{"cache", cacheArtifact, "cache/", ""},
		{"analysis", analysisArtifact, "analysis/", ".analysis"},
		{"teamValue", teamValueArtifact, "analysis/team-values/", ".teamvalue"},
		{"lineupGap", lineupGapArtifact, "analysis/lineup-gaps/", ".lineupgap"},
		{"runLedger", runLedgerArtifact, "runledger/", ".lineup/runs"},
		{"runOutput", runOutputArtifact, "runs/", ".lineup/outputs"},
		{"notification", notificationArtifact, "notifications/", ".lineup/notifications"},
		{"progress", progressArtifact, "runs/", ".lineup/progress"},
		{"lineup", lineupArtifact, "lineup/", ".lineup"},
	}
	for _, tc := range cases {
		if tc.a.s3Prefix != tc.wantPrefix {
			t.Errorf("%s: s3Prefix = %q, want %q", tc.name, tc.a.s3Prefix, tc.wantPrefix)
		}
		if tc.a.localDir != tc.wantLocalDir {
			t.Errorf("%s: localDir = %q, want %q", tc.name, tc.a.localDir, tc.wantLocalDir)
		}
	}
}

func TestPickRoutesToLocalWhenBucketEmpty(t *testing.T) {
	got, err := pick(New(""), artifact{"p/", "dir"},
		func(_ context.Context, b, p string) (string, error) { return "s3:" + b + "/" + p, nil },
		func(dir string) string { return "file:" + dir })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "file:dir" {
		t.Errorf("pick(bucket=\"\") = %q, want file:dir", got)
	}
}

func TestPickRoutesToS3WhenBucketSet(t *testing.T) {
	got, err := pick(New("mybucket"), artifact{"p/", "dir"},
		func(_ context.Context, b, p string) (string, error) { return "s3:" + b + "/" + p, nil },
		func(dir string) string { return "file:" + dir })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "s3:mybucket/p/" {
		t.Errorf("pick(bucket set) = %q, want s3:mybucket/p/", got)
	}
}

func TestLocalConstructors(t *testing.T) {
	s := New("")
	if cs, err := s.CacheStore(); err != nil || cs != nil {
		t.Errorf("CacheStore() local = (%v, %v), want (nil, nil)", cs, err)
	}
	if v, err := s.AnalysisWriter(); err != nil || v == nil {
		t.Errorf("AnalysisWriter() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.AnalysisReader(); err != nil || v == nil {
		t.Errorf("AnalysisReader() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.TeamValueWriter(); err != nil || v == nil {
		t.Errorf("TeamValueWriter() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.TeamValueReader(); err != nil || v == nil {
		t.Errorf("TeamValueReader() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.LineupGapWriter(); err != nil || v == nil {
		t.Errorf("LineupGapWriter() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.LineupGapReader(); err != nil || v == nil {
		t.Errorf("LineupGapReader() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.RunLedger(); err != nil || v == nil {
		t.Errorf("RunLedger() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Output(); err != nil || v == nil {
		t.Errorf("Output() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Notifications(); err != nil || v == nil {
		t.Errorf("Notifications() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Progress(); err != nil || v == nil {
		t.Errorf("Progress() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.LineupPublisher(); err != nil || v == nil {
		t.Errorf("LineupPublisher() local = (%v, %v), want (non-nil, nil)", v, err)
	}
}
