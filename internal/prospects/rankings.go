package prospects

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
)

// fgProspectURL is a var so tests can override it.
// The draft param format is "{season}prospect" for current-year report.
var fgProspectURL = "https://www.fangraphs.com/api/prospects/board/data?draft=%dprospect&season=%d"

// ErrSourceUnavailable indicates a ranking source is temporarily unavailable
// (e.g. 401/403). ChainedRankingSource uses this to fall through to the next source.
var ErrSourceUnavailable = errors.New("ranking source unavailable")

// ---------------------------------------------------------------------------
// 1. FanGraphsRankingSource (primary)
// ---------------------------------------------------------------------------

// FanGraphsRankingSource fetches prospect rankings from The Board on FanGraphs.
// Free endpoint, no auth required.
type FanGraphsRankingSource struct{}

func (s *FanGraphsRankingSource) GetTopProspects(season int) ([]RankedProspect, error) {
	url := fmt.Sprintf(fgProspectURL, season, season)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fangraphs prospects fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("fangraphs prospects: HTTP %d — authentication required: %w", resp.StatusCode, ErrSourceUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fangraphs prospects: status %d", resp.StatusCode)
	}

	var rows []struct {
		PlayerName string `json:"playerName"`
		Team       string `json:"Team"`
		Position   string `json:"Position"`
		OvrRank    int    `json:"Ovr_Rank"`
		FV         int    `json:"FV_Current"`
		ETA        int    `json:"ETA_Current"`
		Level      string `json:"mlevel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("fangraphs prospects json: %w", err)
	}

	result := make([]RankedProspect, 0, len(rows))
	for _, row := range rows {
		if row.OvrRank == 0 {
			continue // unranked in the overall list
		}
		pos := strings.TrimSpace(row.Position)
		eta := ""
		if row.ETA > 0 {
			eta = strconv.Itoa(row.ETA)
		}
		result = append(result, RankedProspect{
			Name:      row.PlayerName,
			MLBTeam:   projections.NormalizeTeam(row.Team),
			Position:  pos,
			Rank:      row.OvrRank,
			FV:        row.FV,
			ETA:       eta,
			Level:     row.Level,
			IsPitcher: isPitcherPosition(pos),
		})
	}

	// Sort by rank ascending (FG data may not be pre-sorted).
	sort.Slice(result, func(i, j int) bool {
		return result[i].Rank < result[j].Rank
	})

	return result, nil
}

func isPitcherPosition(pos string) bool {
	switch pos {
	case "SP", "RP", "P":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 2. FantraxRankingSource (fallback)
// ---------------------------------------------------------------------------

// FantraxRankingSource ranks minors-eligible players from the Fantrax player pool
// by %Rostered descending. Used as fallback when FanGraphs is unavailable.
type FantraxRankingSource struct {
	Client *fantrax.Client
}

func (s *FantraxRankingSource) GetTopProspects(season int) ([]RankedProspect, error) {
	pool, err := s.Client.GetMinorsEligiblePool()
	if err != nil {
		return nil, fmt.Errorf("fantrax prospect pool: %w", err)
	}

	// Sort by %Rostered descending (most rostered = highest ranked prospect).
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].PercentRostered != pool[j].PercentRostered {
			return pool[i].PercentRostered > pool[j].PercentRostered
		}
		return pool[i].FantasyPtsPerG > pool[j].FantasyPtsPerG
	})

	limit := 100
	if len(pool) < limit {
		limit = len(pool)
	}

	result := make([]RankedProspect, 0, limit)
	for i := 0; i < limit; i++ {
		p := pool[i]
		if p.PercentRostered == 0 && p.FantasyPtsPerG == 0 {
			break
		}
		pos := p.PosShortNames
		result = append(result, RankedProspect{
			Name:      projections.NormalizeName(p.Name),
			MLBTeam:   projections.NormalizeTeam(p.MLBTeam),
			Position:  pos,
			Rank:      i + 1,
			IsPitcher: isPitcherPosition(pos),
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// 3. ChainedRankingSource
// ---------------------------------------------------------------------------

// ChainedRankingSource tries multiple RankingSources in order, falling through
// when a source returns ErrSourceUnavailable.
type ChainedRankingSource struct {
	sources []RankingSource
}

// NewChainedRankingSource creates a chained source that tries each delegate in order.
func NewChainedRankingSource(sources ...RankingSource) *ChainedRankingSource {
	return &ChainedRankingSource{sources: sources}
}

func (c *ChainedRankingSource) GetTopProspects(season int) ([]RankedProspect, error) {
	for _, src := range c.sources {
		prospects, err := src.GetTopProspects(season)
		if err != nil {
			if errors.Is(err, ErrSourceUnavailable) {
				continue
			}
			return nil, err
		}
		return prospects, nil
	}
	return nil, fmt.Errorf("all ranking sources failed")
}

// ---------------------------------------------------------------------------
// 4. Cache helpers
// ---------------------------------------------------------------------------

var rankingsCacheFile = ".prospects-cache/rankings.json"

type rankingsCache struct {
	FetchedAt time.Time        `json:"fetched_at"`
	Prospects []RankedProspect `json:"prospects"`
}

func loadRankingsCache(maxAge time.Duration) []RankedProspect {
	data, err := os.ReadFile(rankingsCacheFile)
	if err != nil {
		return nil
	}
	var c rankingsCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if time.Since(c.FetchedAt) > maxAge {
		return nil
	}
	return c.Prospects
}

func saveRankingsCache(prospects []RankedProspect) error {
	dir := filepath.Dir(rankingsCacheFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	c := rankingsCache{
		FetchedAt: time.Now(),
		Prospects: prospects,
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(rankingsCacheFile, data, 0o644)
}

// ---------------------------------------------------------------------------
// 5. LoadRankings
// ---------------------------------------------------------------------------

// LoadRankings returns prospect rankings, using a cache when fresh.
func LoadRankings(source RankingSource, season int, cacheHours int) ([]RankedProspect, error) {
	maxAge := time.Duration(cacheHours) * time.Hour
	if cached := loadRankingsCache(maxAge); cached != nil {
		return cached, nil
	}

	prospects, err := source.GetTopProspects(season)
	if err != nil {
		return nil, err
	}

	if err := saveRankingsCache(prospects); err != nil {
		// Non-fatal: log but don't fail
		fmt.Fprintf(os.Stderr, "warning: failed to save rankings cache: %v\n", err)
	}

	return prospects, nil
}

// ---------------------------------------------------------------------------
// 6. FindUpgrades
// ---------------------------------------------------------------------------

// upgradeThreshold returns the minimum rank gap needed for a given rostered rank.
func upgradeThreshold(rank int) int {
	switch {
	case rank == 0:
		return 1 // unranked: any ranked FA is an upgrade
	case rank <= 10:
		return 5
	case rank <= 50:
		return 15
	default:
		return 25
	}
}

// FindUpgrades compares rostered prospects against available free agents and
// returns recommended swaps. Each rostered player appears at most once, paired
// with the best available FA that meets the tiered threshold.
func FindUpgrades(rostered, available []RankedProspect, currentYear string) []UpgradeCandidate {
	if len(rostered) == 0 || len(available) == 0 {
		return nil
	}

	currentYearInt, _ := strconv.Atoi(currentYear)
	nextYear := strconv.Itoa(currentYearInt + 1)

	var upgrades []UpgradeCandidate

	for _, drop := range rostered {
		threshold := upgradeThreshold(drop.Rank)
		var bestFA *RankedProspect
		var bestGap int

		for i := range available {
			add := &available[i]
			if add.Rank == 0 {
				continue // unranked FA is not an upgrade
			}

			var gap int
			if drop.Rank == 0 {
				// Unranked rostered: any ranked FA is an upgrade
				gap = add.Rank // use rank as proxy (lower is better, so we want a positive signal)
				// For unranked, gap = 101 - add.Rank to make higher ranked better
				gap = 101 - add.Rank
			} else {
				gap = drop.Rank - add.Rank
			}

			if gap < threshold {
				continue
			}

			// FV-based comparison: when both have FV > 0, a gap of ≥5 FV points is significant
			if drop.FV > 0 && add.FV > 0 && add.FV-drop.FV >= 5 {
				// FV upgrade — always prefer
				if bestFA == nil || add.Rank < bestFA.Rank {
					cp := *add
					bestFA = &cp
					bestGap = gap
				}
				continue
			}

			if bestFA == nil || add.Rank < bestFA.Rank {
				cp := *add
				bestFA = &cp
				bestGap = gap
			}
		}

		if bestFA != nil {
			nearTerm := bestFA.ETA == currentYear || bestFA.ETA == nextYear
			upgrades = append(upgrades, UpgradeCandidate{
				Drop:     drop,
				Add:      *bestFA,
				RankGap:  bestGap,
				NearTerm: nearTerm,
			})
		}
	}

	// Sort by rank gap descending
	sort.Slice(upgrades, func(i, j int) bool {
		return upgrades[i].RankGap > upgrades[j].RankGap
	})

	return upgrades
}
