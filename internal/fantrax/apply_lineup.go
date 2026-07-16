package fantrax

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"

	"github.com/nixon-commits/rosterbot/internal/playername"
)

// rosterChangeExecutor is the API call applyLineupWithLockedPlayerRetry uses
// to POST a fieldMap. Stored as a function value so tests can inject a fake
// without the auth_client's hardcoded fantrax.com URL.
type rosterChangeExecutor func(map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error)

// rosterFetcher re-fetches the current roster for post-retry verification.
// Stored as a function value (like rosterChangeExecutor) so tests can inject a
// fake without the auth_client's hardcoded fantrax.com URL. A nil fetcher
// disables verification (used by tests that don't exercise that path).
type rosterFetcher func() (*models.TeamRosterResponse, error)

// lockedPlayerLink matches one <a class="hand"...>NAME</a> tag inside the
// "already locked in this period" rejection message. Capture group 1 is the
// raw player name.
var lockedPlayerLink = regexp.MustCompile(`<a [^>]*class="hand[^"]*"[^>]*>([^<]+)</a>`)

const lockedPrefix = "already locked in this period"

// parseLockedPlayerNames extracts player names from a Fantrax locked-player
// rejection message. Returns nil for messages that don't match the pattern.
func parseLockedPlayerNames(msg string) []string {
	if !strings.Contains(msg, lockedPrefix) {
		return nil
	}
	matches := lockedPlayerLink.FindAllStringSubmatch(msg, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSpace(m[1]))
	}
	return names
}

// applyLineupWithLockedPlayerRetry posts roster changes via executor, retrying
// once after a locked-player rejection with those players excluded from the
// payload. Fantrax atomically rejects an entire fieldMap if any one player
// in it is per-player-locked, so a cosmetic swap involving a locked player
// would otherwise drop every queued change in the run.
//
// Cap of one retry — if the second attempt still trips the same rejection
// (parser missed a name, name normalization mismatch), the function returns
// the error rather than looping. Callers should log and continue rather than
// exit so the rest of the day's work surfaces.
func applyLineupWithLockedPlayerRetry(
	executor rosterChangeExecutor,
	fetch rosterFetcher,
	rawRoster *models.TeamRosterResponse,
	active []PlayerSlot,
	reserve []string,
) error {
	nameToID := buildNameToID(rawRoster)
	excluded := make(map[string]bool)

	const maxAttempts = 2
	var lastMsg string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		fieldMap := buildFieldMapWithMods(rawRoster, active, reserve, excluded)

		resp, err := executor(fieldMap)
		if err != nil {
			return fmt.Errorf("apply roster changes: %w", err)
		}

		// Future-period flow: a "Please confirm" prompt requires re-issuing
		// the same fieldMap to commit. Both calls share the same attempt slot
		// since the prompt itself is not a rejection.
		msg := mainMsg(resp)
		if isConfirmPrompt(msg) {
			resp, err = executor(fieldMap)
			if err != nil {
				return fmt.Errorf("confirm roster changes: %w", err)
			}
			msg = mainMsg(resp)
			if msg == "" {
				return verifyExcludedRetry(fetch, active, reserve, excluded)
			}
		}

		if msg == "" || isBenignNoChangeMsg(msg) {
			return verifyExcludedRetry(fetch, active, reserve, excluded)
		}

		lastMsg = msg
		if attempt+1 < maxAttempts {
			if names := parseLockedPlayerNames(msg); len(names) > 0 {
				added := excludeByName(names, nameToID, excluded)
				log.Printf("fantrax: locked players (%v) — retrying without %d player(s)", names, added)
				continue
			}
		}

		return fmt.Errorf("roster change rejected: %s", msg)
	}

	if lastMsg != "" {
		return fmt.Errorf("roster change rejected after retry: %s", lastMsg)
	}
	return nil
}

// verifyExcludedRetry re-fetches the roster and confirms every non-excluded
// intended change actually landed. Only meaningful after a locked-player retry
// (excluded non-empty): Fantrax atomically rejects the first attempt, but the
// retry payload — with the locked players dropped — can come back benign while
// the remaining changes silently fail to land (e.g. the whole submission
// targeted an already-closed period, the rosterbot-48z incident). A clean apply
// with no exclusions is trusted as-is — Fantrax's own atomicity guarantees it —
// so the happy path skips the extra roster round-trip entirely.
//
// Soft-fails on a re-fetch error: the apply itself reported success, so a
// verify-only hiccup is logged as "outcome unconfirmed" rather than promoted to
// a hard failure. A genuine mismatch (a change that didn't take effect) returns
// an error naming the unmet players so the caller surfaces it (log + Pushover).
func verifyExcludedRetry(
	fetch rosterFetcher,
	active []PlayerSlot,
	reserve []string,
	excluded map[string]bool,
) error {
	if fetch == nil || len(excluded) == 0 {
		return nil
	}
	roster, err := fetch()
	if err != nil {
		log.Printf("fantrax: post-retry verify skipped — roster re-fetch failed: %v", err)
		return nil
	}
	actual := auth_client.BuildFieldMapFromRoster(roster)

	var unmet []string
	for _, ps := range active {
		if excluded[ps.PlayerID] {
			continue
		}
		got := actual[ps.PlayerID]
		if got.StID != auth_client.StatusActive || got.PosID != ps.PosID {
			unmet = append(unmet, fmt.Sprintf("%s→active(%s) but st=%q pos=%q",
				ps.PlayerID, ps.PosID, got.StID, got.PosID))
		}
	}
	for _, id := range reserve {
		if excluded[id] {
			continue
		}
		if actual[id].StID != auth_client.StatusReserve {
			unmet = append(unmet, fmt.Sprintf("%s→reserve but st=%q", id, actual[id].StID))
		}
	}
	if len(unmet) > 0 {
		return fmt.Errorf("post-retry verify failed — %d intended change(s) did not land: %s",
			len(unmet), strings.Join(unmet, "; "))
	}
	return nil
}

// buildNameToID extracts a player-name → ID map from a parsed roster.
// Names are normalized so error-message names (e.g. "Nico Hoerner") match
// roster names regardless of accent / punctuation differences.
func buildNameToID(rawRoster *models.TeamRosterResponse) map[string]string {
	m := make(map[string]string)
	if rawRoster == nil || len(rawRoster.Responses) == 0 {
		return m
	}
	for _, table := range rawRoster.Responses[0].Data.Tables {
		for _, row := range table.Rows {
			if row.Scorer.ScorerID == "" || row.Scorer.Name == "" {
				continue
			}
			m[playername.Normalize(row.Scorer.Name)] = row.Scorer.ScorerID
		}
	}
	return m
}

// buildFieldMapWithMods rebuilds the fieldMap from the original roster and
// applies the requested active/reserve modifications, skipping any player IDs
// in `excluded`. Excluded players keep their original status — Fantrax sees
// them as unchanged so the locked check passes.
func buildFieldMapWithMods(
	rawRoster *models.TeamRosterResponse,
	active []PlayerSlot,
	reserve []string,
	excluded map[string]bool,
) map[string]auth_client.RosterPosition {
	fieldMap := auth_client.BuildFieldMapFromRoster(rawRoster)
	for _, ps := range active {
		if excluded[ps.PlayerID] {
			continue
		}
		pos := fieldMap[ps.PlayerID]
		pos.StID = auth_client.StatusActive
		pos.PosID = ps.PosID
		fieldMap[ps.PlayerID] = pos
	}
	for _, id := range reserve {
		if excluded[id] {
			continue
		}
		pos := fieldMap[id]
		pos.StID = auth_client.StatusReserve
		pos.PosID = ""
		fieldMap[id] = pos
	}
	return fieldMap
}

// excludeByName looks up each name in nameToID and adds the corresponding
// player ID to excluded. Returns how many were successfully excluded.
func excludeByName(names []string, nameToID map[string]string, excluded map[string]bool) int {
	added := 0
	for _, name := range names {
		id, ok := nameToID[playername.Normalize(name)]
		if !ok {
			continue
		}
		if !excluded[id] {
			excluded[id] = true
			added++
		}
	}
	return added
}

func mainMsg(resp *models.RosterChangeResponse) string {
	if resp == nil || len(resp.Responses) == 0 {
		return ""
	}
	return resp.Responses[0].Data.FantasyResponse.MainMsg
}

func isConfirmPrompt(msg string) bool {
	return msg != "" && strings.Contains(msg, "Please confirm")
}

func isBenignNoChangeMsg(msg string) bool {
	if strings.Contains(msg, "no changes detected") {
		return true
	}
	if strings.Contains(strings.ToLower(msg), "same lineup") {
		return true
	}
	return false
}
