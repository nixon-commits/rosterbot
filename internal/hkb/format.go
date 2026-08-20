package hkb

import (
	"fmt"
	"strings"
)

// FormatOPS formats an OPS value like ".812" (no leading zero).
func FormatOPS(ops float64) string {
	s := fmt.Sprintf("%.3f", ops)
	if strings.HasPrefix(s, "0") {
		return s[1:] // ".812" instead of "0.812"
	}
	return s // "1.012" stays as-is
}

// FormatValue formats an HKB value integer with comma separators.
func FormatValue(v int) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
