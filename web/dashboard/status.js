// status.js — the one-line answer to "is everything fine?", shown under the
// header on every tab.
//
// It is fed the runs array live.js already polls rather than fetching its own,
// so job health and lineup freshness cost nothing extra on the wire. The
// pending-offer count is the only additional request, and it is refreshed on a
// much slower cadence than the run poll because an offer arriving a minute late
// is not a problem the way a failing job is.
//
// Rows are built with createElement/textContent: a command string comes from
// the run ledger, which records whatever argv the task ran with.
import { api } from "./api.js";
import { el, relativeTime } from "./render.js";

const OFFERS_TTL_MS = 5 * 60 * 1000;

let offers = { count: null, fetchedAt: 0, inFlight: false };

// The ledger records full argv ("optimize --dates 2026-08-11"), but health is a
// property of the job, so everything here keys on the first token.
const jobOf = (run) => String(run.command || "").split(" ")[0];

// latestTerminalByJob reduces the run list to one record per job: the newest
// run that actually finished. RUNNING records are skipped rather than counted
// as either outcome — the same rule internal/opsalert.Streak follows, and for
// the same reason: an in-flight run is not yet evidence of anything.
function latestTerminalByJob(runs) {
  const byJob = new Map();
  for (const run of runs) {
    if (run.status === "RUNNING") continue;
    const job = jobOf(run);
    if (!byJob.has(job)) byJob.set(job, run);
  }
  return byJob;
}

// consecutiveFailures counts how many terminal runs of a job have failed in a
// row, newest first. Mirrors opsalert.Streak so the dashboard and the Pushover
// alert describe the same outage with the same number.
function consecutiveFailures(runs, job) {
  let n = 0;
  for (const run of runs) {
    if (run.status === "RUNNING" || jobOf(run) !== job) continue;
    if (run.status !== "SUCCESS") n++;
    else break;
  }
  return n;
}

function newestSuccessful(runs, job) {
  return runs.find((r) => jobOf(r) === job && r.status === "SUCCESS") || null;
}

// refreshOffers is fire-and-forget: a failure leaves the count null and the
// offer clause is simply omitted. A trades hiccup must never turn the status
// line — the thing that reports failures — into a failure of its own.
function refreshOffers(onDone) {
  const now = Date.now();
  if (offers.inFlight || now - offers.fetchedAt < OFFERS_TTL_MS) return;
  offers.inFlight = true;
  api.trades()
    .then((snap) => { offers.count = (snap && snap.offers ? snap.offers.length : 0); })
    .catch(() => { offers.count = null; })
    .finally(() => {
      offers.inFlight = false;
      offers.fetchedAt = Date.now();
      if (onDone) onDone();
    });
}

// buildStatus decides what the line says. Priority is deliberate: a failing job
// outranks everything, because it is the only state that needs an action from
// you. Lineup freshness leads otherwise, since that is the question the app is
// usually opened to answer.
function buildStatus(runs) {
  const byJob = latestTerminalByJob(runs);
  const failing = [...byJob.values()].filter((r) => r.status !== "SUCCESS");

  const rest = [];
  let tone = "ok";
  let main;

  if (failing.length) {
    tone = "bad";
    // Name the worst single outage rather than a count of jobs: "grade failed
    // 3×" tells you what to look at; "2 jobs failing" does not.
    const worst = failing
      .map((r) => ({ job: jobOf(r), streak: consecutiveFailures(runs, jobOf(r)) }))
      .sort((a, b) => b.streak - a.streak)[0];
    main = worst.streak > 1
      ? `${worst.job} failed ${worst.streak}× in a row`
      : `${worst.job} failed`;
    if (failing.length > 1) rest.push(`${failing.length} jobs failing`);
  }

  const lastOptimize = newestSuccessful(runs, "optimize");
  const lineupText = lastOptimize
    ? `Lineup applied ${relativeTime(lastOptimize.started_at)}`
    : "No successful lineup run recorded";
  if (!main) {
    main = lineupText;
    // A stale lineup is worth flagging even when nothing has technically
    // failed — a job that silently stopped being scheduled leaves every run
    // record green. This is the dashboard's small echo of opsalert.Overdue.
    const age = lastOptimize ? Date.now() - Date.parse(lastOptimize.started_at) : Infinity;
    if (!(age < 3 * 60 * 60 * 1000)) tone = "warn";
    if (byJob.size) rest.push("All jobs green");
  } else {
    rest.push(lineupText);
  }

  if (offers.count) {
    rest.push(`${offers.count} offer${offers.count === 1 ? "" : "s"} waiting`);
  }

  return { tone, main, rest: rest.filter(Boolean) };
}

export function renderStatus(runs) {
  const host = document.getElementById("status-line");
  if (!host) return;
  if (!Array.isArray(runs) || runs.length === 0) {
    host.replaceChildren();
    return;
  }

  refreshOffers(() => renderStatus(runs));

  const { tone, main, rest } = buildStatus(runs);
  const line = el("div", "status");
  line.appendChild(el("span", `status-dot ${tone}`));
  line.appendChild(el("span", `status-main${tone === "bad" ? " bad" : ""}`, main));

  if (rest.length) {
    const tail = el("span", "status-rest");
    for (const text of rest) tail.appendChild(el("span", null, text));
    line.appendChild(tail);
  }

  host.replaceChildren(line);
}

export function clearStatus() {
  const host = document.getElementById("status-line");
  if (host) host.replaceChildren();
  offers = { count: null, fetchedAt: 0, inFlight: false };
}
