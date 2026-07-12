package teams

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"SDP", "SD"},
		{"SFG", "SF"},
		{"KCR", "KC"},
		{"WSN", "WSH"},
		{"TBR", "TB"},
		{"AZ", "ARI"},
		{"CWS", "CHW"},
		{"OAK", "ATH"}, // 2026 rebrand: the exact class of drift this package guards against.
		{"NYY", "NYY"}, // already-canonical passthrough
		{"  bos ", "BOS"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Normalize(tt.in); got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	for _, in := range []string{"SDP", "OAK", "NYY", "ath"} {
		once := Normalize(in)
		twice := Normalize(once)
		if once != twice {
			t.Errorf("Normalize not idempotent for %q: once=%q twice=%q", in, once, twice)
		}
	}
}
