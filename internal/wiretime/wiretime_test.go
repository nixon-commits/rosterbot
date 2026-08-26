package wiretime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// nonZeroNanos is the fixture every test here uses, and the nanosecond count is
// the entire point of it. A whole-second fixture marshals identically through a
// raw time.Time field, so a test built on one cannot distinguish this type from
// the defect it replaces — it would pass against the unfixed code. 902729184 is
// the measured value from the rosterbot-xj14 report.
var nonZeroNanos = time.Date(2026, 8, 24, 21, 47, 27, 902729184, time.UTC)

func TestMarshalJSON_DropsTheFraction(t *testing.T) {
	got, err := json.Marshal(New(nonZeroNanos))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `"2026-08-24T21:47:27Z"`
	if string(got) != want {
		t.Fatalf("marshal = %s, want %s", got, want)
	}
	// Stated separately from the equality above so a future format change fails
	// on the property rather than only on the literal.
	if strings.Contains(string(got), ".") {
		t.Errorf("marshal = %s, which carries a fractional second; "+
			"ISO8601DateFormatter([.withInternetDateTime]) returns nil on that", got)
	}
}

// TestMarshalJSON_NormalizesAwayFromUTC pins that the type, not the caller,
// owns the zone. A caller holding a local-zone instant is the ordinary case for
// anything read out of a store or handed in from time.Now().
func TestMarshalJSON_NormalizesAwayFromUTC(t *testing.T) {
	zone := time.FixedZone("PDT", -7*60*60)
	got, err := json.Marshal(New(nonZeroNanos.In(zone)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `"2026-08-24T21:47:27Z"`
	if string(got) != want {
		t.Fatalf("marshal = %s, want %s (same instant, rendered UTC)", got, want)
	}
}

// TestZeroValue_MarshalsLikeARawTimeTime is what makes converting a field a
// non-event for unset values: every site being converted already emitted this
// exact string for its zero, because `omitempty` has never omitted a time.Time.
func TestZeroValue_MarshalsLikeARawTimeTime(t *testing.T) {
	got, err := json.Marshal(Time{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw, err := json.Marshal(time.Time{})
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("zero Time = %s, raw zero time.Time = %s; converting a field "+
			"would change what an unset value looks like on the wire", got, raw)
	}
	if !(Time{}).IsZero() {
		t.Error("IsZero() = false on the zero value, so `omitzero` would never fire")
	}
}

func TestRoundTrip(t *testing.T) {
	b, err := json.Marshal(New(nonZeroNanos))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Time
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Time().Equal(nonZeroNanos.Truncate(time.Second)) {
		t.Fatalf("round trip = %v, want %v", got.Time(), nonZeroNanos.Truncate(time.Second))
	}
}

// TestUnmarshalJSON_ReadsAFractionalPayload covers reading a payload produced
// before the field was converted: those carry the full RFC3339Nano fraction,
// and a decoder that rejected them would break exactly at the deploy boundary.
func TestUnmarshalJSON_ReadsAFractionalPayload(t *testing.T) {
	for _, in := range []string{
		`"2026-08-24T21:47:27.902729184Z"`,
		`"2026-08-24T21:47:27.9Z"`,
		`"2026-08-24T14:47:27.902729184-07:00"`,
	} {
		var got Time
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		// Every input above is the same instant to the second.
		if want := time.Date(2026, 8, 24, 21, 47, 27, 0, time.UTC); !got.Time().Equal(want) {
			t.Errorf("unmarshal %s = %v, want %v", in, got.Time(), want)
		}
		// And re-marshalling must produce the canonical shape, not echo the input.
		out, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		if string(out) != `"2026-08-24T21:47:27Z"` {
			t.Errorf("re-marshal of %s = %s, want the canonical shape", in, out)
		}
	}
}

// TestUnmarshalJSON_AbsentIsZeroNotAnError: an unset timestamp is the ordinary
// state for every omitempty field on this API ("never connected", "registered
// before metadata existed"), so it must decode rather than fail.
func TestUnmarshalJSON_AbsentIsZeroNotAnError(t *testing.T) {
	for _, in := range []string{`null`, `""`} {
		got := New(nonZeroNanos) // seeded non-zero so a no-op decode would fail
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if !got.IsZero() {
			t.Errorf("unmarshal %s = %v, want the zero Time", in, got.Time())
		}
	}
}

func TestUnmarshalJSON_RejectsNonsense(t *testing.T) {
	var got Time
	if err := json.Unmarshal([]byte(`"tuesday"`), &got); err == nil {
		t.Fatal("unmarshal of a non-timestamp string succeeded; a silent zero here " +
			"would look exactly like an absent field")
	}
}

// TestOmitzero_OmitsTheZeroValue pins the tag behaviour the converted sites rely
// on, since it is IsZero() on this type — not encoding/json — that makes it work.
func TestOmitzero_OmitsTheZeroValue(t *testing.T) {
	type row struct {
		At Time `json:"at,omitzero"`
	}
	got, err := json.Marshal(row{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{}` {
		t.Fatalf("marshal = %s, want {} — omitzero needs IsZero()", got)
	}
	if got, err = json.Marshal(row{At: New(nonZeroNanos)}); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"at":"2026-08-24T21:47:27Z"}` {
		t.Fatalf("marshal = %s", got)
	}
}

// TestNow_IsCanonical: Now() is the constructor that makes the defect live, so
// its output is asserted directly rather than trusted from New's tests.
func TestNow_IsCanonical(t *testing.T) {
	n := Now()
	if n.Time().Nanosecond() != 0 {
		t.Errorf("Now() carries %d nanoseconds", n.Time().Nanosecond())
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), ".") {
		t.Errorf("Now() marshalled as %s, which carries a fraction", b)
	}
}
