package recaplog

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"
	"time"
)

// header is the exact two-line preamble CloudFront writes, captured from a real
// delivered object on 2026-07-27. Keeping the real field list here means a
// change to CloudFront's format shows up as a test failure rather than as
// silently shifted columns in production.
const header = "#Version: 1.0\n" +
	"#Fields: date time x-edge-location sc-bytes c-ip cs-method cs(Host) cs-uri-stem sc-status cs(Referer) cs(User-Agent) cs-uri-query cs(Cookie) x-edge-result-type x-edge-request-id x-host-header cs-protocol cs-bytes time-taken x-forwarded-for ssl-protocol ssl-cipher x-edge-response-result-type cs-protocol-version fle-status fle-encrypted-fields c-port time-to-first-byte x-edge-detailed-result-type sc-content-type sc-content-len sc-range-start sc-range-end\n"

// row builds a tab-separated log line with the fields this package reads at
// their real positions and filler elsewhere.
func row(date, tm, edge, ip, uri string, status int) string {
	f := make([]string, 33)
	for i := range f {
		f[i] = "-"
	}
	f[fDate], f[fTime], f[fEdge] = date, tm, edge
	f[fIP], f[fURI], f[fStatus] = ip, uri, fmt.Sprint(status)
	return strings.Join(f, "\t")
}

func gzipped(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// The real object observed in production: two page reads and the favicon 403
// every browser visit drags along.
func realisticObject(t *testing.T) []byte {
	t.Helper()
	return gzipped(t, header+
		row("2026-07-27", "20:12:50", "SFO5-P3", "66.189.172.157", "/", 200)+"\n"+
		row("2026-07-27", "20:12:50", "SFO5-P3", "66.189.172.157", "/favicon.ico", 403)+"\n"+
		row("2026-07-27", "20:13:52", "SFO5-P3", "66.189.172.157", "/week-08.html", 200)+"\n")
}

func TestParseSkipsHeadersAndReadsFields(t *testing.T) {
	hits, err := Parse(realisticObject(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Parse returns everything; filtering belongs to BuildModel.
	if len(hits) != 3 {
		t.Fatalf("want 3 rows (headers skipped, favicon kept), got %d: %+v", len(hits), hits)
	}
	got := hits[0]
	want := Hit{
		Time:         time.Date(2026, 7, 27, 20, 12, 50, 0, time.UTC),
		ClientIP:     "66.189.172.157",
		URI:          "/",
		EdgeLocation: "SFO5-P3",
		Status:       200,
	}
	if got != want {
		t.Errorf("hits[0] = %+v, want %+v", got, want)
	}
}

func TestParseSkipsMalformedRowsWithoutFailingTheObject(t *testing.T) {
	body := header +
		row("2026-07-27", "20:12:50", "SFO5-P3", "1.2.3.4", "/", 200) + "\n" +
		"not\ta\tvalid\trow\n" + // too few fields
		row("nonsense", "xx:yy:zz", "SFO5-P3", "1.2.3.4", "/a.html", 200) + "\n" + // unparseable time
		row("2026-07-27", "20:14:00", "SFO5-P3", "1.2.3.4", "/b.html", 200) + "\n"

	hits, err := Parse(gzipped(t, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want the 2 good rows, got %d: %+v", len(hits), hits)
	}
}

func TestParseRejectsNonGzip(t *testing.T) {
	if _, err := Parse([]byte("plain text, not gzip")); err == nil {
		t.Fatal("want an error for non-gzip input")
	}
}

func TestIsPageRead(t *testing.T) {
	cases := []struct {
		name string
		hit  Hit
		want bool
	}{
		{"root serves index.html", Hit{URI: "/", Status: 200}, true},
		{"week page", Hit{URI: "/week-08.html", Status: 200}, true},
		{"favicon 403 rides along with every browser visit", Hit{URI: "/favicon.ico", Status: 403}, false},
		{"stylesheet is not a page read", Hit{URI: "/style.css", Status: 200}, false},
		{"missing page is not a read", Hit{URI: "/week-99.html", Status: 404}, false},
		{"redirect is not a read", Hit{URI: "/", Status: 301}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsPageRead(c.hit); got != c.want {
				t.Errorf("IsPageRead(%+v) = %v, want %v", c.hit, got, c.want)
			}
		})
	}
}

func TestBuildModelFiltersOrdersAndCaps(t *testing.T) {
	at := func(min int) time.Time { return time.Date(2026, 7, 27, 20, min, 0, 0, time.UTC) }
	hits := []Hit{
		{Time: at(1), ClientIP: "1.1.1.1", URI: "/", Status: 200},
		{Time: at(3), ClientIP: "2.2.2.2", URI: "/week-08.html", Status: 200},
		{Time: at(2), ClientIP: "3.3.3.3", URI: "/favicon.ico", Status: 403},
		{Time: at(5), ClientIP: "4.4.4.4", URI: "/week-09.html", Status: 200},
	}
	m := BuildModel(hits, 2, at(9))

	if m.Empty {
		t.Error("Empty should be false when page reads exist")
	}
	if len(m.Hits) != 2 {
		t.Fatalf("want the limit of 2, got %d", len(m.Hits))
	}
	if !m.Hits[0].Time.Equal(at(5)) || !m.Hits[1].Time.Equal(at(3)) {
		t.Errorf("want newest-first [20:05, 20:03], got %v", []time.Time{m.Hits[0].Time, m.Hits[1].Time})
	}
	for _, h := range m.Hits {
		if h.URI == "/favicon.ico" {
			t.Error("favicon leaked into the model")
		}
	}
}

// A freshly deployed log prefix has no page reads. That must be a clean empty
// model, not an error, or the tab shows a failure on its first day.
func TestBuildModelEmptyIsNotAnError(t *testing.T) {
	m := BuildModel(nil, 50, time.Now())
	if !m.Empty {
		t.Error("Empty should be true with no hits")
	}
	if len(m.Hits) != 0 {
		t.Errorf("want no hits, got %d", len(m.Hits))
	}
}

func TestBuildModelBreaksTiesStably(t *testing.T) {
	ts := time.Date(2026, 7, 27, 20, 0, 0, 0, time.UTC)
	hits := []Hit{
		{Time: ts, ClientIP: "9.9.9.9", URI: "/b.html", Status: 200},
		{Time: ts, ClientIP: "1.1.1.1", URI: "/a.html", Status: 200},
		{Time: ts, ClientIP: "1.1.1.1", URI: "/b.html", Status: 200},
	}
	first := BuildModel(hits, 0, ts)
	second := BuildModel(hits, 0, ts)
	for i := range first.Hits {
		if first.Hits[i] != second.Hits[i] {
			t.Fatalf("ordering is not deterministic at %d: %+v vs %+v", i, first.Hits[i], second.Hits[i])
		}
	}
	if first.Hits[0].ClientIP != "1.1.1.1" || first.Hits[0].URI != "/a.html" {
		t.Errorf("equal timestamps should order by IP then URI, got %+v", first.Hits[0])
	}
}

// fakeStore serves objects by key and counts reads so the early stop is testable.
type fakeStore struct {
	objs       map[string][]byte
	unreadable map[string]bool
	gets       int
}

func (f *fakeStore) List(prefix string) ([]string, error) {
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (f *fakeStore) Get(key string) ([]byte, error) {
	f.gets++
	if f.unreadable[key] {
		return nil, fmt.Errorf("boom")
	}
	b, ok := f.objs[key]
	if !ok {
		return nil, fmt.Errorf("no such key %s", key)
	}
	return b, nil
}

func TestReadRecentWalksNewestFirstAndStopsEarly(t *testing.T) {
	f := &fakeStore{objs: map[string][]byte{}}
	// One object per hour; the key's YYYY-MM-DD-HH segment makes a descending
	// key sort reverse-chronological.
	for h := 10; h <= 20; h++ {
		key := fmt.Sprintf("recap/E19.2026-07-27-%02d.abc.gz", h)
		f.objs[key] = gzipped(t, header+
			row("2026-07-27", fmt.Sprintf("%02d:00:00", h), "SFO5-P3", "1.1.1.1", "/week-08.html", 200)+"\n")
	}

	m, err := ReadRecent(f, "recap/", 3, time.Now())
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(m.Hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(m.Hits))
	}
	if got := m.Hits[0].Time.Hour(); got != 20 {
		t.Errorf("newest hit hour = %d, want 20 (newest object first)", got)
	}
	// Three objects hold three page reads, so it must stop after three gets
	// rather than reading all eleven.
	if f.gets != 3 {
		t.Errorf("want 3 object reads before the early stop, got %d", f.gets)
	}
}

func TestReadRecentSkipsUnreadableObjectsAndNonGzKeys(t *testing.T) {
	f := &fakeStore{
		objs: map[string][]byte{
			"recap/E19.2026-07-27-20.aaa.gz": gzipped(t, header+
				row("2026-07-27", "20:00:00", "SFO5-P3", "1.1.1.1", "/week-08.html", 200)+"\n"),
			"recap/E19.2026-07-27-19.bbb.gz": nil, // unreadable
			"recap/NOTES.txt":                []byte("ignore me"),
		},
		unreadable: map[string]bool{"recap/E19.2026-07-27-19.bbb.gz": true},
	}

	m, err := ReadRecent(f, "recap/", 50, time.Now())
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(m.Hits) != 1 {
		t.Fatalf("want the one readable hit, got %d: %+v", len(m.Hits), m.Hits)
	}
}

func TestReadRecentOnEmptyPrefixIsEmptyNotError(t *testing.T) {
	m, err := ReadRecent(&fakeStore{objs: map[string][]byte{}}, "recap/", 50, time.Now())
	if err != nil {
		t.Fatalf("ReadRecent on an empty prefix: %v", err)
	}
	if !m.Empty {
		t.Error("want Empty=true for a prefix with no objects")
	}
}
