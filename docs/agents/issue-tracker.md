# Issue tracker: Local Markdown

> **Superseded for issue tracking.** This repo now uses **bd (beads)** for all task/issue
> tracking — see the "Beads Issue Tracker" section in the root `CLAUDE.md`. Skills that say
> "publish to the issue tracker" or "fetch the relevant ticket" should use `bd create`/`bd show`/
> `bd update`, not this file's `.scratch/` convention. See `triage-labels.md` for how the
> triage skill's canonical roles map onto bd. `.scratch/<feature-slug>/` may still see
> occasional use for freeform PRD drafts ahead of filing bd issues, but it is not where
> triage state lives.

Issues and PRDs for this repo live as markdown files in `.scratch/`.

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`
- The PRD is `.scratch/<feature-slug>/PRD.md`
- Implementation issues are `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`
- Triage state is recorded as a `Status:` line near the top of each issue file (see `triage-labels.md` for the role strings)
- Comments and conversation history append to the bottom of the file under a `## Comments` heading

## When a skill says "publish to the issue tracker"

Create a new file under `.scratch/<feature-slug>/` (creating the directory if needed).

## When a skill says "fetch the relevant ticket"

Read the file at the referenced path. The user will normally pass the path or the issue number directly.
