package playername

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// Diacritics
		{"Ronald Acuña Jr.", "ronald acuna"},
		{"Vladímir Guerrero Jr.", "vladimir guerrero"},
		{"Jesús Made", "jesus made"},

		// Suffixes
		{"Bobby Witt Jr.", "bobby witt"},
		{"Bobby Witt Jr", "bobby witt"},
		{"Ken Griffey Sr.", "ken griffey"},
		{"Cal Ripken III", "cal ripken"},
		{"Ken Griffey II", "ken griffey"},

		// Periods
		{"A.J. Puk", "aj puk"},
		{"J.D. Martinez", "jd martinez"},

		// Whitespace
		{"  Juan  Soto  ", "juan soto"},

		// Plain names
		{"Mike Trout", "mike trout"},
		{"Leo De Vries", "leo de vries"},
		{"Leodalis De Vries", "leodalis de vries"},

		// Apostrophes: straight vs the "smart quote" variants a source's
		// text pipeline may substitute must all collapse to the same form.
		{"Tyler O'Neill", "tyler oneill"},
		{"Tyler O’Neill", "tyler oneill"}, // right single quotation mark
		{"Tyler O‘Neill", "tyler oneill"}, // left single quotation mark
		{"Tyler O`Neill", "tyler oneill"}, // grave accent

		// Suffix preceded by a comma ("Last, Jr.") must match the
		// comma-less form used elsewhere in this table.
		{"Vladimir Guerrero, Jr.", "vladimir guerrero"},
		{"Vladimir Guerrero, Jr", "vladimir guerrero"},

		// Hyphenated first/last names must match a space-separated
		// rendering of the same name.
		{"Ha-Seong Kim", "ha seong kim"},
		{"Ha Seong Kim", "ha seong kim"},
	}
	for _, tt := range tests {
		if got := Normalize(tt.input); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
