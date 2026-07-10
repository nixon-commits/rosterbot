# Triage Labels

The skills speak in terms of five canonical **state** roles plus a **category** role
(bug/enhancement). This file maps those onto **bd (beads)**, this repo's actual issue
tracker (see CLAUDE.md's "Beads Issue Tracker" section) — not the `.scratch/` markdown
convention `issue-tracker.md` still describes, which predates the bd migration.

## State role → bd state dimension

bd has a purpose-built primitive for exactly this — a named **state dimension**
(`bd set-state`), which atomically replaces the prior value, records a `--reason`, and
writes an event bead (audit trail of every transition). Use the dimension `triage`:

| Canonical role    | bd command                                                                  |
| ------------------ | ---------------------------------------------------------------------------- |
| `needs-triage`      | `bd set-state <id> triage=needs-triage --reason "..."`                       |
| `needs-info`        | `bd set-state <id> triage=needs-info --reason "..."`                         |
| `ready-for-agent`   | `bd set-state <id> triage=ready-for-agent --reason "..."`                    |
| `ready-for-human`   | `bd set-state <id> triage=ready-for-human --reason "..."`                    |
| `wontfix`           | `bd set-state <id> triage=wontfix --reason "..."` then `bd close <id>`       |

This produces a `triage:<value>` label under the hood (queryable via
`bd state <id> triage` or `bd label list <id>`) plus a permanent event-log entry —
strictly better than a bare label for this use case since the *why* isn't lost.
`wontfix` also closes the issue — "will not be actioned" has no other bd status.

Post agent briefs and triage notes via `bd comment <id> "..."` (every comment must open
with the required AI-disclaimer line — see the triage skill's top-level instructions).

## Category role → bd type

bd's `type` field already distinguishes `bug` from `feature`, and bd recognizes
`enhancement`/`feat` as aliases for `feature` natively:

| Canonical role | bd command                          |
| -------------- | ------------------------------------ |
| `bug`          | `bd update <id> --type bug`          |
| `enhancement`  | `bd update <id> --type enhancement`  |

bd also has `task`/`epic`/`chore`/`decision` types with no canonical-role equivalent.
Default new/pre-existing `task`-typed issues to whichever of bug/enhancement the content
actually reads as during triage; leave `epic`/`chore`/`decision` issues out of the
triage-role system entirely (they aren't work items in the triage sense).

## Querying

- "Unlabeled" (never triaged) = a bd issue with none of the five state labels set —
  `bd list --status=open` and check each issue's labels (bd has no `--label` list filter
  as of this writing; filter client-side).
- `bd list` output and `bd show <id>` both surface labels when present.
