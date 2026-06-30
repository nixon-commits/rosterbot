# Projection system comparison (v2)

Status: ready-for-human

## Summary
Extend the projection-accuracy dashboard to compare MULTIPLE projection systems
and blending weights head-to-head (Steamer / DepthCharts / TheBatX + weighting
variants), not just the production blend.

## Dependency (blocking)
The grades store records ONE projected value per player-day (the production
blend). v2 requires a daily MULTI-SYSTEM capture pipeline: for each player-day,
snapshot each candidate system's projection, grade each against actual FPts.
This must accumulate weeks of data before comparisons are statistically
meaningful — so the capture should start well before the comparison UI ships.

## Scope sketch
- Capture: extend the snapshot writer (or a new sidecar) to record per-system
  projected pts/game per player per day; grade each into the Analysis Store with
  a `system` dimension on GradeRow.
- UI: overlay systems in the existing calibration + by-position panels (v1 was
  designed so a per-system series slots in without restructuring), plus a
  systems-vs-MAE leaderboard.

## Notes
v1 design: docs/superpowers/specs/2026-06-29-projection-accuracy-dashboard-design.md
