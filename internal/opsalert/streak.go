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

// Ledger record statuses, as written by the run-ledger command in entrypoint.sh.
const (
	StatusRunning = "RUNNING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
)

// Record is the subset of a run ledger record the alerting logic needs. The
// json tags mirror internal/lineupapi.RunDetail's wire contract.
type Record struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	LogTail  string `json:"log_tail,omitempty"`
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

// Verdict is the decision for one command. Failure is the record that triggered
// a Started or Escalated verdict, so the caller can quote its log tail; it is
// nil for None and Recovered.
type Verdict struct {
	Kind    Kind
	Command string
	Streak  int
	Failure *Record
}

// Streak decides what to say about the newest run of command, given recs in
// newest-first order (the run ledger's inverted-timestamp keys list that way
// natively).
//
// Only terminal records count. RUNNING is the start-of-run write that the
// end-of-run write later overwrites at the same key, so an in-flight sibling
// run must not be mistaken for a break in the streak.
//
// The whole decision is derived from the ledger, which is why there is no
// counter object to keep consistent and no cooldown to tune: streak
// transitions deduplicate themselves.
func Streak(recs []Record, command string) Verdict {
	var hist []Record
	for _, r := range recs {
		if r.Command != command {
			continue
		}
		if r.Status != StatusSuccess && r.Status != StatusFailed {
			continue
		}
		hist = append(hist, r)
	}
	if len(hist) == 0 {
		return Verdict{Command: command}
	}

	if hist[0].Status == StatusSuccess {
		n := leadingFailures(hist[1:])
		if n == 0 {
			return Verdict{Command: command}
		}
		return Verdict{Kind: Recovered, Command: command, Streak: n}
	}

	n := leadingFailures(hist)
	v := Verdict{Command: command, Streak: n, Failure: &hist[0]}
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
