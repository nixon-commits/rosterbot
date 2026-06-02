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
	}
	for _, tt := range tests {
		if got := Normalize(tt.input); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
