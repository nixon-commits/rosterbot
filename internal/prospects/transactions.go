package prospects

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
)

var mlbTransactionsURL = "https://statsapi.mlb.com/api/v1/transactions?startDate=%s&endDate=%s"

// FetchTransactionAlerts fetches MLB transactions and cross-references against
// your Minors roster and ranked prospects to produce alerts.
// myMinors: normalized name -> true for players on your Minors roster.
// rankings: normalized name -> rank (1-100) for ranked prospects.
func FetchTransactionAlerts(
	from, to time.Time,
	myMinors map[string]bool,
	rankings map[string]int,
) ([]ProspectAlert, error) {
	url := fmt.Sprintf(mlbTransactionsURL, from.Format("2006-01-02"), to.Format("2006-01-02"))

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mlb transactions fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlb transactions: status %d", resp.StatusCode)
	}

	var payload struct {
		Transactions []struct {
			Person struct {
				FullName string `json:"fullName"`
			} `json:"person"`
			ToTeam struct {
				Abbreviation string `json:"abbreviation"`
			} `json:"toTeam"`
			TypeCode string `json:"typeCode"`
			Date     string `json:"date"`
		} `json:"transactions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb transactions decode: %w", err)
	}

	var alerts []ProspectAlert
	for _, txn := range payload.Transactions {
		name := projections.NormalizeName(txn.Person.FullName)
		team := projections.NormalizeTeam(txn.ToTeam.Abbreviation)
		rank := rankings[name]

		switch txn.TypeCode {
		case "CU", "RET": // Called up / Recalled
			if myMinors[name] {
				alerts = append(alerts, ProspectAlert{
					Kind:       CalledUp,
					Priority:   "high",
					PlayerName: txn.Person.FullName,
					MLBTeam:    team,
					Detail:     "Called up — move from Minors slot",
					OnMyTeam:   true,
					Rank:       rank,
				})
			} else if rank > 0 {
				alerts = append(alerts, ProspectAlert{
					Kind:       FreeAgentBuzz,
					Priority:   "high",
					PlayerName: txn.Person.FullName,
					MLBTeam:    team,
					Detail:     fmt.Sprintf("#%d prospect called up — available in your league?", rank),
					Rank:       rank,
				})
			}

		case "OPT", "DFA": // Optioned / DFA
			if myMinors[name] {
				alerts = append(alerts, ProspectAlert{
					Kind:       Optioned,
					Priority:   "low",
					PlayerName: txn.Person.FullName,
					MLBTeam:    team,
					Detail:     fmt.Sprintf("Optioned/DFA (%s)", txn.TypeCode),
					OnMyTeam:   true,
					Rank:       rank,
				})
			}
		}
	}

	return alerts, nil
}
