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

`FANTRAX_USERNAME` and `FANTRAX_PASSWORD` are deliberately **not** in that list.

> [!WARNING]
> **Cloud environments have no secrets store.** Anthropic's docs say plainly
> that anyone who uses the environment can read the values and not to put
> credentials there. `FANTRAX_USERNAME` / `FANTRAX_PASSWORD` are credentials —
> and a Fantrax password is a *reusable primary* one: Fantrax issues no scoped
> API tokens, so the only revocation is changing the password everywhere.

**The decision for this repo is to leave them out**, because the cost of doing
so is much smaller than it first appears. The two constraints are independent,
and it is the **allowlist**, not the password, that unlocks most of a cloud
session:

| Needs only the network allowlist | Needs Fantrax credentials |
|---|---|
| `archive` (HKB, FanGraphs, Savant, prospects — **verified running with no credentials at all**), plus build, `go vet`, the full test suite, the dashboard, and every pure package | `optimize`, `gs-check`, `waivers`, `claims`, `transactions`, `recap` |

Every upstream except Fantrax is unauthenticated. So a credential-free cloud
session can still develop and exercise anything built on HKB, FanGraphs,
Savant or MLB statsapi — which is most of this repo's data surface.

What remains behind the password is the set of commands that read live league
state or write to a live roster, and those are the ones worth running where
you can see the result. This branch is the worked example: the verification
that actually mattered — the live HKB join — needed the allowlist and no
credentials, while the Fantrax-dependent half was better done locally anyway.

If you decide otherwise, do it in a *personal* environment on your own account,
never a shared one, and treat the password as disclosed the moment it goes in.

## Setup script

Installs what the VM lacks. It must exit zero — a non-zero exit fails session
startup — and finish in about five minutes so the environment cache can build.

```bash
#!/bin/bash
# bd (beads) ships prebuilt release binaries; fetch one of those instead of
# building from source. `go install github.com/steveyegge/beads/cmd/bd@v1.0.4`
# compiles bd's Dolt-linked ~200 MB binary from scratch, backgrounded against
# `go mod download` on the theory that the two together would fit the ~5
# minute budget the environment allows before it gives up on caching. That
# theory is now timed (see "Measured cost" below): a fully cold `go install`
# alone finishes around 42s on comparable hardware, well inside the budget --
# so the switch below isn't rescuing a timeout, it's the bead's own stated
# preference plus a much smaller download and no C++/ICU toolchain to keep
# working. The prebuilt tarball is a ~45 MB download instead (verified live:
# linux_amd64 is 47,668,079 bytes, linux_arm64 is 44,283,170, via the GitHub
# Releases API), verified against the release's own checksums.txt before
# anything from it runs. There's no apt-get step left: it existed only to
# give the from-source build a C++ toolchain and ICU headers (unicode/regex.h
# and unicode/uregex.h), and beads' own install script
# (github.com/gastownhall/beads/blob/main/scripts/install.sh) installs the
# release binaries on a bare box with no apt-get at all.
BD_VERSION="1.0.4"
BD_OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64) BD_ARCH="amd64" ;;
  aarch64|arm64) BD_ARCH="arm64" ;;
  *) BD_ARCH="$(uname -m)" ;;
esac
BD_ASSET="beads_${BD_VERSION}_${BD_OS}_${BD_ARCH}.tar.gz"
BD_BASE_URL="https://github.com/gastownhall/beads/releases/download/v${BD_VERSION}"

# sha256sum is the common case (every Linux distro's coreutils); shasum -a
# 256 covers macOS, which has no sha256sum by default. Picking the tool once
# up front (rather than chaining `sha256sum ... || shasum ...`) matters: a
# genuine mismatch from sha256sum still exits non-zero, and chained that fell
# through to shasum too -- which then failed on its own with a confusing "no
# properly formatted SHA checksum lines found" instead of one clear message.
if command -v sha256sum >/dev/null 2>&1; then
  BD_SHA256_VERIFY="sha256sum -c -"
else
  BD_SHA256_VERIFY="shasum -a 256 -c -"
fi

bd_tmp="$(mktemp -d)"
if curl -fsSL --max-time 120 -o "$bd_tmp/$BD_ASSET" "$BD_BASE_URL/$BD_ASSET" \
    && curl -fsSL --max-time 30 -o "$bd_tmp/checksums.txt" "$BD_BASE_URL/checksums.txt"; then
  bd_line="$(awk -v f="$BD_ASSET" '$2 == f' "$bd_tmp/checksums.txt")"
  if [ -n "$bd_line" ] && (cd "$bd_tmp" && echo "$bd_line" | $BD_SHA256_VERIFY); then
    tar -xzf "$bd_tmp/$BD_ASSET" -C "$bd_tmp"
    install -m 0755 "$bd_tmp/bd" /usr/local/bin/bd
  else
    echo "bd: checksum verification failed for $BD_ASSET, leaving bd uninstalled" >&2
  fi
else
  echo "bd: no release asset for $BD_OS/$BD_ARCH ($BD_ASSET), leaving bd uninstalled" >&2
fi
rm -rf "$bd_tmp"

# Warm the module cache in the foreground -- there's no longer a long
# background `go install` for it to overlap with.
for d in /home/user/rosterbot /workspace/rosterbot; do
  if [ -f "$d/go.mod" ]; then (cd "$d" && go mod download) || true; break; fi
done

exit 0
```

**The pin is `v1.0.4`** — the version this repo's operator runs locally, and the
one to re-check with `bd version` if it ever drifts. `@latest`
installed v1.2.1 here, which is newer than the Dolt data on this repo's
`refs/dolt/data`; `bd bootstrap` then refuses to hydrate and `bd init` offers
to migrate a database shared with every other clone. Matching the version
sidesteps the question. If you do want to upgrade, do it deliberately from one
machine, not implicitly from a cloud VM that is discarded an hour later. The
pin now lives entirely in the release URL (`BD_VERSION`), not in a Go module
`@` suffix, so bumping it costs one variable edit and no `go install` cache to
invalidate or distrust.

**The old `go install` cold-build time — the bead's step 1 — is measured, and
it fits the budget.** `release-assets.githubusercontent.com` (where GitHub
redirects a release-asset `curl`) is unreachable from this analysis sandbox —
confirmed again while writing this — but that host has nothing to do with
timing the *old* approach: `go install
github.com/steveyegge/beads/cmd/bd@v1.0.4` only needs `proxy.golang.org`
(reachable here in ~1s) plus a C++ toolchain and ICU headers, and an earlier
pass at this doc wrongly treated the CDN outage as blocking that measurement
too. Run cold — fresh `GOPATH`, `GOCACHE` and `GOMODCACHE`, so every module
download and every compile actually happened — it finished in **41.7s wall**
(`181.00s user, 35.71s system, 519% cpu`), producing a 178.5 MB binary that
then reported `bd version 1.0.4 (dev)`. That is comfortably inside the ~5
minute setup-script budget, on this machine. (It needed `CGO_CFLAGS` and
`CGO_CXXFLAGS` pointed at a local ICU install — Homebrew's `icu4c@78` here —
plus `CGO_LDFLAGS`; the package has no `#cgo pkg-config` directive, so nothing
finds ICU without those set explicitly. And the missing header on a plain
build is `unicode/regex.h`, pulled in by a C++ translation unit, not only the
`unicode/uregex.h` the comment above names — both are real requirements, but
`regex.h` is the one that actually stops a naive build.) This measurement
doesn't prove a cloud VM — different cores, different network path to
`proxy.golang.org` — lands under budget too, but it does mean the switch to a
prebuilt binary was not rescuing a build that was actually timing out on
comparable hardware; the "theory nobody had actually timed" in the comment
above is now timed, and came in fine. The prebuilt-binary approach still
stands on its own merits — the bead's own first preference, a 25x smaller
download, and no C++/ICU toolchain dependency to go stale — just not on an
urgent timeout risk.

**What's still unmeasured (rosterbot-c8e), and why.** Two numbers remain
genuinely out of reach from this sandbox, for the reason above: the *new*
script's real network-download time, and whether the environment cache
engages on a second session afterward (the observable signal is that second
session starting fast). Both need `release-assets.githubusercontent.com` —
confirmed unreachable again just now, timing out connecting to all four
Fastly edge IPs on every attempt, while `github.com`, `api.github.com` and
`proxy.golang.org` stayed reachable in under half a second (`proxy.golang.org`
under a second) — or a real `claude.ai/code` cloud session, and record them
here once one runs the script. What *was* verified from this sandbox for the
new approach: the real release assets are ~45 MB
(`beads_1.0.4_linux_amd64.tar.gz` is 47,668,079 bytes, `linux_arm64` is
44,283,170, per the GitHub Releases API); and the script's own logic —
arch/os detection, download, checksum verification on both a matching and a
deliberately corrupted checksum, extraction, and the PATH install — was run
end to end against a local HTTP server standing in for GitHub, serving a
real `bd 1.0.4` binary repackaged into an identically-shaped tarball (44.2 MB,
matching the real `darwin_arm64` asset's 44,302,058 bytes almost exactly),
and came back reporting `bd version 1.0.4` afterward; the corrupted-checksum
run correctly refused to install anything and left `bd` absent. Non-network
overhead (checksum + extract + install) measured under 0.3s against that
local stand-in.

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
