# Issue tracker: Local Markdown (superseded)

> **Superseded for issue tracking.** This repo now uses **bd (beads)** for all task/issue
> tracking — see the "Beads Issue Tracker" section in the root `CLAUDE.md`. Skills that say
> "publish to the issue tracker" or "fetch the relevant ticket" should use `bd create`/`bd show`/
> `bd update`, not this file's `.scratch/` convention. See `triage-labels.md` for how the
> triage skill's canonical roles map onto bd. `.scratch/<feature-slug>/` may still see
> occasional use for freeform PRD drafts ahead of filing bd issues, but it is not where
> issues or triage state live.

## Current convention

| A skill says…                    | Do this                                                      |
| -------------------------------- | ------------------------------------------------------------ |
| "publish to the issue tracker"   | `bd create --title=… --description=… --type=… --priority=…`   |
| "fetch the relevant ticket"      | `bd show <id>` (the user passes the bd id, e.g. `rosterbot-h9q`) |
| "record triage state"            | `bd set-state <id> triage=<role> --reason "…"` — see `triage-labels.md` |
| "comment on the issue"           | `bd comment <id> "…"`                                         |

`.scratch/` is not read by any of these. It holds nothing durable — it is local scratch space
for freeform PRD drafts that get filed into bd, is not committed, and is absent from a fresh
checkout (including this one).

## Historical: what the markdown convention was

Retained so that references to `.scratch/<feature-slug>/issues/NN-slug.md` in older PRDs,
commit messages and `docs/superpowers/specs/` are legible. **None of this is live.**

- One feature per directory: `.scratch/<feature-slug>/`
- The PRD was `.scratch/<feature-slug>/PRD.md`
- Implementation issues were `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`
- Triage state was a `Status:` line near the top of each issue file
- Comments appended to the bottom of the file under a `## Comments` heading
