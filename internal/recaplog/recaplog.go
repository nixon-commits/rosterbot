// Package recaplog reads CloudFront access logs for the recap site and reduces
// them to the recent-hits view the dashboard shows: when a page was read, from
// what IP, which page, and which edge location served it.
//
// The recap site is public and unauthenticated, so these logs are the only
// readership signal that exists — there is no session to attribute a view to.
// That bounds what this package can honestly claim: a client IP is a network,
// not a person.
//
// It is a leaf. Parsing and aggregation are pure; the bytes arrive through the
// Store seam, whose S3 implementation lives in internal/recaplog/s3recaplog so
// the AWS SDK stays out of here (the same arrangement as cache/cachestore and
// ndjsonstore/s3ndjson).
package recaplog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Prefix is where CloudFront writes the recap site's access logs within the log
// bucket. It must match the LogFilePrefix set on the SiteCdn distribution in
// infra/infra.go — changing one without the other silently yields no data.
const Prefix = "recap/"

// Store is read-only byte access to the log objects under a bucket prefix.
// Keys are prefix-relative. There is no Put: CloudFront is the only writer.
type Store interface {
	// List returns every key under prefix; an empty prefix is not an error.
	List(prefix string) ([]string, error)
	// Get reads one log object.
	Get(key string) ([]byte, error)
}

// Hit is one page read.
type Hit struct {
	Time         time.Time `json:"time"`
	ClientIP     string    `json:"ip"`
	URI          string    `json:"uri"`
	EdgeLocation string    `json:"edge"`
	Status       int       `json:"status"`
}

// Model is the JSON the dashboard fetches.
type Model struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Hits        []Hit     `json:"hits"`
	// Empty distinguishes "no readers yet" from "failed to load", so the tab
	// can say the former rather than showing an error. A freshly deployed log
	// prefix is empty for real and must render cleanly.
	Empty bool `json:"empty"`
}

// CloudFront standard-log field positions. The format is positional TSV with a
// #Fields: header; these indexes match that header and must not be reordered.
const (
	fDate    = 0
	fTime    = 1
	fEdge    = 2
	fIP      = 4
	fURI     = 7
	fStatus  = 8
	minField = 9 // a usable row must reach at least the status column
)

// Parse decodes one gzipped CloudFront access-log object into hits.
//
// Every row is returned, including asset and error requests — filtering is
// BuildModel's job, so a caller that wants the raw picture still can. Malformed
// rows are skipped rather than failing the object: one bad line must not blank
// the whole readership view.
func Parse(gz []byte) ([]Hit, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer zr.Close()

	var hits []Hit
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// #Version / #Fields comment lines carry no data.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) <= minField {
			continue
		}
		ts, err := time.Parse("2006-01-02 15:04:05", f[fDate]+" "+f[fTime])
		if err != nil {
			continue
		}
		status, err := strconv.Atoi(f[fStatus])
		if err != nil {
			continue
		}
		hits = append(hits, Hit{
			Time:         ts.UTC(),
			ClientIP:     f[fIP],
			URI:          f[fURI],
			EdgeLocation: f[fEdge],
			Status:       status,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return hits, nil
}

// IsPageRead reports whether a hit is a person opening a recap page, as opposed
// to the browser fetching an asset alongside it.
//
// CloudFront logs every object, so without this the table fills with favicon
// and stylesheet rows — the /favicon.ico 403 that accompanies every real visit
// is the canonical example. "/" counts because the distribution's
// DefaultRootObject serves index.html there.
func IsPageRead(h Hit) bool {
	if h.Status != 200 {
		return false
	}
	return h.URI == "/" || strings.HasSuffix(h.URI, ".html")
}

// BuildModel filters to page reads, orders newest first and caps at limit
// (limit <= 0 means no cap). Ties break on IP then URI so equal-timestamp rows
// have a stable order rather than reshuffling between runs.
func BuildModel(hits []Hit, limit int, now time.Time) Model {
	page := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if IsPageRead(h) {
			page = append(page, h)
		}
	}
	sort.SliceStable(page, func(i, j int) bool {
		a, b := page[i], page[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.After(b.Time)
		}
		if a.ClientIP != b.ClientIP {
			return a.ClientIP < b.ClientIP
		}
		return a.URI < b.URI
	})
	if limit > 0 && len(page) > limit {
		page = page[:limit]
	}
	return Model{GeneratedAt: now.UTC(), Hits: page, Empty: len(page) == 0}
}

// ReadRecent loads the newest log objects from store until it has at least
// limit page reads, then builds the model.
//
// CloudFront names objects <dist-id>.YYYY-MM-DD-HH.<hash>.gz, so a descending
// key sort is reverse-chronological to the hour. Walking newest-first and
// stopping once the limit is met keeps the read bounded — the alternative,
// reading the whole prefix, grows with the 90-day retention window for no
// benefit to a view that only ever shows the most recent rows.
func ReadRecent(store Store, prefix string, limit int, now time.Time) (Model, error) {
	keys, err := store.List(prefix)
	if err != nil {
		return Model{}, fmt.Errorf("list %s: %w", prefix, err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	var hits []Hit
	for _, k := range keys {
		if !strings.HasSuffix(k, ".gz") {
			continue
		}
		b, err := store.Get(k)
		if err != nil {
			// A single unreadable object must not blank the view; the rest of
			// the window is still worth showing.
			continue
		}
		hs, err := Parse(b)
		if err != nil {
			continue
		}
		hits = append(hits, hs...)

		if limit > 0 {
			n := 0
			for _, h := range hits {
				if IsPageRead(h) {
					n++
				}
			}
			if n >= limit {
				break
			}
		}
	}
	return BuildModel(hits, limit, now), nil
}
