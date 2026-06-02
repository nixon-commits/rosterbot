.PHONY: build install test run dry-run run-all clean-cache

build:
	go build -o rosterbot .

install:
	go install .
	"$$(go env GOPATH)/bin/rosterbot" completion zsh > "$${HOMEBREW_PREFIX:-/usr/local}/share/zsh/site-functions/_rosterbot"

test:
	go test ./internal/...

run:
	go run . optimize

dry-run:
	go run . optimize --dry-run

# Wipe the on-disk file cache. The next run repopulates everything from
# upstream APIs. Pair with `run-all` to compare cold vs warm timings:
#   make clean-cache && make run-all   # cold pass, all cache misses
#   make run-all                       # warm pass, mostly cache hits
clean-cache:
	rm -rf .cache/

# run-all exercises every command in a dry-run / read-only configuration.
# Useful as an end-to-end smoke test and for observing the cache layer:
# stderr lines like `cache hit:` / `cache miss:` show what each command
# touched. Each step continues on error so one broken command doesn't
# abort the whole sweep — final-status check is on you.
run-all:
	@echo "=== scoring ===";                              time go run . scoring;                                          echo
	@echo "=== optimize --dry-run ===";                   time go run . optimize --dry-run;                               echo
	@echo "=== prospects --dry-run ===";                  time go run . prospects --dry-run;                              echo
	@echo "=== gs-check --dry-run --force ===";           time go run . gs-check --dry-run --force;                       echo
	@echo "=== transactions --dry-run ===";               time go run . transactions --dry-run;                           echo
	@echo "=== waivers --dry-run ===";                    time go run . waivers --dry-run;                                echo
	@echo "=== backtest ===";                             time go run . backtest;                                         echo
	@echo "=== recap --out /tmp/recap.html ===";          time go run . recap --out /tmp/recap.html;                      echo
	@echo "=== recap-site --out /tmp/recap-site ===";     time go run . recap-site --out /tmp/recap-site;                 echo
	@echo "=== cache size ===";                           du -sh .cache/ 2>/dev/null || echo "(no cache directory)";      echo
	@echo "=== DONE ==="
