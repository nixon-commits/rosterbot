package playername

import "testing"

func TestMatchProbable(t *testing.T) {
	probables := map[string]string{
		Normalize("José Berríos"): "TOR",
		Normalize("Gerrit Cole"):  "NYY",
	}

	cases := []struct {
		name, team string
		want       ProbableMatch
	}{
		{"José Berríos", "TOR", ConfirmedStarter},
		// The join must be diacritic-insensitive from the caller's side too:
		// normalization happens INSIDE MatchProbable, so a caller holding the
		// plain-ASCII spelling still hits.
		{"Jose Berrios", "TOR", ConfirmedStarter},
		// Name announced, club disagrees — a lagging trade on either side.
		// This is a distinct answer, not a miss: roster.CheckILStarters
		// surfaces it rather than dropping it.
		{"Gerrit Cole", "BOS", TeamMismatch},
		{"Some Reliever", "NYY", NotProbable},
	}
	for _, c := range cases {
		if got := MatchProbable(c.name, c.team, probables); got != c.want {
			t.Errorf("MatchProbable(%q, %q) = %v, want %v", c.name, c.team, got, c.want)
		}
	}

	if got := MatchProbable("Gerrit Cole", "NYY", nil); got != NotProbable {
		t.Errorf("MatchProbable against a nil map = %v, want NotProbable", got)
	}
}
