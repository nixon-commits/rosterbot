package s3lineup

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// InfraStore lists state-bucket prefixes for GET /v1/infra.
//
// Unlike the other stores here it is keyed by nothing — each call enumerates a
// prefix and reports aggregates. It reads on demand rather than from a
// precomputed file so the status page can never present its own staleness as
// health.
type InfraStore struct {
	client listAPI
	bucket string
}

// NewInfra builds a lister over the whole bucket; prefixes come from
// internal/statestore/layout per request.
func NewInfra(ctx context.Context, bucket string) (*InfraStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &InfraStore{client: s3.NewFromConfig(cfg), bucket: bucket}, nil
}

// dtRe extracts a Hive dt= partition value from anywhere in a key.
var dtRe = regexp.MustCompile(`(?:^|/)dt=(\d{4}-\d{2}-\d{2})(?:/|$)`)

// systemRe extracts the Analysis Store's second partition dimension.
var systemRe = regexp.MustCompile(`(?:^|/)system=([^/]+)(?:/|$)`)

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

	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: &s.bucket,
		Prefix: &prefix,
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return lineupapi.PrefixListing{}, err
		}
		for _, o := range page.Contents {
			out.Objects++
			if o.Size != nil {
				out.Bytes += *o.Size
			}
			if o.LastModified != nil && o.LastModified.After(out.LastModified) {
				out.LastModified = *o.LastModified
			}
			if o.Key == nil {
				continue
			}
			rel := strings.TrimPrefix(*o.Key, prefix)
			if m := dtRe.FindStringSubmatch(rel); m != nil {
				parts[m[1]] = true
			}
			if m := systemRe.FindStringSubmatch(rel); m != nil {
				subs[m[1]] = true
			} else if prefix == "archive/" {
				// archive/<source>/dt=.../file — the source is the first segment.
				if i := strings.Index(rel, "/"); i > 0 {
					subs[rel[:i]] = true
				}
			}
		}
		if out.Objects >= maxKeys {
			break
		}
	}

	out.Partitions = sortedKeys(parts)
	out.Subkeys = sortedKeys(subs)
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
