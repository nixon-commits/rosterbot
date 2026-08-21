package hkb

import "testing"

func TestFormatValue(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{1000, "1,000"},
		{10000, "10,000"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		if got := FormatValue(tt.input); got != tt.want {
			t.Errorf("FormatValue(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatOPS(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.812, ".812"},
		{0.900, ".900"},
		{1.012, "1.012"},
		{0.000, ".000"},
	}
	for _, tt := range tests {
		if got := FormatOPS(tt.input); got != tt.want {
			t.Errorf("FormatOPS(%f) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
