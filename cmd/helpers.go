package cmd

import (
	"fmt"
	"strings"
	"time"
)

// parseDates parses the --dates flag value into a slice of dates. Shared by
// optimize, shadow, backtest, grade, and recap.
func parseDates(s string, today time.Time) ([]time.Time, error) {
	if s == "" {
		return []time.Time{today}, nil
	}
	if parts := strings.SplitN(s, ":", 2); len(parts) == 2 {
		start, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			return nil, fmt.Errorf("start date: %w", err)
		}
		end, err := time.Parse("2006-01-02", parts[1])
		if err != nil {
			return nil, fmt.Errorf("end date: %w", err)
		}
		if end.Before(start) {
			return nil, fmt.Errorf("end date %s is before start date %s", parts[1], parts[0])
		}
		var dates []time.Time
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		return dates, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, err
	}
	return []time.Time{d}, nil
}
