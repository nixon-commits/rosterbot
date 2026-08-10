package cmd

import "testing"

// The Pos column shipped blank for 340 of 515 rostered players because the
// mapping read MultiPositions, which Fantrax populates only for players
// eligible at more than one named position. These cases are the real payload
// shapes that distinguish the three candidate fields, so a future "tidy" back
// onto MultiPositions or onto the Positions IDs fails here rather than in the
// browser.
func TestPositionDisplay(t *testing.T) {
	tests := []struct {
		name, short, want string
	}{
		// Observed live in .cache/fantrax-player-pool-*.json. Every one of
		// these is empty in MultiPositions except Witt and Ohtani.
		{"single position", "OF", "OF"},         // Juan Soto, James Wood
		{"single pitcher", "SP", "SP"},          // Paul Skenes
		{"catcher drops the UT flex", "C", "C"}, // Cal Raleigh, Positions ["001","014"]
		{"multi position", "SS,INF", "SS,INF"},  // Bobby Witt Jr.
		{"two-way keeps UT", "UT,P", "UT,P"},    // Ohtani, Positions ["014","017"]

		// The documented-but-not-currently-emitted HTML form. Upstream's own
		// doc comment gives "<b>UT</b>,SP,UT2"; the roster endpoints emit
		// "<b>C</b>".
		{"bold primary", "<b>C</b>", "C"},
		{"bold within a list", "<b>UT</b>,SP,UT2", "UT,SP,UT2"},

		{"empty stays empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := positionDisplay(tc.short); got != tc.want {
				t.Errorf("positionDisplay(%q) = %q, want %q", tc.short, got, tc.want)
			}
		})
	}
}

// An unterminated tag must not swallow the rest of the string -- a truncated
// payload should degrade to a short label, never to a silently empty cell that
// reads as "this player has no position".
func TestPositionDisplay_UnterminatedTagDoesNotEatTheString(t *testing.T) {
	if got := positionDisplay("OF,<b"); got != "OF," {
		t.Errorf("positionDisplay(%q) = %q, want %q", "OF,<b", got, "OF,")
	}
}
