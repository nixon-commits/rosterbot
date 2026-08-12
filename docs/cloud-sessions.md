# Claude Code cloud sessions

How to make a [Claude Code on the web](https://code.claude.com/docs/en/claude-code-on-the-web)
session behave like a local checkout of this repo.

A cloud session is a fresh Ubuntu 24.04 VM with the repo cloned and common
toolchains pre-installed. It gets your committed `CLAUDE.md`, `.claude/`
settings, hooks, agents and skills for free. It does **not** get your `.env`,
your warm `.cache/`, your `~/.claude/`, or anything else that lives only on
your machine. Three things therefore have to be configured, and only one of
them lives in this repo.

| Layer | Where it lives | What it covers |
|---|---|---|
| Setup script | environment dialog at claude.ai/code | Tools the VM lacks (`bd`). Runs once, then snapshotted |
| Env vars + network | environment dialog at claude.ai/code | Fantrax config, and egress to this repo's data sources |
| SessionStart hook | `.claude/hooks/session-start.sh` (committed) | Per-session project setup; runs locally too |

Open the dialog from the cloud icon above the message box at
[claude.ai/code](https://claude.ai/code) — hover an environment and pick the
gear, or **Add cloud environment**. There is no settings page or direct URL.

## Network access

The default **Trusted** level covers package registries and GitHub but **none
of this repo's upstreams**, so a session can build and test but cannot fetch a
projection or log into Fantrax. Set **Network access** to **Custom**, check
*Also include default list of common package managers*, and list:

```text
www.fantrax.com
harryknowsball.com
www.fangraphs.com
statsapi.mlb.com
baseballsavant.mlb.com
api.sleeper.app
api.statsguyfantasy.com
api.pushover.net
```

That is every host the Go code dials, from
`grep -rhoE 'https?://[a-zA-Z0-9._-]+' --include=*.go internal/ cmd/`. Keep
that grep as the source of truth — a new provider package means a new line
here, and the symptom of forgetting is a `403` from the proxy that reads like
an upstream outage. `api.pushover.net` is only needed for real (non-dry-run)
notification sends; drop it if the environment is for read-only work.
`midfield.mlbstatic.com` is deliberately absent: the recap embeds those logo
URLs into HTML and never fetches them.

## Environment variables

`.env` format, one `KEY=value` per line. The values a session needs:

```dotenv
FANTRAX_LEAGUE_ID=...
FANTRAX_TEAM_ID=...
FANTRAX_IL_SLOTS=3
FANTRAX_MINORS_SLOTS=5
```

> [!WARNING]
> **Cloud environments have no secrets store.** Anthropic's docs say plainly
> that anyone who uses the environment can read the values and not to put
> credentials there. `FANTRAX_USERNAME` / `FANTRAX_PASSWORD` are credentials.
> Adding them is what makes `optimize`, `gs-check`, `waivers`, `claims`,
> `transactions`, `recap` and `archive` runnable in a cloud session, and it is
> a real trade — a personal environment on your own account is a much smaller
> exposure than a shared one, but it is not zero, and a Fantrax password is
> reusable. Leaving them out costs only the live-Fantrax commands: everything
> else in this repo (build, vet, the full test suite, the dashboard, every pure
> package) works without them.

## Setup script

Installs what the VM lacks. It must exit zero — a non-zero exit fails session
startup — and finish in about five minutes so the environment cache can build.

```bash
#!/bin/bash
# ICU headers first: bd links Dolt, which will not compile without them
# (the build dies on unicode/uregex.h).
apt-get update -qq || true
apt-get install -y -qq libicu-dev || true

# The bd build is the long pole (~200 MB binary), so start it and warm the
# module cache alongside it.
go install github.com/steveyegge/beads/cmd/bd@vX.Y.Z &
BD=$!
for d in /home/user/rosterbot /workspace/rosterbot; do
  if [ -f "$d/go.mod" ]; then (cd "$d" && go mod download) || true; break; fi
done
wait $BD || true

# go install writes to GOPATH/bin, which is NOT on PATH in a cloud session —
# without this the binary exists and every `command -v bd` still fails.
GOBIN="$(go env GOPATH)/bin"
[ -x "$GOBIN/bd" ] && ln -sf "$GOBIN/bd" /usr/local/bin/bd

exit 0
```

**Pin `vX.Y.Z` to the version your laptop runs** (`bd version`). `@latest`
installed v1.2.1 here, which is newer than the Dolt data on this repo's
`refs/dolt/data`; `bd bootstrap` then refuses to hydrate and `bd init` offers
to migrate a database shared with every other clone. Matching the version
sidesteps the question. If you do want to upgrade, do it deliberately from one
machine, not implicitly from a cloud VM that is discarded an hour later.

## What the SessionStart hook handles

`.claude/hooks/session-start.sh` is committed, so it runs in every session,
cloud and local. It does no installing — that is the setup script's job, paid
once — and it never exits non-zero, because a hook that fails the session is
worse than the gap it reports. It:

- runs `bd prime` when `bd` is present, and prints the install recipe when it
  is not. The bare `bd prime` that used to be wired directly into
  `.claude/settings.json` failed silently on every cloud session, so the agent
  worked without a tracker and without any sign one was expected;
- links `chromium` onto PATH from the Playwright browser the VM already ships
  (`/opt/pw-browsers/chromium-*/chrome-linux/chrome`). chromedp — the Fantrax
  login path — searches PATH for a fixed set of browser names and knows nothing
  about that install, so without the link Fantrax auth fails even with valid
  credentials. One symlink beats installing a second ~500 MB Chrome;
- prints whether the session has Fantrax credentials, so "offline mode" is
  known up front rather than discovered when a command fails halfway.

## What still differs from your machine

- **Cold caches.** `.cache/`, `.fantrax-cache/` and `.backtest/` are gitignored
  and start empty, so the first `optimize` pays every upstream fetch and does a
  full chromedp browser login rather than reusing a session cookie. `make
  clean-cache && make run-all` is the local equivalent of a cloud session's
  first run.
- **No AWS.** `aws` is not installed and no credentials are present, so
  `STATE_BUCKET` is unset and every store falls back to its local directory —
  which is the intended local-dev path, not a degraded one.
- **`gh` is absent by design.** Cloud sessions use the GitHub MCP tools and a
  credential proxy instead; `git push` works only on the session's own branch.
