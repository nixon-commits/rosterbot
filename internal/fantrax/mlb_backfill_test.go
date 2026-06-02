package fantrax

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
)

func TestHitterFPtsFromGame(t *testing.T) {
	// Standard league weights (from .cache/fantrax-hitter-scoring-*.json).
	w := ScoringWeights{
		"1B": 1, "2B": 2, "3B": 3, "HR": 4, "R": 1, "RBI": 1,
		"BB": 1, "SB": 4, "CS": -1, "HBP": 1, "SO": -1, "GIDP": -1,
		"XBH": 2, "TB": 1, "CYC": 50,
	}

	cases := []struct {
		name string
		game mlbGameLogDay
		want float64
	}{
		{
			name: "single",
			game: mlbGameLogDay{H: 1, R: 1, BB: 1, SB: 1, SO: 1}, // Moreno-style May 21
			// 1B=1 → +1, TB=1 → +1, R=1 → +1, BB=1 → +1, SB=1 → +4, SO=1 → -1
			want: 7,
		},
		{
			name: "double-with-rbi",
			game: mlbGameLogDay{H: 2, Doubles: 1, RBI: 1}, // Ruiz May 20
			// 1B = H - 2B - 3B - HR = 2 - 1 = 1
			// 2B = 1, XBH = 1, TB = 1*1 + 2*1 = 3
			// 1B*1 + 2B*2 + XBH*2 + TB*1 + RBI*1 = 1 + 2 + 2 + 3 + 1 = 9
			want: 9,
		},
		{
			name: "homer-with-rbi",
			game: mlbGameLogDay{H: 1, HR: 1, R: 1, RBI: 2}, // Ohtani May 20 hitting
			// 1B=0, HR=1, XBH=1, TB=4, R=1, RBI=2
			// HR*4 + XBH*2 + TB*1 + R*1 + RBI*1 = 4 + 2 + 4 + 1 + 2 = 13
			want: 13,
		},
		{
			name: "bad-game",
			game: mlbGameLogDay{AB: 3, SO: 2, GIDP: 1}, // Andujar May 20
			// SO*-1 + GIDP*-1 = -2 + -1 = -3
			want: -3,
		},
		{
			name: "zero-line",
			game: mlbGameLogDay{},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hitterFPtsFromGame(c.game, w)
			if got != c.want {
				t.Errorf("hitterFPtsFromGame() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPitcherFPtsFromGame(t *testing.T) {
	w := ScoringWeights{
		"IP": 3, "K": 1, "BB": -1, "H": -1, "ER": -2,
		"W": 5, "L": -3, "SV": 8, "HLD": 2, "QS": 8,
	}

	cases := []struct {
		name string
		game mlbGameLogDay
		want float64
	}{
		{
			name: "ohtani-may20-start",
			game: mlbGameLogDay{IP: 5, ER: 0, K: 4, BBA: 2, HA: 3, W: 1},
			// IP*3 + K*1 + BB*-1 + H*-1 + ER*-2 + W*5 = 15 + 4 - 2 - 3 + 0 + 5 = 19
			// QS gate: IP>=6 fails → no QS bonus
			want: 19,
		},
		{
			name: "quality-start",
			game: mlbGameLogDay{IP: 7, ER: 2, K: 8, BBA: 1, HA: 5, W: 1},
			// IP*3 + K*1 + BB*-1 + H*-1 + ER*-2 + W*5 + QS*8
			// = 21 + 8 - 1 - 5 - 4 + 5 + 8 = 32
			want: 32,
		},
		{
			name: "save",
			game: mlbGameLogDay{IP: 1, K: 2, SV: 1},
			// IP*3 + K*1 + SV*8 = 3 + 2 + 8 = 13
			want: 13,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pitcherFPtsFromGame(c.game, w)
			if got != c.want {
				t.Errorf("pitcherFPtsFromGame() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseInningsPitched(t *testing.T) {
	cases := []struct {
		s    string
		outs int
		want float64
	}{
		{"6.1", 0, 6 + 1.0/3.0},
		{"7.2", 0, 7 + 2.0/3.0},
		{"5.0", 0, 5},
		{"", 16, 16.0 / 3.0},
	}
	for _, c := range cases {
		got := parseInningsPitched(c.s, c.outs)
		if got != c.want {
			t.Errorf("parseInningsPitched(%q, %d) = %v, want %v", c.s, c.outs, got, c.want)
		}
	}
}

func TestComputeFPtsFromGameLog_Doubleheader(t *testing.T) {
	w := ScoringWeights{"1B": 1, "TB": 1, "R": 1}
	log := []mlbGameLogDay{
		{Date: "2026-05-23", H: 1, R: 1},                          // 1B=1 → 3 pts
		{Date: "2026-05-23", H: 1, R: 0},                          // 1B=1 → 2 pts
		{Date: "2026-05-24", H: 5, Doubles: 1, Triples: 1, HR: 1}, // wrong day, ignored
	}
	date, _ := time.Parse("2006-01-02", "2026-05-23")
	got, had := computeFPtsFromGameLog(log, date, false, w, nil)
	if !had {
		t.Fatal("HadGame should be true")
	}
	if got != 5 {
		t.Errorf("doubleheader sum = %v, want 5", got)
	}
}

func TestComputeFPtsFromGameLog_OffDay(t *testing.T) {
	log := []mlbGameLogDay{
		{Date: "2026-05-22", H: 3},
	}
	date, _ := time.Parse("2006-01-02", "2026-05-23")
	got, had := computeFPtsFromGameLog(log, date, false, ScoringWeights{"1B": 1, "TB": 1}, nil)
	if had {
		t.Error("HadGame should be false when no game on target date")
	}
	if got != 0 {
		t.Errorf("FPts on off day = %v, want 0", got)
	}
}

// gameLogServer stubs the MLB statsapi gameLog endpoint with a static JSON
// response. Used by the BackfillDailyFPts integration tests below.
func gameLogServer(t *testing.T, dateToStat map[string]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Build a stats response from the provided per-date stat lines.
		w.Header().Set("Content-Type", "application/json")
		var sb strings.Builder
		sb.WriteString(`{"stats":[{"splits":[`)
		first := true
		for date, stat := range dateToStat {
			if !first {
				sb.WriteString(",")
			}
			first = false
			sb.WriteString(`{"date":"`)
			sb.WriteString(date)
			sb.WriteString(`","stat":{`)
			fk := true
			for k, v := range stat {
				if !fk {
					sb.WriteString(",")
				}
				fk = false
				sb.WriteString(`"`)
				sb.WriteString(k)
				sb.WriteString(`":`)
				switch vv := v.(type) {
				case int:
					sb.WriteString(itoa(vv))
				case string:
					sb.WriteString(`"`)
					sb.WriteString(vv)
					sb.WriteString(`"`)
				default:
					t.Fatalf("unsupported stat type %T", v)
				}
			}
			sb.WriteString("}}")
		}
		sb.WriteString(`]}]}`)
		_, _ = w.Write([]byte(sb.String()))
	}))
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// writeScoringCache pre-populates a Client's cache directory with hitter
// and pitcher scoring weights so GetScoringWeights / GetPitcherScoringWeights
// return immediately without hitting the network.
func writeScoringCache(t *testing.T, dir, leagueID string, hitter, pitcher ScoringWeights) {
	t.Helper()
	writeCacheFile := func(key string, data any) {
		path := filepath.Join(dir, key+".json")
		b, err := json.MarshalIndent(struct {
			FetchedAt time.Time `json:"fetched_at"`
			Data      any       `json:"data"`
		}{FetchedAt: time.Now(), Data: data}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeCacheFile("fantrax-hitter-scoring-"+leagueID, hitter)
	writeCacheFile("fantrax-pitcher-scoring-"+leagueID, pitcher)
}

// newTestBackfillClient constructs a Client with the bare minimum state the
// backfill needs: a cache dir and a league ID. Auth/public clients are not
// initialized — backfill doesn't call them.
func newTestBackfillClient(t *testing.T, dir, leagueID string) *Client {
	t.Helper()
	c := &Client{leagueID: leagueID}
	c.SetCache(dir)
	return c
}

func TestBackfillDailyFPts_NewPlayerHitter(t *testing.T) {
	dir := t.TempDir()
	leagueID := "TESTLG"
	hitterW := ScoringWeights{
		"1B": 1, "2B": 2, "TB": 1, "XBH": 2,
		"R": 1, "RBI": 1, "BB": 1, "SB": 4, "SO": -1, "GIDP": -1, "HBP": 1,
	}
	writeScoringCache(t, dir, leagueID, hitterW, ScoringWeights{})

	// Stub MLB game log server returning Moreno's actual May 21 line.
	srv := gameLogServer(t, map[string]map[string]any{
		"2026-05-21": {"hits": 1, "runs": 1, "baseOnBalls": 1, "stolenBases": 1, "strikeOuts": 1},
	})
	defer srv.Close()

	prevURL := mlbBackfillGameLogURL
	mlbBackfillGameLogURL = srv.URL + "/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"
	defer func() { mlbBackfillGameLogURL = prevURL }()

	prevResolver := resolveBackfillNames
	resolveBackfillNames = func(names []string, _ string) (*playername.ResolvedPlayers, error) {
		rp := &playername.ResolvedPlayers{
			ByName: map[string]int{playername.Normalize("Gabriel Moreno"): 672515},
			ByID:   map[int]string{672515: "Gabriel Moreno"},
		}
		return rp, nil
	}
	defer func() { resolveBackfillNames = prevResolver }()

	c := newTestBackfillClient(t, dir, leagueID)

	date, _ := time.Parse("2006-01-02", "2026-05-21")
	days := []DayRoster{
		{Date: date, Players: []DayPlayerFP{
			{PlayerID: "p1", Name: "Gabriel Moreno", FPts: 0, Active: true, IsPitcher: false, NeedsBackfill: true},
		}},
	}

	if err := c.BackfillDailyFPts(days); err != nil {
		t.Fatalf("BackfillDailyFPts: %v", err)
	}

	got := days[0].Players[0]
	// 1B=1 → +1, TB=1 → +1, R=1 → +1, BB=1 → +1, SB=1 → +4, SO=1 → -1 = 7
	if got.FPts != 7 {
		t.Errorf("FPts = %v, want 7", got.FPts)
	}
	if !got.HadGame {
		t.Error("HadGame should be true after backfill")
	}
	if got.NeedsBackfill {
		t.Error("NeedsBackfill should be cleared after successful backfill")
	}
}

func TestBackfillDailyFPts_TwoWayPitchingCross(t *testing.T) {
	dir := t.TempDir()
	leagueID := "TESTLG"
	pitcherW := ScoringWeights{
		"IP": 3, "K": 1, "BB": -1, "H": -1, "ER": -2, "W": 5,
	}
	writeScoringCache(t, dir, leagueID, ScoringWeights{}, pitcherW)

	// Ohtani's actual May 20 pitching: 5 IP, 0 ER, 4 K, 2 BB, 3 H, 1 W → 19 FPts.
	srv := gameLogServer(t, map[string]map[string]any{
		"2026-05-20": {"inningsPitched": "5.0", "earnedRuns": 0, "strikeOuts": 4, "baseOnBalls": 2, "hits": 3, "wins": 1},
	})
	defer srv.Close()

	prevURL := mlbBackfillGameLogURL
	mlbBackfillGameLogURL = srv.URL + "/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"
	defer func() { mlbBackfillGameLogURL = prevURL }()

	prevResolver := resolveBackfillNames
	resolveBackfillNames = func(names []string, _ string) (*playername.ResolvedPlayers, error) {
		return &playername.ResolvedPlayers{
			ByName: map[string]int{playername.Normalize("Shohei Ohtani"): 660271},
			ByID:   map[int]string{660271: "Shohei Ohtani"},
		}, nil
	}
	defer func() { resolveBackfillNames = prevResolver }()

	c := newTestBackfillClient(t, dir, leagueID)

	date, _ := time.Parse("2006-01-02", "2026-05-20")
	days := []DayRoster{
		{Date: date, Players: []DayPlayerFP{
			{PlayerID: "ohtani", Name: "Shohei Ohtani", FPts: 0, Active: true, IsPitcher: true, NeedsBackfill: true},
		}},
	}

	if err := c.BackfillDailyFPts(days); err != nil {
		t.Fatalf("BackfillDailyFPts: %v", err)
	}

	got := days[0].Players[0]
	if got.FPts != 19 {
		t.Errorf("FPts = %v, want 19 (Ohtani pitching May 20)", got.FPts)
	}
	if got.NeedsBackfill {
		t.Error("NeedsBackfill should be cleared")
	}
}

func TestBackfillDailyFPts_OffDay(t *testing.T) {
	dir := t.TempDir()
	leagueID := "TESTLG"
	writeScoringCache(t, dir, leagueID, ScoringWeights{"1B": 1, "TB": 1}, ScoringWeights{})

	// Game log has a game on a DIFFERENT date — the target date is an off day.
	srv := gameLogServer(t, map[string]map[string]any{
		"2026-05-19": {"hits": 3},
	})
	defer srv.Close()
	prevURL := mlbBackfillGameLogURL
	mlbBackfillGameLogURL = srv.URL + "/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"
	defer func() { mlbBackfillGameLogURL = prevURL }()

	prevResolver := resolveBackfillNames
	resolveBackfillNames = func(names []string, _ string) (*playername.ResolvedPlayers, error) {
		return &playername.ResolvedPlayers{
			ByName: map[string]int{playername.Normalize("OffDay Joe"): 1},
		}, nil
	}
	defer func() { resolveBackfillNames = prevResolver }()

	c := newTestBackfillClient(t, dir, leagueID)
	date, _ := time.Parse("2006-01-02", "2026-05-21")
	days := []DayRoster{
		{Date: date, Players: []DayPlayerFP{
			{PlayerID: "x", Name: "OffDay Joe", FPts: 0, Active: true, NeedsBackfill: true},
		}},
	}

	if err := c.BackfillDailyFPts(days); err != nil {
		t.Fatalf("BackfillDailyFPts: %v", err)
	}

	got := days[0].Players[0]
	if got.FPts != 0 {
		t.Errorf("FPts on off day = %v, want 0", got.FPts)
	}
	if got.HadGame {
		t.Error("HadGame should be false on a legitimate off day")
	}
	if got.NeedsBackfill {
		t.Error("NeedsBackfill should be cleared (backfill succeeded — answer is genuinely zero)")
	}
}

func TestBackfillDailyFPts_NameResolveMiss(t *testing.T) {
	dir := t.TempDir()
	leagueID := "TESTLG"
	writeScoringCache(t, dir, leagueID, ScoringWeights{"1B": 1}, ScoringWeights{})

	prevResolver := resolveBackfillNames
	// Resolver returns empty map — no match.
	resolveBackfillNames = func(names []string, _ string) (*playername.ResolvedPlayers, error) {
		return &playername.ResolvedPlayers{ByName: map[string]int{}, ByID: map[int]string{}}, nil
	}
	defer func() { resolveBackfillNames = prevResolver }()

	c := newTestBackfillClient(t, dir, leagueID)
	date, _ := time.Parse("2006-01-02", "2026-05-21")
	days := []DayRoster{
		{Date: date, Players: []DayPlayerFP{
			{PlayerID: "x", Name: "Unknown Player", FPts: 0, Active: true, NeedsBackfill: true},
		}},
	}

	if err := c.BackfillDailyFPts(days); err != nil {
		t.Fatalf("BackfillDailyFPts should soft-fail, got error: %v", err)
	}

	got := days[0].Players[0]
	if got.FPts != 0 {
		t.Errorf("unresolved row FPts should stay 0, got %v", got.FPts)
	}
	if !got.NeedsBackfill {
		t.Error("NeedsBackfill should remain true on hard failure (resolver miss)")
	}
}

func TestBackfillDailyFPts_NoTargets(t *testing.T) {
	c := newTestBackfillClient(t, t.TempDir(), "TESTLG")
	date, _ := time.Parse("2006-01-02", "2026-05-21")
	days := []DayRoster{
		{Date: date, Players: []DayPlayerFP{
			{PlayerID: "x", Name: "Healthy", FPts: 10, Active: true, NeedsBackfill: false},
		}},
	}
	if err := c.BackfillDailyFPts(days); err != nil {
		t.Fatalf("BackfillDailyFPts with no targets should be a no-op, got: %v", err)
	}
	if days[0].Players[0].FPts != 10 {
		t.Error("non-flagged rows should be untouched")
	}
}
