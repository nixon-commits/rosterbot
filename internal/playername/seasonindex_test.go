package playername

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pmurley/go-mlb/models"
)

// person builds one dump/search row. Pointer fields are what statsapi omits, so
// a fixture that sets them proves the deref path rather than assuming it.
func person(id int, full, first, last, use string, active bool) models.Person {
	p := models.Person{ID: id, FullName: full, Active: &active}
	if first != "" {
		p.FirstName = &first
	}
	if last != "" {
		p.LastName = &last
	}
	if use != "" {
		p.UseName = &use
	}
	return p
}

func peopleJSON(t *testing.T, people ...models.Person) string {
	t.Helper()
	b, err := json.Marshal(struct {
		People []models.Person `json:"people"`
	}{people})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// statsServer is a fake statsapi covering the three endpoints the resolver
// touches: the five season dumps, people-search, and the people detail fetch.
type statsServer struct {
	// dumps[sportID] is that sport's dump; a sport absent from the map serves
	// an empty dump.
	dumps map[int][]models.Person
	// dumpStatus[sportID], when set, is served instead of a body. 4xx is
	// deliberate in tests: go-mlb does not retry 4xx (client.go:294-296), so
	// the failure is immediate rather than three backed-off attempts.
	dumpStatus map[int]int
	// dumpDelay[sportID] holds that sport's response back, so a test can order
	// the responses deterministically.
	dumpDelay map[int]time.Duration
	// search maps a requested name to the people that search returns for it.
	search map[string][]models.Person
	// detail maps an id to the person the /people fetch returns.
	detail map[int]models.Person

	dumpHits   atomic.Int32
	searchHits atomic.Int32
}

func (s *statsServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/sports/") && strings.HasSuffix(r.URL.Path, "/players"):
			s.dumpHits.Add(1)
			var sport int
			if _, err := fmt.Sscanf(r.URL.Path, "/api/v1/sports/%d/players", &sport); err != nil {
				http.Error(w, "bad sport", http.StatusBadRequest)
				return
			}
			if d, ok := s.dumpDelay[sport]; ok {
				time.Sleep(d)
			}
			if code, ok := s.dumpStatus[sport]; ok {
				http.Error(w, "dump unavailable", code)
				return
			}
			_, _ = w.Write([]byte(peopleJSON(t, s.dumps[sport]...)))
		case r.URL.Path == "/api/v1/people/search":
			s.searchHits.Add(1)
			var out []models.Person
			for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
				out = append(out, s.search[n]...)
			}
			_, _ = w.Write([]byte(peopleJSON(t, out...)))
		case r.URL.Path == "/api/v1/people":
			var out []models.Person
			for _, raw := range strings.Split(r.URL.Query().Get("personIds"), ",") {
				var id int
				if _, err := fmt.Sscanf(raw, "%d", &id); err != nil {
					continue
				}
				if p, ok := s.detail[id]; ok {
					out = append(out, p)
				}
			}
			_, _ = w.Write([]byte(peopleJSON(t, out...)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	useServer(t, srv)
	return srv
}

// useServer points the resolver at srv for the duration of the test and clears
// the process memo on both sides.
//
// The memo reset is not belt-and-braces: it is keyed on the base URL, and the OS
// recycles httptest ports after Close, so without it a later test can inherit an
// earlier test's index and pass or fail for a reason unrelated to its own
// fixtures.
func useServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := mlbBaseURL
	mlbBaseURL = srv.URL
	resetSeasonIndexMemo()
	t.Cleanup(func() {
		mlbBaseURL = old
		resetSeasonIndexMemo()
	})
}

// lowFloor drops the minimum-population guard so a hand-written fixture of two
// or three rows can be indexed at all.
func lowFloor(t *testing.T) {
	t.Helper()
	old := seasonIndexMinPlayers
	seasonIndexMinPlayers = 1
	t.Cleanup(func() { seasonIndexMinPlayers = old })
}

var (
	tommyWhite  = person(695720, "Tommy White", "Thomas", "White", "Tommy", true)
	thomasWhite = person(806258, "Thomas White", "Thomas", "White", "", true)
	mikeTrout   = person(100, "Mike Trout", "Michael", "Trout", "Mike", true)
)

// The season scope removes the RETIRED-namesake class but not namesake
// ambiguity: measured live 2026-08-31 the 2026 index holds 55 contested keys
// over 5,890 players, and `thomas white` is one of them — 695720 (Tommy White,
// firstName Thomas) and 806258 (Thomas White). Every dump row is active:true, so
// claimName cannot arbitrate and a lower-ID tie-break would answer 695720 where
// the production search returns 806258. The index must decline.
func TestSeasonIndex_DeclinesAContestedKey(t *testing.T) {
	ix := buildSeasonIndex([]models.Person{tommyWhite, thomasWhite})

	if _, _, res := ix.find("Thomas White"); res != findContested {
		t.Errorf("find(Thomas White) = %v, want findContested", res)
	}
	id, _, res := ix.find("Tommy White")
	if res != findFound || id != 695720 {
		t.Errorf("find(Tommy White) = %d/%v, want 695720/findFound", id, res)
	}
}

// The same property at the call site, which is where it actually has to hold:
// a contested key must reach the search, and the search's answer must survive.
func TestResolveMLBAMIDs_ContestedIndexKeyFallsThroughToSearch(t *testing.T) {
	lowFloor(t)
	s := &statsServer{
		dumps:  map[int][]models.Person{1: {tommyWhite, thomasWhite}},
		search: map[string][]models.Person{"Thomas White": {thomasWhite}},
		detail: map[int]models.Person{806258: thomasWhite},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Thomas White"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["thomas white"]; got != 806258 {
		t.Errorf("Thomas White → %d, want 806258 (the search's answer, not the index's lower id)", got)
	}
	if s.searchHits.Load() == 0 {
		t.Error("a contested key must reach the search")
	}
}

// The point of the whole change: an uncontested name must not cost a search.
func TestResolveMLBAMIDs_IndexAnswersWithoutSearching(t *testing.T) {
	lowFloor(t)
	s := &statsServer{dumps: map[int][]models.Person{1: {mikeTrout}}}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["mike trout"]; got != 100 {
		t.Errorf("Mike Trout → %d, want 100 (from the index)", got)
	}
	if n := s.searchHits.Load(); n != 0 {
		t.Errorf("people-search was called %d time(s); the index answered every name", n)
	}
}

// A partial index is worse than no index, and the failure is invisible.
// Measured live 2026-08-31: with sport 1 dropped, key `luis garcia` loses 472610
// and reads UNCONTESTED at 671277, so the index answers confidently where the
// full one correctly declines. loadSeasonIndex must therefore refuse the whole
// build rather than index whatever succeeded.
func TestResolveMLBAMIDs_PartialDumpFailureSkipsTheIndexEntirely(t *testing.T) {
	lowFloor(t)
	garcia := person(472610, "Luis Garcia", "Luis", "Garcia", "", true)
	garciaJr := person(671277, "Luis Garcia Jr.", "Luis", "Garcia", "", true)
	// The failing sport answers LAST, so the four that succeed have already
	// landed when it fails. Without that the errgroup's cancellation empties
	// them anyway and the test would pass whether or not the partial build is
	// refused — it has to be the refusal that carries it.
	s := &statsServer{
		dumps:      map[int][]models.Person{1: {garcia}, 12: {garcia}, 13: {garcia}, 14: {garcia}},
		dumpStatus: map[int]int{11: http.StatusNotFound},
		dumpDelay:  map[int]time.Duration{11: 150 * time.Millisecond},
		search:     map[string][]models.Person{"Luis Garcia": {garciaJr}},
		detail:     map[int]models.Person{671277: garciaJr},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Luis Garcia"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["luis garcia"]; got != 671277 {
		t.Errorf("Luis Garcia → %d, want 671277 (a partial dump must not be indexed)", got)
	}
}

// An index outage costs speed, not correctness — every name falls through to the
// search and resolves exactly as well as it does today. Folding it into
// errDegraded would suppress the outer 7-day cache write (ResolveMLBAMIDs),
// turning a slow run into a slow week.
func TestResolveMLBAMIDs_IndexFetchFailureIsNotDegraded(t *testing.T) {
	s := &statsServer{
		dumpStatus: map[int]int{1: 404, 11: 404, 12: 404, 13: 404, 14: 404},
		search:     map[string][]models.Person{"Mike Trout": {mikeTrout}},
		detail:     map[int]models.Person{100: mikeTrout},
	}
	s.start(t)

	dir := t.TempDir()
	rp, err := ResolveMLBAMIDs([]string{"Mike Trout"}, dir)
	if err != nil {
		t.Fatalf("an index outage must not be an error: %v", err)
	}
	if got := rp.ByName["mike trout"]; got != 100 {
		t.Errorf("Mike Trout → %d, want 100 (via the search fallback)", got)
	}
	if !hasPrefixedFile(t, dir, "mlb-player-ids") {
		t.Error("the resolution was not cached; an index outage must not suppress the write")
	}
}

// The complement of the contested case: a name the index has never heard of
// still resolves, because the search is a first-class fallback and not a rump.
// Measured live 2026-08-31, 12 of 13 index misses over the rostered pool are
// players absent from every dump — the 60-day-IL class, which comes back when
// they are activated.
func TestResolveMLBAMIDs_IndexMissFallsThroughToSearch(t *testing.T) {
	lowFloor(t)
	s := &statsServer{
		dumps:  map[int][]models.Person{1: {thomasWhite}},
		search: map[string][]models.Person{"Mike Trout": {mikeTrout}},
		detail: map[int]models.Person{100: mikeTrout},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["mike trout"]; got != 100 {
		t.Errorf("Mike Trout → %d, want 100 (via the search fallback)", got)
	}
}

// THE COMPOSITION IS WHERE rosterbot-bms COMES BACK. The index writes straight
// into ResolvedPlayers; hydrate then indexes every person any search batch
// happened to return, including people nobody asked for. If the index's answer
// is not recorded as a claim, the zero value reads "incumbent inactive" and a
// RETIRED namesake takes the key through claimName's lower-ID tie-break —
// retired namesakes carrying the lower id is the bead's own 501447 < 514888.
func TestResolveMLBAMIDs_SearchDoesNotOverwriteAnIndexAnswer(t *testing.T) {
	lowFloor(t)
	activeAltuve := person(514888, "Jose Altuve", "Jose", "Altuve", "", true)
	retiredAltuve := person(501447, "Jose Altuve", "Jose", "Altuve", "", false)
	// "Mike Trout" is the name that goes to search; the search returns the
	// retired Altuve alongside him, exactly as a real batch returns everyone
	// matching any name in it.
	s := &statsServer{
		dumps:  map[int][]models.Person{1: {activeAltuve}},
		search: map[string][]models.Person{"Mike Trout": {mikeTrout, retiredAltuve}},
		detail: map[int]models.Person{100: mikeTrout, 501447: retiredAltuve},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Jose Altuve", "Mike Trout"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["jose altuve"]; got != 514888 {
		t.Errorf("Jose Altuve → %d, want 514888 (the index answered; search must not overwrite it)", got)
	}
}

// The other half of the same invariant, and the reason the index's answer is
// PINNED rather than merely recorded as active: a search-hydrated namesake who
// is genuinely active carries no lower standing, so claimName would fall to its
// lower-ID tie-break and take the key. The index answered a name the caller
// ASKED about, from a player who is in this season at an affiliated level; the
// person reaching hydrate merely shares a spelling and rode along in some other
// name's batch.
func TestResolveMLBAMIDs_AnActiveSearchNamesakeDoesNotDisplaceTheIndex(t *testing.T) {
	lowFloor(t)
	indexAltuve := person(514888, "Jose Altuve", "Jose", "Altuve", "", true)
	otherAltuve := person(400001, "Jose Altuve", "Jose", "Altuve", "", true)
	s := &statsServer{
		dumps:  map[int][]models.Person{1: {indexAltuve}},
		search: map[string][]models.Person{"Mike Trout": {mikeTrout, otherAltuve}},
		detail: map[int]models.Person{100: mikeTrout, 400001: otherAltuve},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Jose Altuve", "Mike Trout"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["jose altuve"]; got != 514888 {
		t.Errorf("Jose Altuve → %d, want 514888 (an index answer is pinned, not merely claimed active)", got)
	}
}

// Both legal and common spellings must resolve, or the index answers fewer names
// than the search it replaces. The second fixture row is the normalization case
// the search needs a whole second pass for: MLB's search matches "A.J. Blubaugh"
// literally and returns nothing, while a Normalize-keyed index has no such
// problem.
func TestSeasonIndex_IndexesEveryNameVariant(t *testing.T) {
	leo := person(815888, "Leo De Vries", "Leodalis", "De Vries", "Leo", true)
	blubaugh := person(805123, "AJ Blubaugh", "AJ", "Blubaugh", "", true)
	ix := buildSeasonIndex([]models.Person{leo, blubaugh})

	for _, tc := range []struct {
		name string
		want int
	}{
		{"Leo De Vries", 815888},
		{"Leodalis De Vries", 815888},
		{"A.J. Blubaugh", 805123},
	} {
		id, _, res := ix.find(tc.name)
		if res != findFound || id != tc.want {
			t.Errorf("find(%q) = %d/%v, want %d/findFound", tc.name, id, res, tc.want)
		}
	}
}

// A player matched on an uncontested key can still SHARE one of his other
// variant spellings with somebody else, and projecting that key would assert an
// answer the index has no right to give — the contested-key decline, one level
// down. Here "rob refsnyder" is his alone while "robert refsnyder" is held by
// both, so only the first may be written.
func TestResolveMLBAMIDs_DoesNotProjectAContestedVariantOfAMatchedPlayer(t *testing.T) {
	lowFloor(t)
	rob := person(900, "Rob Refsnyder", "Robert", "Refsnyder", "Rob", true)
	robert := person(901, "Robert Refsnyder", "Robert", "Refsnyder", "", true)
	s := &statsServer{dumps: map[int][]models.Person{1: {rob, robert}}}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Rob Refsnyder"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rp.ByName["rob refsnyder"]; got != 900 {
		t.Errorf("Rob Refsnyder → %d, want 900", got)
	}
	if got, ok := rp.ByName["robert refsnyder"]; ok {
		t.Errorf("contested variant \"robert refsnyder\" was projected as %d; it is held by 900 and 901", got)
	}
}

// A player appears in several sport dumps at once — Sam Antonacci 803011 is in
// sports 1 and 11 live, verified 2026-08-31. Appending him to his own key once
// per dump would leave len(ids) == 3 and stop every multi-level player
// resolving, which is most prospects.
func TestSeasonIndex_APlayerInSeveralSportDumpsDoesNotContestHisOwnKey(t *testing.T) {
	antonacci := person(803011, "Sam Antonacci", "Samuel", "Antonacci", "Sam", true)
	ix := buildSeasonIndex([]models.Person{antonacci, antonacci, antonacci})

	id, _, res := ix.find("Sam Antonacci")
	if res != findFound || id != 803011 {
		t.Errorf("find(Sam Antonacci) = %d/%v, want 803011/findFound", id, res)
	}
	if ix.players != 1 {
		t.Errorf("players = %d, want 1 (the same person in three dumps is one player)", ix.players)
	}
}

// internal/recap fans out per team at opts.Concurrency and every worker reaches
// ResolveMLBAMIDs, so the memo has to be single-flight: the lock is held ACROSS
// the five fetches, not merely around the map access. A lock narrowed to the map
// would have N workers each perform five fetches of a 1.2 MB payload and hold N
// copies of the index at once.
func TestSeasonIndex_MemoIsSingleFlightAcrossConcurrentCallers(t *testing.T) {
	lowFloor(t)
	s := &statsServer{dumps: map[int][]models.Person{1: {mikeTrout}}}
	s.start(t)

	const workers = 8
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ResolveMLBAMIDsNoCache([]string{fmt.Sprintf("Mike Trout %d", i)}); err != nil {
				t.Errorf("worker %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if n := s.dumpHits.Load(); n != int32(len(seasonIndexSports)) {
		t.Errorf("season dumps fetched %d time(s) across %d concurrent callers, want %d", n, workers, len(seasonIndexSports))
	}
}

// The disk cache has to carry the index across processes, not only across calls
// within one — a scheduled task is a fresh container every run.
func TestSeasonIndex_CachesEachSportOnDisk(t *testing.T) {
	lowFloor(t)
	s := &statsServer{dumps: map[int][]models.Person{1: {mikeTrout}}}
	s.start(t)

	dir := t.TempDir()
	if _, err := ResolveMLBAMIDs([]string{"Mike Trout"}, dir); err != nil {
		t.Fatal(err)
	}
	resetSeasonIndexMemo()
	if _, err := ResolveMLBAMIDs([]string{"Leo De Vries"}, dir); err != nil {
		t.Fatal(err)
	}

	if n := s.dumpHits.Load(); n != int32(len(seasonIndexSports)) {
		t.Errorf("season dumps fetched %d time(s) with the memo cleared, want %d (the disk cache must serve the second build)", n, len(seasonIndexSports))
	}
	for _, sport := range seasonIndexSports {
		want := fmt.Sprintf("%s-%d-%d.json", keyMLBSeasonPlayers, seasonNow(), sport)
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected cache file %s: %v", want, err)
		}
	}
}

// ResolvedPlayers is serialised into the per-name-set `mlb-player-ids-<sha8>`
// cache entry and recap-site performs ~186 resolutions per build, so projecting
// the whole index into it would turn a handful of small files into hundreds of
// megabytes — on Fargate, in the S3 cache/ prefix.
func TestResolveMLBAMIDs_ResultCarriesOnlyTheRequestedPlayers(t *testing.T) {
	lowFloor(t)
	leo := person(815888, "Leo De Vries", "Leodalis", "De Vries", "Leo", true)
	s := &statsServer{dumps: map[int][]models.Person{1: {mikeTrout, leo, thomasWhite}}}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rp.ByID) != 1 {
		t.Errorf("ByID holds %d player(s), want 1 — only the requested player is projected", len(rp.ByID))
	}
	for _, k := range []string{"leo de vries", "thomas white"} {
		if _, ok := rp.ByName[k]; ok {
			t.Errorf("unrequested key %q was projected into the result", k)
		}
	}
}

// A cleanly-fetched but partly-populated dump fails exactly the way a partial
// FETCH does, arriving by way of time instead of failure. Measured live
// 2026-08-31: season 2027 — which has not begun — returns 4,205 players against
// 5,768-5,965 for each of 2022-2026, and it DECONTESTS both known collisions
// (`thomas white` reads uncontested at 695720, `luis garcia` at 671277).
func TestSeasonIndex_RefusesAnUnderpopulatedDump(t *testing.T) {
	s := &statsServer{
		dumps:  map[int][]models.Person{1: {tommyWhite}},
		search: map[string][]models.Person{"Tommy White": {tommyWhite}},
		detail: map[int]models.Person{695720: tommyWhite},
	}
	s.start(t)

	rp, err := ResolveMLBAMIDsNoCache([]string{"Tommy White"})
	if err != nil {
		t.Fatal(err)
	}
	if s.searchHits.Load() == 0 {
		t.Error("an underpopulated dump must be refused and every name sent to the search")
	}
	if got := rp.ByName["tommy white"]; got != 695720 {
		t.Errorf("Tommy White → %d, want 695720 (via the search fallback)", got)
	}
}

// The floor is a measured constant, not a knob. Tests lower it to index a
// two-row fixture; this pins what actually ships.
func TestSeasonIndexMinPlayers_ProductionFloorIsTheMeasuredOne(t *testing.T) {
	if seasonIndexMinPlayers != 5000 {
		t.Errorf("seasonIndexMinPlayers = %d, want 5000 (13%% below the smallest completed season measured, 19%% above the pre-season dump)", seasonIndexMinPlayers)
	}
}

func hasPrefixedFile(t *testing.T, dir, prefix string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			return true
		}
	}
	return false
}
