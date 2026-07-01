// Package archive captures faithful daily snapshots of ephemeral upstream data
// (data that only exists "as of now" and is unrecoverable once the day passes).
// It is a leaf: it imports only the standard library. Concrete Sources live in
// their home packages and are wired together by cmd/archive.go.
package archive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Artifact is one file to archive: the upstream response bytes verbatim plus the
// filename it should land under within the source's dated partition.
type Artifact struct {
	Filename string
	Bytes    []byte
}

// Source fetches the ephemeral artifacts for one upstream (e.g. all HKB
// rankings, or all Savant CSVs) as of the given capture date.
type Source interface {
	Name() string
	Fetch(ctx context.Context, date time.Time) ([]Artifact, error)
}

// FuncSource adapts a plain fetch function into a Source, so cmd/archive.go can
// wire each package's ArchiveArtifacts without a bespoke type per source.
type FuncSource struct {
	N string
	F func(ctx context.Context, date time.Time) ([]Artifact, error)
}

func (s FuncSource) Name() string { return s.N }
func (s FuncSource) Fetch(ctx context.Context, date time.Time) ([]Artifact, error) {
	return s.F(ctx, date)
}

// Writer lays artifacts down under Root/<source>/dt=YYYY-MM-DD/<filename>.
type Writer struct{ Root string }

// Write is atomic per (source, date): it stages artifacts in a sibling temp dir,
// then swaps it into place, so a partial fetch never lands as a complete
// partition. Re-writing a date fully replaces that day's blobs (last-write-wins).
func (w Writer) Write(date time.Time, source string, arts []Artifact) error {
	dir := filepath.Join(w.Root, source, "dt="+date.UTC().Format("2006-01-02"))
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	for _, a := range arts {
		if err := os.WriteFile(filepath.Join(tmp, a.Filename), a.Bytes, 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// archiveHTTPClient is the shared client for raw archival fetches. Timeout is
// generous (Savant CSVs are the slowest); ctx carries any tighter deadline.
var archiveHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Get performs a raw HTTP GET and returns the response body verbatim. It is the
// single fetch primitive every Source uses, so the GET+status+read block is not
// duplicated across the hkb/projections/waivers/prospects archive files.
func Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := archiveHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
