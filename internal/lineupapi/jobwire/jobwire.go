// Package jobwire holds the per-job wire result types (the {type, data}
// payloads behind GET /v1/runs/{id}/output) and the RecordOutput hook the job
// packages publish them through.
//
// A ZERO-DEPENDENCY LEAF, on the statestore/layout precedent, and the
// dependency direction is the whole point: prospects, transactions, waivers,
// gscheck and claims used to import internal/lineupapi — the tree's biggest,
// most security-sensitive package (WebAuthn ceremonies, session cookies,
// tenant admin) — solely for these structs and this hook. Both sides now
// import this leaf instead, so a job adding a wire field no longer edits
// lineupapi and its review/blame stays with the job.
// TestProducersDoNotImportLineupapi pins the direct-import direction; the
// producers still reach lineupapi transitively through internal/notify's
// sinks, a separate documented coupling that is notify's to own.
//
// The JSON tags are wire contract twice over: the iOS client decodes them,
// and the dashboard's run viewer is a generic JSON-to-DOM renderer that
// prints object keys verbatim as table headers — the json names ARE the
// labels a reader sees.
package jobwire

// OutputRecorder is a nil-safe global hook (mirrors notify.Recorder). cmd sets
// it to a closure that marshals the {type,data} envelope and writes it under
// the RUN_ID env var. Jobs call RecordOutput; when the hook is unset (tests,
// local runs without a run id) the call is a no-op, so nothing else has to
// change.
var OutputRecorder func(jobType string, data any)

// RecordOutput hands a job's typed result to the installed recorder. Safe to
// call unconditionally; no-op when no recorder is installed.
func RecordOutput(jobType string, data any) {
	if OutputRecorder != nil {
		OutputRecorder(jobType, data)
	}
}
