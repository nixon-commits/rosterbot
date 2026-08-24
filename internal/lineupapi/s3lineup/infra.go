package s3lineup

import (
	"context"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// InfraStore lists state-bucket prefixes for GET /v1/infra.
//
// Unlike the other stores here it is keyed by nothing — each call enumerates a
// prefix and reports aggregates — so its Blob is scoped to the bucket root and
// every prefix arrives per request. It reads on demand rather than from a
// precomputed file so the status page can never present its own staleness as
// health.
type InfraStore struct{ blob *s3blob.Blob }

// NewInfra builds a lister over the whole bucket; prefixes come from
// internal/statestore/layout per request.
func NewInfra(ctx context.Context, bucket string) (*InfraStore, error) {
	b, err := s3blob.New(ctx, bucket, "")
	if err != nil {
		return nil, err
	}
	return &InfraStore{blob: b}, nil
}

// dtRe extracts a Hive dt= partition value from anywhere in a key.
var dtRe = regexp.MustCompile(`(?:^|/)dt=(\d{4}-\d{2}-\d{2})(?:/|$)`)

// systemRe extracts the Analysis Store's second partition dimension.
var systemRe = regexp.MustCompile(`(?:^|/)system=([^/]+)(?:/|$)`)

// dayFileRe reads the day out of a file NAMED for it. The projection snapshots
// under backtest/ record their date that way — backtest/user=<uid>/
// snapshots-systems/system=<sys>/2026-08-01.json carries no dt= segment — and
// whether one exists for a day is what decides if an Analysis Store gap can be
// re-graded. Anchored on the basename so a date in a directory name cannot be
// mistaken for one.
var dayFileRe = regexp.MustCompile(`(?:^|/)(\d{4}-\d{2}-\d{2})\.[A-Za-z0-9]+$`)

// userRe extracts the tenant segment. It anchors at the START of the key
// relative to the prefix, because user= is always the first segment a
// per-tenant artifact adds — matching it anywhere would also catch a stray
// "user=" inside a filename and invent a tenant.
var userRe = regexp.MustCompile(`^user=([^/]+)/`)

// maxKeys bounds a single listing. The state bucket's biggest prefix is cache/
// (~thousands of small objects); this caps the work per request so the Lambda
// can't be walked into a timeout by an unbounded prefix.
const maxKeys = 20000

// ListPrefix enumerates one prefix and returns aggregates plus the partition
// and sub-dimension values found.
//
// Sub-dimensions are derived from key structure rather than requested
// separately: for analysis/grades/ that is the system= segment (so a shadow
// system that quietly stopped shows as a missing entry), and for archive/ it is
// the first path segment (the per-source directories: hkb, fangraphs, savant,
// prospects). Both come free from the same listing.
func (s *InfraStore) ListPrefix(ctx context.Context, prefix string) (lineupapi.PrefixListing, error) {
	var out lineupapi.PrefixListing
	parts := map[string]bool{}
	subs := map[string]bool{}
	// Per-tenant slices, so a dead tenant cannot hide behind a live one.
	tenantObjects := map[string]int{}
	tenantNewest := map[string]time.Time{}
	tenantParts := map[string]map[string]bool{}
	// Marker-vs-data day sets, differenced at the end. A day counts as
	// deliberately skipped only when a skip marker is the ONLY thing under it;
	// markers are never deleted and grade re-grades a trailing window, so a
	// marker can outlive the emptiness it recorded. See
	// lineupapi.PrefixListing.SkippedDays.
	markerDays := map[string]bool{}
	dataDays := map[string]bool{}
	tenantMarker := map[string]map[string]bool{}
	tenantData := map[string]map[string]bool{}
	mark := func(m map[string]map[string]bool, uid, day string) {
		if m[uid] == nil {
			m[uid] = map[string]bool{}
		}
		m[uid][day] = true
	}

	err := s.blob.Walk(ctx, prefix, func(o s3blob.Object) bool {
		out.Objects++
		out.Bytes += o.Size
		if o.LastModified.After(out.LastModified) {
			out.LastModified = o.LastModified
		}

		rel := strings.TrimPrefix(o.Key, prefix)

		// The day this key belongs to, however the prefix spells it.
		day := ""
		if m := dtRe.FindStringSubmatch(rel); m != nil {
			day = m[1]
		} else if m := dayFileRe.FindStringSubmatch(rel); m != nil {
			day = m[1]
		}

		// The basename is the only signal available: this lister holds
		// ListBucket and no GetObject, so it can never open the body.
		isSkipMarker := pathpkg.Base(rel) == analysis.SkipMarkerFilename

		if m := userRe.FindStringSubmatch(rel); m != nil {
			uid := m[1]
			tenantObjects[uid]++
			if o.LastModified.After(tenantNewest[uid]) {
				tenantNewest[uid] = o.LastModified
			}
			if day != "" {
				if tenantParts[uid] == nil {
					tenantParts[uid] = map[string]bool{}
				}
				tenantParts[uid][day] = true
				if isSkipMarker {
					mark(tenantMarker, uid, day)
				} else {
					mark(tenantData, uid, day)
				}
			}
		}
		if day != "" {
			parts[day] = true
			if isSkipMarker {
				markerDays[day] = true
			} else {
				dataDays[day] = true
			}
		}
		if m := systemRe.FindStringSubmatch(rel); m != nil {
			subs[m[1]] = true
		} else if prefix == "archive/" {
			// archive/<source>/dt=.../file — the source is the first segment.
			if i := strings.Index(rel, "/"); i > 0 {
				subs[rel[:i]] = true
			}
		}
		return out.Objects < maxKeys
	})
	if err != nil {
		return lineupapi.PrefixListing{}, err
	}

	out.Partitions = sortedKeys(parts)
	out.Subkeys = sortedKeys(subs)

	// Objects hits the cap exactly when the callback declined to continue (see
	// the `return out.Objects < maxKeys` below), which is the only way this
	// walk stops before Walk itself runs out of pages. That makes >= maxKeys
	// the correct truncation test rather than == : a prefix whose true size is
	// exactly maxKeys would otherwise be misread as cut off.
	out.Truncated = out.Objects >= maxKeys

	// A skipped day is a POSITIVE claim — "a marker is the only object under
	// this day" — and a truncated walk cannot support it. Within a dt= day S3
	// returns keys in lexicographic order, and "no-actuals.json" sorts before
	// "system=…" (n < s), so a walk that stops in between sees the marker and
	// not the grades beside it, and the day reads as deliberately skipped when
	// it was graded. Withhold the whole field rather than emit a label that is
	// wrong for a reason nothing on the page discloses.
	if !out.Truncated {
		out.SkippedDays = lineupapi.MarkerOnlyDays(markerDays, dataDays)
	}
	// Same suppression for the per-tenant slices, which are cut from the one
	// walk and are therefore truncated together with it.
	skipped := func(marker, data map[string]bool) []string {
		if out.Truncated {
			return nil
		}
		return lineupapi.MarkerOnlyDays(marker, data)
	}
	if len(tenantObjects) > 0 {
		out.Tenants = make(map[string]lineupapi.TenantListing, len(tenantObjects))
		for uid, n := range tenantObjects {
			out.Tenants[uid] = lineupapi.TenantListing{
				Objects:      n,
				LastModified: tenantNewest[uid],
				Partitions:   sortedKeys(tenantParts[uid]),
				SkippedDays:  skipped(tenantMarker[uid], tenantData[uid]),
			}
		}
	}
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
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

var _ lineupapi.InfraLister = (*InfraStore)(nil)
