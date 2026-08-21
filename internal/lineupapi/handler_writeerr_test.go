package lineupapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// writeErr's message is not always a literal — handleJobRun forwards
// BuildJobArgs' error, which interpolates the caller's own param value. The
// hand-concatenated body this replaced turned a quote in that value into a
// malformed response, or into extra keys of the caller's choosing.
func TestWriteErr_MessageCannotEscapeTheJSONString(t *testing.T) {
	for _, msg := range []string{
		`invalid period: plain`,
		`invalid period: x"y`,
		`invalid period: x","admin":true,"y":"z`,
		"invalid period: line\nbreak",
		`invalid period: back\slash`,
	} {
		rec := httptest.NewRecorder()
		writeErr(rec, 400, msg)

		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not valid JSON for msg %q: %v (body=%s)", msg, err, rec.Body.String())
		}
		if len(got) != 1 {
			t.Errorf("msg %q injected extra keys: %v", msg, got)
		}
		if got["error"] != msg {
			t.Errorf("msg %q round-tripped as %q", msg, got["error"])
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if rec.Code != 400 {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	}
}
