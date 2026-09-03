#!/bin/bash
# SessionStart hook — project setup that must hold in every session, cloud or
# local. It is deliberately NOT the place to install toolchains: a cloud
# environment's setup script does that once and gets snapshotted, while this
# runs on every session including resumes. Anything slow belongs there.
#
# The rule for everything below: never fail the session. A hook that exits
# non-zero on a laptop that happens not to have `bd` yet is worse than the gap
# it reports, so each step degrades to a printed note.
set -uo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

# bd (beads) is this repo's issue tracker and CLAUDE.md mandates it for all task
# tracking. It is not pre-installed in a cloud VM, and the bare `bd prime` that
# used to sit here failed silently every cloud session — the agent then worked
# a whole session with no tracker and no indication one was expected.
if command -v bd >/dev/null 2>&1; then
  bd prime
else
  echo "NOTE: bd (beads) is not installed, so issue tracking is unavailable this session."
  echo "      File follow-up work in your reply instead of dropping it."
  echo "      To install it, add the prebuilt-binary setup script from"
  echo "      docs/cloud-sessions.md to the cloud environment's setup script --"
  echo "      NOT 'go install', which cold-builds bd's Dolt-linked ~200 MB binary"
  echo "      from source and needs a C++ toolchain plus ICU headers -- a measured"
  echo "      cold build fits the setup-script budget on its own, but the prebuilt"
  echo "      binary is a 25x smaller download with no toolchain to keep working."
  echo "      The short version: fetch"
  echo "      beads_1.0.4_<os>_<arch>.tar.gz from"
  echo "      github.com/gastownhall/beads/releases/download/v1.0.4, verify it"
  echo "      against that release's checksums.txt (sha256sum/shasum -a 256), then"
  echo "      extract bd onto PATH (e.g. /usr/local/bin)."
  echo "      v1.0.4 is this repo's pin — the version the operator runs. @latest can"
  echo "      be newer than the Dolt data on refs/dolt/data, and bd then refuses to"
  echo "      hydrate rather than migrating a database shared with your other clones."
fi

# chromedp (Fantrax login) searches PATH for a browser under a fixed set of
# names and knows nothing about the Playwright install a cloud VM ships. One
# symlink is the whole fix; installing a second Chrome would cost minutes of
# setup time and ~500 MB for a browser already on disk.
if ! command -v chromium >/dev/null 2>&1; then
  for candidate in /opt/pw-browsers/chromium-*/chrome-linux/chrome; do
    if [ -x "$candidate" ]; then
      ln -sf "$candidate" /usr/local/bin/chromium 2>/dev/null \
        && echo "Linked chromium -> $candidate (chromedp/Fantrax login)"
      break
    fi
  done
fi

# Credentials are what separate "can build and test" from "can talk to Fantrax".
# Say which mode this session is in up front, so the difference is known before
# a command fails halfway through rather than after.
missing=""
for var in FANTRAX_USERNAME FANTRAX_PASSWORD FANTRAX_LEAGUE_ID FANTRAX_TEAM_ID; do
  [ -z "${!var:-}" ] && missing="$missing $var"
done
if [ -n "$missing" ] && [ ! -f .env ]; then
  echo "NOTE: no Fantrax credentials ($(echo $missing | tr ' ' ',')) and no .env."
  echo "      Offline mode: build, vet, tests, and the web dashboard all work."
  echo "      Anything that reads Fantrax live (optimize, gs-check, waivers,"
  echo "      claims, transactions, recap, archive, team-values) will not run."
fi

exit 0
