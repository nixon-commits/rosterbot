---
name: dry-run
description: Clear caches and run full optimizer with prospect report in dry-run mode
---

Run the following commands in sequence:

1. `rm -f .cache/rankings.json`
2. `go run ./cmd --prospects --dry-run`

Report the output, highlighting any warnings or errors. Summarize the prospect report (alerts and upgrades) and lineup changes.
