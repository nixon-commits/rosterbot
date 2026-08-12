// Package opsalert decides whether a finished job run is worth waking an
// operator for, and renders the message when it is.
//
// It is a stdlib-only leaf on purpose. The only consumer is the opsnotify
// Lambda, and internal/lineupapi — where the run ledger's wire types live —
// transitively pulls internal/fantrax and therefore chromedp. A notification
// Lambda has no business carrying a headless browser, so Record redeclares the
// five fields this package needs and contract_test.go guards the duplication.
//
// Everything here is pure: the Lambda fetches, opsalert judges.
package opsalert

import "time"

// Ledger record statuses, as written by the run-ledger command in entrypoint.sh.
const (
	StatusRunning = "RUNNING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
)

// Record is the subset of a run ledger record the alerting logic needs. The
// json tags mirror internal/lineupapi.RunDetail's wire contract.
//
// StartedAt is RFC3339 UTC and is what the heartbeat check reads to answer "when
// did this job last launch?" — see heartbeat.go. Streak ignores it: a streak is
// ordering, and the ledger's inverted-timestamp keys already deliver records in
// order, so parsing a timestamp to re-derive that would only add a failure mode.
//
// UserID is which tenant's run this was, and is empty for every record written
// before per-tenant fan-out. Both decisions in this package key on
// (Command, UserID) rather than on Command alone, because under fan-out N
// tenants run the *same command string* and a command-only key silently
// inverts: see the Streak and Overdue docs. The empty value is a tenant like
// any other, so the whole pre-fan-out ledger keeps grading as the single tenant
// it describes.
type Record struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	UserID    string `json:"user_id,omitempty"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	LogTail   string `json:"log_tail,omitempty"`
}

// Started parses StartedAt. The zero time means the record carries no usable
// timestamp, which every caller must treat as "unknown", never as "the epoch".
func (r Record) Started() time.Time {
	t, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Kind is what, if anything, the operator should be told.
type Kind int

const (
	// None means stay quiet: a mid-streak repeat, or an ordinary success.
	None Kind = iota
	// Started is the first failure after a success — the outage begins.
	Started
	// Escalated is the EscalateAt'th consecutive failure — this is not a blip.
	Escalated
	// Recovered is the first success after one or more failures.
	Recovered
)

func (k Kind) String() string {
	switch k {
	case Started:
		return "Started"
	case Escalated:
		return "Escalated"
	case Recovered:
		return "Recovered"
	default:
		return "None"
	}
}

// EscalateAt is the streak length that earns a second push. Three hourly runs
// is roughly two hours of the lineup hot path down — past any plausible
// transient. Streak fires on equality, not on >=, so an eleven-failure outage
// escalates exactly once.
const EscalateAt = 3

// Verdict is the decision for one (command, tenant). Failure is the record that
// triggered a Started or Escalated verdict, so the caller can quote its log
// tail; it is nil for None and Recovered.
type Verdict struct {
	Kind    Kind
	Command string
	UserID  string
	Streak  int
	Failure *Record
}

// Streak decides what to say about the newest run of command for tenant userID,
// given recs in newest-first order (the run ledger's inverted-timestamp keys
// list that way natively).
//
// The history is keyed on (command, userID), not on command alone. Under
// per-tenant fan-out every tenant runs the same command string into the same
// ledger, so a command-only history interleaves them — and since a SUCCESS
// breaks the streak, a tenant failing *every* run grades as healthy on its
// neighbours' successes. That failure is silent by construction: the code
// compiles, runs, and reports green.
//
// userID is empty for every record written before fan-out, and empty is treated
// as an ordinary tenant value rather than a wildcard, so the existing ledger
// grades exactly as it did. During cutover a mixed ledger holds both, and the
// two histories stay separate in both directions.
//
// Only terminal records count. RUNNING is the start-of-run write that the
// end-of-run write later overwrites at the same key, so an in-flight sibling
// run must not be mistaken for a break in the streak.
//
// The whole decision is derived from the ledger, which is why there is no
// counter object to keep consistent and no cooldown to tune: streak
// transitions deduplicate themselves.
func Streak(recs []Record, command, userID string) Verdict {
	var hist []Record
	for _, r := range recs {
		if r.Command != command || r.UserID != userID {
			continue
		}
		if r.Status != StatusSuccess && r.Status != StatusFailed {
			continue
		}
		hist = append(hist, r)
	}
	none := Verdict{Command: command, UserID: userID}
	if len(hist) == 0 {
		return none
	}

	if hist[0].Status == StatusSuccess {
		n := leadingFailures(hist[1:])
		if n == 0 {
			return none
		}
		return Verdict{Kind: Recovered, Command: command, UserID: userID, Streak: n}
	}

	n := leadingFailures(hist)
	v := Verdict{Command: command, UserID: userID, Streak: n, Failure: &hist[0]}
	switch n {
	case 1:
		v.Kind = Started
	case EscalateAt:
		v.Kind = Escalated
	}
	return v
}

// leadingFailures counts consecutive FAILED records from the front of recs.
func leadingFailures(recs []Record) int {
	n := 0
	for _, r := range recs {
		if r.Status != StatusFailed {
			break
		}
		n++
	}
	return n
}
