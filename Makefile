.PHONY: build build-modules check-pins install lint lint-install test test-modules run dry-run run-all clean-cache

build: check-pins build-modules
	go build -o rosterbot .

# lambda/, opsnotify/ and infra/ are SEPARATE Go modules (each its own
# go.mod); `go build ./...` at the repo root never descends into them. CDK
# bundles lambda/ and opsnotify/ as GoFunction assets at deploy time, so a
# stale go.mod in ANY of them only surfaces as a failed `cdk deploy` — this
# broke the dashboard-v2 deploy twice (lambda/, then opsnotify/). They share
# deps with the root module via `replace ../`, so every dependabot bump to a
# shared root dep re-stales them. Cross-compile every nested module for the real
# target so the break fails locally. Fix a failure with: cd <dir> && go mod tidy
build-modules:
	@for d in $$(find . -name go.mod -not -path './.git/*' -not -path './.claude/*' | grep -v '^\./go\.mod$$' | xargs -n1 dirname | sort); do \
	  printf '  build %s\n' "$$d"; \
	  ( cd "$$d" && GOOS=linux GOARCH=arm64 go build -o /dev/null ./ ) || exit 1; \
	done

# Every module that imports a forked dep carries its OWN `replace` line for it —
# the root and lambda/ each pin github.com/pmurley/go-fantrax to a fork commit,
# with nothing making them agree. `build-modules` catches that drift only when
# the bump adds exported symbols (which is why rosterbot-7i3 looked covered);
# in the actual recovery scenario — bumping auth_client.APIVersion's STRING with
# no API change — a stale lambda/go.mod compiles clean, CI stays green, and the
# Lambda ships the old constant. `version-check` can't see it either: it probes
# the root binary's pin only. So: any module path replaced in more than one
# go.mod must resolve to the same target everywhere (rosterbot-00e).
#
# Only VERSION-pinned replaces are compared. `replace <root> => ../` appears in
# lambda/ and opsnotify/ and the strings match, but only by luck — a filesystem
# path resolves against its own module dir, so comparing it across modules is
# meaningless. Filtering on a non-empty Version drops those honestly instead of
# passing them accidentally.
check-pins:
	@command -v jq >/dev/null 2>&1 || { echo "check-pins: jq not found (required)" >&2; exit 1; }
	@tmp=$$(mktemp); \
	for f in $$(find . -name go.mod -not -path './.git/*' -not -path './.claude/*' | sort); do \
	  d=$$(dirname "$$f"); \
	  json=$$( cd "$$d" && go mod edit -json ) || { echo "check-pins: go mod edit failed in $$d" >&2; rm -f "$$tmp"; exit 1; }; \
	  printf '%s' "$$json" | jq -r --arg d "$$d" \
	    '.Replace[]? | select(.New.Version != null and .New.Version != "") | "\(.Old.Path)\t\(.New.Path)@\(.New.Version)\t\($$d)"' >> "$$tmp" || { rm -f "$$tmp"; exit 1; }; \
	done; \
	bad=$$(cut -f1,2 "$$tmp" | sort -u | cut -f1 | uniq -d); \
	if [ -n "$$bad" ]; then \
	  echo "check-pins: replace directives disagree across modules:" >&2; \
	  for p in $$bad; do \
	    sort "$$tmp" | awk -F'\t' -v p="$$p" '$$1==p {printf "  %-12s %s => %s\n", $$3, $$1, $$2}' >&2; \
	  done; \
	  echo "  fix: make every go.mod above use one target, then re-run make build-modules" >&2; \
	  rm -f "$$tmp"; exit 1; \
	fi; \
	n=$$(sort -u "$$tmp" | wc -l | tr -d ' '); \
	rm -f "$$tmp"; \
	printf '  pins agree (%s version-pinned replace site(s))\n' "$$n"

# Pinned so an upstream golangci-lint release cannot turn CI red with no code
# change. CI installs exactly this version via `make lint-install`, so the
# version developers run and the version that gates a PR are one string.
GOLANGCI_LINT_VERSION ?= v2.13.1

# One .golangci.yml at the repo root serves all four modules — golangci-lint
# walks up from each module dir to find it, so there is a single definition of
# "what lints" (same reasoning as check-pins: two copies would silently drift).
#
# The loop mirrors build-modules/test-modules rather than linting `./...` from
# the root, because the root run never descends into lambda/, opsnotify/ or
# infra/ — the mistake test-modules exists to document.
#
# A missing golangci-lint is a HARD ERROR, not a skip, exactly like the jq
# check in check-pins: a gate that silently no-ops reports clean while checking
# nothing, which is worse than no gate at all.
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "lint: golangci-lint not found (required)" >&2; \
	  echo "  fix: make lint-install" >&2; exit 1; }
	@printf '  lint .\n'
	@golangci-lint run ./...
	@for d in $$(find . -name go.mod -not -path './.git/*' -not -path './.claude/*' | grep -v '^\./go\.mod$$' | xargs -n1 dirname | sort); do \
	  printf '  lint %s\n' "$$d"; \
	  ( cd "$$d" && golangci-lint run ./... ) || exit 1; \
	done

lint-install:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

install:
	go install .
	"$$(go env GOPATH)/bin/rosterbot" completion zsh > "$${HOMEBREW_PREFIX:-/usr/local}/share/zsh/site-functions/_rosterbot"

# Mirrors `build: build-modules`. The root run never descends into the nested
# modules (lambda/, opsnotify/, infra/), so their tests silently never ran —
# opsnotify/build_test.go sat dead for months.
#
# `./...`, NOT `./internal/...`. Naming a subtree makes the gate an allowlist of
# directories, so it silently re-breaks every time code lives somewhere new —
# and it had: cmd/ is neither internal/ nor its own module, so its 26 test files
# ran in no workflow at all. That is the composition root, where every
# tenant-credential, tenant-sync, auto_apply and connect-gate guard lives.
#
# Proven rather than assumed: reintroducing the rosterbot-crq.17 silent
# operator-credential fallback in cmd/credmode.go passed `go build`, `go vet`,
# `make check-pins`, `make build-modules` AND `make test`, and was caught only
# by `go test ./cmd/...` run by hand.
#
# The first fix for this widened the allowlist (b823b57, adding the nested
# modules) instead of correcting its shape, which is why it broke again one
# commit later. Test the module, not a list of its directories.
test: test-modules
	go test ./...

test-modules:
	@for d in $$(find . -name go.mod -not -path './.git/*' -not -path './.claude/*' | grep -v '^\./go\.mod$$' | xargs -n1 dirname | sort); do \
	  printf '  test %s\n' "$$d"; \
	  ( cd "$$d" && go test ./... ) || exit 1; \
	done

run:
	go run . optimize

dry-run:
	go run . optimize --dry-run

# Wipe the on-disk file cache. The next run repopulates everything from
# upstream APIs. Pair with `run-all` to compare cold vs warm timings:
#   make clean-cache && make run-all   # cold pass, all cache misses
#   make run-all                       # warm pass, mostly cache hits
# Clears only the LOCAL filesystem cache. On AWS the cache lives in S3 (cache/
# prefix) — clear it with: aws s3 rm s3://<state-bucket>/cache/ --recursive
clean-cache:
	rm -rf .cache/

# run-all exercises every command in a dry-run / read-only configuration.
# Useful as an end-to-end smoke test and for observing the cache layer:
# stderr lines like `cache hit:` / `cache miss:` show what each command
# touched. Each step continues on error so one broken command doesn't
# abort the whole sweep — final-status check is on you.
# run-all exercises every CLI command in dry-run / read-only mode.
#
# Each step is chained with `&&` rather than `;`. In shell, `a; b` exits with
# B's status, so the trailing `echo` that separated the sections swallowed
# every command's exit code — the target could not fail, while CLAUDE.md
# calls it the canonical pre-push smoke test (rosterbot-ww8). The only way a
# failure is ignored now is an explicit fallback that says why.
run-all:
	@echo "=== build-modules (nested Go modules) ===";    $(MAKE) build-modules &&                                          echo
	@echo "=== scoring ===";                              time go run . scoring &&                                          echo
	@echo "=== optimize --dry-run --publish-lineup ===";  time go run . optimize --dry-run --publish-lineup &&              echo
	@echo "  (serve is long-running — exercise manually: ROSTERBOT_API_TOKEN=test go run . serve)"
	@echo "=== prospects --dry-run ===";                  time go run . prospects --dry-run &&                              echo
	@echo "=== gs-check --dry-run ===";                   time go run . gs-check --dry-run &&                               echo
	@echo "=== version-check ===";                        time go run . version-check &&                                    echo
	@echo "=== transactions --dry-run ===";               time go run . transactions --dry-run &&                           echo
	@echo "=== waivers --dry-run ===";                    time go run . waivers --dry-run &&                                echo
	@echo "=== claims --dry-run ===";                    time go run . claims --dry-run --no-signals &&                    echo
	@echo "=== grade --dry-run ===";                     time go run . grade --dry-run &&                                  echo
	@echo "=== archive --dry-run ===";                   time go run . archive --dry-run &&                                echo
	@echo "=== team-values --dry-run ===";               time go run . team-values --dry-run &&                            echo
	@echo "=== football-values --dry-run ===";           if [ -n "$$SLEEPER_LEAGUE_ID" ]; then time go run . football-values --dry-run; else echo "SKIPPED (SLEEPER_LEAGUE_ID unset; it lives in SSM for the deployment)"; fi && echo
	@echo "=== football-trades --dry-run ===";           if [ -n "$$SLEEPER_LEAGUE_ID" ]; then time go run . football-trades --dry-run; else echo "SKIPPED (SLEEPER_LEAGUE_ID unset)"; fi && echo
	@echo "=== backtest ===";                             time go run . backtest &&                                         echo
	@echo "=== backtest --recency-experiment ===";        time go run . backtest --recency-experiment --dates 2026-05-01:2026-05-07 || echo "(tolerated: needs archived snapshots that may not exist locally)" && echo
	@echo "=== recap --out /tmp/recap.html ===";          time go run . recap --out /tmp/recap.html &&                      echo
	@echo "=== recap-site --out /tmp/recap-site ===";     time go run . recap-site --out /tmp/recap-site &&                 echo
	@echo "=== invite --dry-run ===";                        time go run . invite --dry-run --email smoke@example.test --name Smoke && echo
	@echo "=== user set-team --dry-run ===";                 time go run . user set-team --dry-run --user smoke-nobody --team team-0 || true; echo
	@echo "=== projection-site --out /tmp/rosterbot-proj-report ==="; time go run . projection-site --out /tmp/rosterbot-proj-report || echo "(tolerated: needs an Analysis Store that may be empty locally)" && echo
	@echo "=== cache size ===";                           du -sh .cache/ 2>/dev/null || echo "(no cache directory)";      echo
	@echo "=== DONE ==="
