// runs.js — run history (polls faster while anything is RUNNING, slower while
// idle), a per-run detail + output viewer, and the notifications activity feed.
import { api, ApiError } from "./api.js";
import { renderAuto, escapeHtml, relativeTime } from "./render.js";
import { CONNECT_CHIP, FAILURE_COPY } from "./connstate.js";

const POLL_MS = 5000;
const IDLE_POLL_MS = 30000;
let pollTimer = null;

export async function renderRuns(root) {
  stopPolling();
  root.innerHTML = "";

  const runsSection = document.createElement("section");
  runsSection.innerHTML = "<h2>Recent runs</h2>";
  const runsList = document.createElement("div");
  runsSection.appendChild(runsList);
  root.appendChild(runsSection);

  const detailSection = document.createElement("section");
  detailSection.id = "run-detail";
  root.appendChild(detailSection);

  const notifSection = document.createElement("section");
  notifSection.innerHTML = "<h2>Activity</h2>";
  const notifList = document.createElement("div");
  notifSection.appendChild(notifList);
  root.appendChild(notifSection);

  let active = true;
  const onNavigateAway = () => {
    active = false;
    stopPolling();
  };
  // Stop polling once the user navigates away from this view.
  window.addEventListener("hashchange", onNavigateAway, { once: true });

  const [runs] = await Promise.all([
    loadRuns(runsList, detailSection),
    loadNotifications(notifList),
  ]);

  if (!active) return; // user navigated away during the initial load; don't start polling
  schedulePoll(runsList, detailSection, runs);
}

// Polls fast (POLL_MS) while a run is active or the last load failed (so a
// transient error self-heals instead of freezing), and slow (IDLE_POLL_MS)
// otherwise so a run started elsewhere is still picked up without hammering
// the API while idle.
function schedulePoll(runsList, detailSection, runs) {
  stopPolling();
  const intervalMs = runs === null || hasRunningRun(runs) ? POLL_MS : IDLE_POLL_MS;
  pollTimer = setTimeout(async () => {
    const nextRuns = await loadRuns(runsList, detailSection, /* silent */ true);
    schedulePoll(runsList, detailSection, nextRuns);
  }, intervalMs);
}

function stopPolling() {
  if (pollTimer) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}

async function loadRuns(container, detailSection, silent) {
  let runs;
  try {
    const resp = await api.runs();
    runs = resp.runs;
  } catch (err) {
    if (!silent) container.innerHTML = `<p class="error">Failed to load runs: ${escapeHtml(err.message)}</p>`;
    return null;
  }
  container.innerHTML = "";
  if (runs.length === 0) {
    container.innerHTML = "<p class=\"muted\">No runs yet.</p>";
    return runs;
  }
  const table = document.createElement("table");
  table.className = "data-table";
  // Duration and Trigger are marked secondary: on a phone the five columns
  // needed 447px inside a 343px window, so the table scrolled sideways and you
  // could never see a command and its status at once. Both are shown in the
  // run detail that a tap on the row already opens, so hiding them narrow
  // costs nothing — where dropping Command or Status would.
  table.innerHTML = "<thead><tr><th>Command</th><th>Status</th><th>Started</th>" +
    "<th class=\"col-secondary\">Duration</th><th class=\"col-secondary\">Trigger</th></tr></thead>";
  const tbody = document.createElement("tbody");
  for (const run of runs) {
    const tr = document.createElement("tr");
    tr.style.cursor = "pointer";
    tr.innerHTML = `
      <td>${escapeHtml(run.command)}</td>
      <td><span class="badge badge-${escapeHtml(run.status.toLowerCase())}">${escapeHtml(run.status)}</span></td>
      <td>${escapeHtml(relativeTime(run.started_at))}</td>
      <td class="col-secondary">${escapeHtml(runDuration(run))}</td>
      <td class="col-secondary">${escapeHtml(run.trigger)}</td>
    `;
    // Appended into the Status cell rather than added as a column: a fifth
    // column already overflowed a 343px window (see the header comment above).
    const chip = connectChip(run);
    if (chip) tr.cells[1].append(" ", chip);
    tr.addEventListener("click", () => showDetail(detailSection, run.id));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  const wrap = document.createElement("div");
  wrap.className = "table-wrap";
  wrap.appendChild(table);
  container.appendChild(wrap);
  return runs;
}

// connectChip renders what the CONNECT TASK concluded, beside the ledger status
// it contradicts.
//
// A connect that fails for a reason only the tenant can fix exits 0 on purpose,
// so opsalert does not page the operator for something the operator cannot fix
// — which makes the Status cell read SUCCESS above an Activity entry saying the
// connection failed (rosterbot-jg92). The verdict is the other half.
//
// ABSENT MEANS "NOT ATTRIBUTABLE", NEVER "SUCCEEDED": the API omits `connect`
// unless the tenant's connection record names this exact run, so an older
// connect row and every non-connect row correctly say nothing.
//
// Coloured on `verdict` alone. The tenant's live connection status is
// deliberately not on the wire: on the operator-actionable route the connect
// task leaves it untouched, so it can still read "verified" on a run that
// failed and paged.
//
// Built with createElement/textContent rather than a template string because
// last_error is a server string that reaches a title attribute — render.js's
// escapeHtml does cover quotes now, but that comment still asks new code to use
// the DOM API.
function connectChip(run) {
  const c = run.connect;
  if (!c) return null;
  const span = document.createElement("span");
  if (c.verdict === "verified") {
    span.className = "badge badge-ok";
    span.textContent = "connected";
    return span;
  }
  span.className = "badge badge-failed";
  // An unrecognised class falls back to the raw string, and an outcome with no
  // class at all still says the connection failed: degrade to noise, never to
  // silence.
  const why = c.last_error ? ": " + (CONNECT_CHIP[c.last_error] || c.last_error) : "";
  span.textContent = "connection failed" + why;
  if (c.last_error && FAILURE_COPY[c.last_error]) span.title = FAILURE_COPY[c.last_error];
  return span;
}

function hasRunningRun(runs) {
  return Array.isArray(runs) && runs.some((run) => run.status === "RUNNING");
}

// m:ss for anything a minute or over, else "Xs" — mirrors the compact style
// used elsewhere in the dashboard (e.g. live.js's hero elapsed counter).
function formatDuration(ms) {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

// Duration column: terminal runs get a fixed ended_at - started_at; a still
// RUNNING run gets elapsed-so-far computed at render time (not ticking — the
// live hero already covers a real-time counter for the currently-watched run).
function runDuration(run) {
  const started = Date.parse(run.started_at);
  if (Number.isNaN(started)) return "";
  if (run.status === "RUNNING") {
    return formatDuration(Date.now() - started);
  }
  const ended = Date.parse(run.ended_at);
  if (Number.isNaN(ended)) return "";
  return formatDuration(ended - started);
}

async function showDetail(section, id) {
  section.innerHTML = "<p class=\"muted\">Loading…</p>";
  let detail;
  try {
    detail = await api.runDetail(id);
  } catch (err) {
    section.innerHTML = `<p class="error">Failed to load run: ${escapeHtml(err.message)}</p>`;
    return;
  }
  section.innerHTML = "";
  const card = document.createElement("div");
  card.className = "card";
  card.innerHTML = `
    <h3>${escapeHtml(detail.command)}</h3>
    <p>Status: <span class="badge badge-${escapeHtml(detail.status.toLowerCase())}">${escapeHtml(detail.status)}</span> · Trigger: ${escapeHtml(detail.trigger)}</p>
    <p class="muted">Started ${escapeHtml(detail.started_at)}${detail.ended_at ? " · Ended " + escapeHtml(detail.ended_at) : ""}</p>
  `;
  const chip = connectChip(detail);
  if (chip) {
    const p = document.createElement("p");
    p.append(chip);
    card.append(p);
  }
  if (detail.log_tail) {
    const pre = document.createElement("pre");
    pre.textContent = detail.log_tail;
    card.appendChild(pre);
  }
  section.appendChild(card);

  try {
    const output = await api.runOutput(id);
    const outCard = document.createElement("div");
    outCard.className = "card";
    outCard.innerHTML = `<h3>Output (${escapeHtml(output.type)})</h3>`;
    outCard.appendChild(renderAuto(output.data));
    section.appendChild(outCard);
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 404)) {
      const errCard = document.createElement("div");
      errCard.className = "card";
      errCard.innerHTML = `<p class="error">Failed to load output: ${escapeHtml(err.message)}</p>`;
      section.appendChild(errCard);
    }
    // 404 just means this run's job type doesn't record typed output
    // (optimize, recap-site) — not an error worth surfacing.
  }
}

async function loadNotifications(container) {
  let notifs;
  try {
    const resp = await api.notifications();
    notifs = resp.notifications;
  } catch (err) {
    container.innerHTML = `<p class="error">Failed to load activity: ${escapeHtml(err.message)}</p>`;
    return;
  }
  container.innerHTML = "";
  if (notifs.length === 0) {
    container.innerHTML = "<p class=\"muted\">No activity yet.</p>";
    return;
  }
  for (const n of notifs) {
    const card = document.createElement("div");
    card.className = "card";
    card.innerHTML = `
      <div><span class="badge badge-${escapeHtml(n.status)}">${escapeHtml(n.status)}</span> <span class="muted">${escapeHtml(n.kind)}</span> <strong>${escapeHtml(n.title)}</strong></div>
      <p>${escapeHtml(n.message)}</p>
      <p class="muted">${escapeHtml(n.created_at)}</p>
    `;
    container.appendChild(card);
  }
}
