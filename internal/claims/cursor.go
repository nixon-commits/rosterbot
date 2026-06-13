package claims

import (
	"encoding/json"
	"os"
	"time"
)

// cursorFile is the default path for the claims cursor.
const cursorFile = ".cache/last-claims.json"

type cursor struct {
	LastChecked time.Time `json:"lastChecked"`
}

// loadCursor reads the last-checked timestamp; returns zero time on any error.
func loadCursor(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return time.Time{}
	}
	return c.LastChecked
}

// saveCursor writes the last-checked timestamp.
func saveCursor(path string, date time.Time) error {
	data, err := json.MarshalIndent(cursor{LastChecked: date}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
