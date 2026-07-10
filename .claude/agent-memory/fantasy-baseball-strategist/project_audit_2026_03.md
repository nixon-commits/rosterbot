---
name: Strategic audit findings (Mar + Jul 2026)
description: Codebase audit history — Mar 2026 criticals now FIXED; Jul 2026 health-check findings (blend double-count, GS gate over-conservatism)
type: project
---

## July 2026 health-check (current)

Full strategic audit 2026-07-10. Overall: automation is fundamentally SOUND, no critical live bugs. Verified optimizer correctness/idempotency, scoring algebra against LIVE league weights, and QS population against cached FanGraphs data.

**Verified-safe (checked, not bugs):**
- Team abbreviations: Fantrax `MLBTeamShortName` returns canonical forms (ATH, CHW, KC, SD, SF, TB, WSH, ARI) matching `NormalizeTeam` output for all 30 teams. The schedule-lookup-vs-raw-MLBTeam asymmetry (projection path normalizes via `projKey`; schedule/probable path compares raw) is a latent coupling, NOT live. Would silently bench a whole team only if a future MLB/Fantrax abbrev change diverges. Cheap defensive fix: normalize MLBTeam at construction in client.go (3 sites).
- Optimizer: branch-and-bound with admissible upper-bound pruning is exact; tie-break (fewer changes) + eps=1e-9 converges to "No changes needed" on rerun. Idempotent.
- Scoring: derivations (1B=H-2B-3B-HR floored, XBH, TB) correct. Faithful to fetched weights (only applies stats with a configured weight).

**Live league weights (leagueID epsb8xzlmj203yrx), for reference:**
- Hitter: 1B=1,2B=2,3B=3,HR=4,RBI=1,R=1,BB=1,SB=4,CS=-1,HBP=1,SO=-1,GIDP=-1,XBH=2,TB=1,**CYC=50**. Note league scores BOTH component hits AND XBH+TB aggregates (intentional, not double-count bug).
- Pitcher: IP=3,K=1,BB=-1,H=-1,ER=-2,W=5,QS=8,SV=8,HLD=2,SHO=10,**NH=25,PG=75**. No L penalty, no HR-allowed penalty.

**Findings (strategic, real points impact):**
1. Hitter blend may DOUBLE-COUNT season form. Base is depthcharts-ros (already YTD-aware, regressed); blend adds ~58% raw UNregressed YTD FP/G on top (at ~90 GP, mid-July). Partially undoes projection regression. Affects every hitter daily. Empirically testable via existing `backtest --recency-experiment` / `shadow` / `grade` harness — compare base-only vs current blend by lineup Gap.
2. GS gate over-conservatism (gs_budget.go ~164-179): future ESTIMATED starts valued at full roster-SP mean AND ceil-rounded (`math.Ceil(futureEstimated)`), so a 0.2-expected day becomes a full-value competitor. Biases toward benching CERTAIN today-starts for PROJECTED future ones. Only active when GS_TRACKING_ENABLED + budget tight.
3. Non-starter SP discount still 0.10x (pitcher_lineup.go:67) — flagged Mar 2026, unchanged. Mostly bites future-date planning (daily run has probables). Consider 0.03-0.05.

**Minor:** CYC/NH/PG unhandled in scoring (correct for projections — unprojectable; trivially addable to mlb_backfill game-log scorer for exact historical grading, but immaterial). GS gate doc drift: CLAUDE.md still describes "proportional gate (allowToday=round(...))" but code does value-ranking across the week (better) — update the doc.

## March 2026 audit — items now RESOLVED (verified Jul 2026)
- RollingSource SO/GIDP omission: FIXED (rolling.go includes both).
- Hitter blend no min-GP: FIXED (config BlendMinGP, default 2).
- expectedPts duplicated optimizer/projections: FIXED (unified `internal/scoring` package; fantrax.ScoringWeights is an alias).
- Fixed blend weights 0.60/0.40: REPLACED with dynamic PA-based weights (hitter seasonWeight=approxPA/(approxPA+250); pitcher role-aware SP@15/RP@25).
- GHA timeout-minutes: OBSOLETE — GHA retired, now ECS Fargate + EventBridge.
