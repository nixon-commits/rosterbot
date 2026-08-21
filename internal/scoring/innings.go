package scoring

import (
	"strconv"
	"strings"
)

// ParseIP converts MLB's innings-pitched notation ("6.1" = 6 full innings +
// 1 out = 6.333 IP) to a float. The decimal part is outs (0-2), not a
// fractional value, so a naive strconv.ParseFloat would be wrong. Returns 0
// for an empty string.
func ParseIP(s string) float64 {
	if s == "" {
		return 0
	}
	parts := strings.SplitN(s, ".", 2)
	full, _ := strconv.Atoi(parts[0])
	outs := 0
	if len(parts) == 2 {
		outs, _ = strconv.Atoi(parts[1])
	}
	return float64(full) + float64(outs)/3.0
}
