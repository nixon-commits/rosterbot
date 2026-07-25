package lineupapi

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// Health is a per-artifact verdict, ordered from best to worst.
type Health string

const (
	HealthOK      Health = "ok"      // fresh, or ephemeral (age carries no signal)
	HealthGap     Health = "gap"     // a day is missing and cannot be recovered
	HealthStale   Health = "stale"   // newest object is older than the artifact's MaxAge
	HealthMissing Health = "missing" // durable prefix is empty — nothing has ever landed
	HealthUnknown Health = "unknown" // the listing itself failed
)

// PrefixListing is the raw result of enumerating one prefix. The adapter does
// the S3 call; every judgement about what the numbers mean is made here, so the
// health rules are testable without AWS.
type PrefixListing struct {
	Objects      int       `json:"objects"`
	Bytes        int64     `json:"bytes"`
	LastModified time.Time `json:"last_modified"`

	// Partitions holds the dt= values found (YYYY-MM-DD), for artifacts that
	// are Hive-partitioned. Empty for flat prefixes.
	Partitions []string `json:"partitions,omitempty"`

	// Subkeys names the second-level dimension where one exists — the four
	// projection systems under analysis/grades/, the archive's per-source
	// directories. A missing entry here is the "one shadow system quietly
	// stopped" case that no error would otherwise surface.
	Subkeys []string `json:"subkeys,omitempty"`
}

// InfraLister enumerates one prefix of the state bucket. Implemented by
// s3lineup for the deployed Lambda; nil in local `serve`, where GET /v1/infra
// returns 501 like the other optional routes.
type InfraLister interface {
	ListPrefix(ctx context.Context, prefix string) (PrefixListing, error)
}

// ArtifactStatus is one row of the status page.
type ArtifactStatus struct {
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
	Health      Health `json:"health"`
	Durable     bool   `json:"durable"`
	Producer    string `json:"producer,omitempty"`
	NoBackfill  bool   `json:"no_backfill,omitempty"`
	Partitioned bool   `json:"partitioned,omitempty"`

	Objects       int       `json:"objects"`
	Bytes         int64     `json:"bytes"`
	LastModified  time.Time `json:"last_modified,omitempty"`
	AgeSeconds    float64   `json:"age_seconds,omitempty"`
	MaxAgeSeconds float64   `json:"max_age_seconds,omitempty"`

	LatestPartition string   `json:"latest_partition,omitempty"`
	Partitions      int      `json:"partitions,omitempty"`
	Gaps            []string `json:"gaps,omitempty"`
	Subkeys         []string `json:"subkeys,omitempty"`

	Error string `json:"error,omitempty"`
}

// InfraStatus is the whole page.
//
// GeneratedAt is always the moment of the request: this endpoint lists S3 live
// rather than serving a precomputed file. That is the point — a status page
// built from a scheduled artifact would go stale in exactly the situation it
// exists to detect, and could report "all healthy" while the job that writes it
// is the thing that died.
type InfraStatus struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Artifacts   []ArtifactStatus `json:"artifacts"`
}

// artifactHealth judges one listing against the artifact's expectations.
//
// Ephemeral artifacts are always OK: cache/ is TTL-evicted by design, so both
// an old object and an empty prefix (a cold start) are normal.
func artifactHealth(a layout.Artifact, l PrefixListing, now time.Time) Health {
	if !a.Durable {
		return HealthOK
	}
	if l.Objects == 0 {
		return HealthMissing
	}
	if a.MaxAge > 0 && !l.LastModified.IsZero() && now.Sub(l.LastModified) > a.MaxAge {
		return HealthStale
	}
	return HealthOK
}

const partitionLayout = "2006-01-02"

// findGaps returns the dt= days absent from a partitioned series, scanning from
// its earliest partition up to YESTERDAY. Today is excluded deliberately: the
// producers run mid-morning UTC, so "today isn't written yet" is normal for
// most of the day and would otherwise show as a permanent-looking gap every
// morning — the kind of false alarm that trains you to ignore the page.
func findGaps(partitions []string, now time.Time) []string {
	if len(partitions) < 2 {
		return nil
	}
	days := make([]string, len(partitions))
	copy(days, partitions)
	sort.Strings(days)

	first, err := time.ParseInLocation(partitionLayout, days[0], time.UTC)
	if err != nil {
		return nil
	}
	last, err := time.ParseInLocation(partitionLayout, days[len(days)-1], time.UTC)
	if err != nil {
		return nil
	}
	// Never scan past yesterday, even if a partition is somehow dated ahead.
	yesterday := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if last.After(yesterday) {
		last = yesterday
	}

	have := make(map[string]bool, len(days))
	for _, d := range days {
		have[d] = true
	}
	var gaps []string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if key := d.Format(partitionLayout); !have[key] {
			gaps = append(gaps, key)
		}
	}
	return gaps
}

// buildStatus lists every artifact and judges it. A listing failure is confined
// to its own row (HealthUnknown + the message) so one broken prefix cannot
// blank the page — the same soft-fail posture the rest of the API takes.
func buildStatus(ctx context.Context, lister InfraLister, artifacts []layout.Artifact, now time.Time) InfraStatus {
	st := InfraStatus{GeneratedAt: now, Artifacts: make([]ArtifactStatus, 0, len(artifacts))}

	for _, a := range artifacts {
		row := ArtifactStatus{
			Name:        a.Name,
			Prefix:      a.S3Prefix,
			Durable:     a.Durable,
			Producer:    a.Producer,
			NoBackfill:  a.NoBackfill,
			Partitioned: a.Partitioned,
		}
		if a.MaxAge > 0 {
			row.MaxAgeSeconds = a.MaxAge.Seconds()
		}

		l, err := lister.ListPrefix(ctx, a.S3Prefix)
		if err != nil {
			row.Health = HealthUnknown
			row.Error = err.Error()
			st.Artifacts = append(st.Artifacts, row)
			continue
		}

		row.Objects, row.Bytes, row.LastModified = l.Objects, l.Bytes, l.LastModified
		row.Subkeys = l.Subkeys
		if !l.LastModified.IsZero() {
			row.AgeSeconds = now.Sub(l.LastModified).Seconds()
		}
		row.Health = artifactHealth(a, l, now)

		if a.Partitioned && len(l.Partitions) > 0 {
			days := make([]string, len(l.Partitions))
			copy(days, l.Partitions)
			sort.Strings(days)
			row.Partitions = len(days)
			row.LatestPartition = days[len(days)-1]
			row.Gaps = findGaps(days, now)

			// A gap is only a health FAILURE where the day cannot be recovered.
			// Grades can be re-graded from archived snapshots; team values
			// cannot be reconstructed at all (docs/adr/0002), so that gap is
			// permanent data loss and outranks a fresh newest-partition.
			if a.NoBackfill && len(row.Gaps) > 0 && row.Health == HealthOK {
				row.Health = HealthGap
			}
		}
		st.Artifacts = append(st.Artifacts, row)
	}
	return st
}

func (cfg Config) handleInfra(w http.ResponseWriter, r *http.Request) {
	if cfg.Infra == nil {
		writeErr(w, http.StatusNotImplemented, "infra status not configured")
		return
	}
	st := buildStatus(r.Context(), cfg.Infra, layout.All(), time.Now().UTC())
	writeJSON(w, http.StatusOK, st)
}

// FileInfraStore lists the local-filesystem equivalents of the state-bucket
// prefixes, so `serve` shows the same view against a dev machine's .cache/,
// .analysis/, .teamvalue/ and friends. Mirrors the FileXxxStore pattern the
// other optional stores use.
//
// It takes the local dir from layout.Artifact.LocalDir rather than the S3
// prefix, which is the whole reason that table carries both.
type FileInfraStore struct{ root string }

// NewFileInfraStore roots the lister at a directory (usually "." — the layout's
// LocalDir values are already relative to the repo root).
func NewFileInfraStore(root string) *FileInfraStore { return &FileInfraStore{root: root} }

// ListPrefix walks the local directory matching the given S3 prefix. An absent
// directory is not an error: it lists as zero objects, which the health rules
// then read as "missing" for a durable artifact and "fine" for an ephemeral
// one — exactly as an empty S3 prefix would.
func (f *FileInfraStore) ListPrefix(ctx context.Context, prefix string) (PrefixListing, error) {
	dir := localDirFor(prefix)
	if dir == "" {
		return PrefixListing{}, nil
	}
	full := filepath.Join(f.root, dir)

	var out PrefixListing
	parts := map[string]bool{}
	subs := map[string]bool{}

	err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, don't fail the whole listing
		}
		rel, relErr := filepath.Rel(full, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if m := dtDirRe.FindStringSubmatch(rel); m != nil {
				parts[m[1]] = true
			}
			if m := systemDirRe.FindStringSubmatch(rel); m != nil {
				subs[m[1]] = true
			}
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		out.Objects++
		out.Bytes += info.Size()
		if info.ModTime().After(out.LastModified) {
			out.LastModified = info.ModTime()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return PrefixListing{}, err
	}

	out.Partitions = sortedStrings(parts)
	out.Subkeys = sortedStrings(subs)
	return out, nil
}

var (
	dtDirRe     = regexp.MustCompile(`(?:^|/)dt=(\d{4}-\d{2}-\d{2})(?:/|$)`)
	systemDirRe = regexp.MustCompile(`(?:^|/)system=([^/]+)(?:/|$)`)
)

// localDirFor maps an S3 prefix back to its local directory via the layout
// table, so the mapping is never written down twice.
func localDirFor(prefix string) string {
	for _, a := range layout.All() {
		if a.S3Prefix == prefix {
			return a.LocalDir
		}
	}
	return ""
}

func sortedStrings(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
