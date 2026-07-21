// runs.js — run history (polls faster while anything is RUNNING, slower while
// idle), a per-run detail + output viewer, and the notifications activity feed.
import { api, ApiError } from "./api.js";
import { renderAuto, escapeHtml } from "./render.js";

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
  pollTimer = setInterval(async () => {
    const nextRuns = await loadRuns(runsList, detailSection, /* silent */ true);
    schedulePoll(runsList, detailSection, nextRuns);
  }, intervalMs);
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer);
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
  table.innerHTML = "<thead><tr><th>Command</th><th>Status</th><th>Started</th><th>Duration</th><th>Trigger</th></tr></thead>";
  const tbody = document.createElement("tbody");
  for (const run of runs) {
    const tr = document.createElement("tr");
    tr.style.cursor = "pointer";
    tr.innerHTML = `
      <td>${escapeHtml(run.command)}</td>
      <td><span class="badge badge-${escapeHtml(run.status.toLowerCase())}">${escapeHtml(run.status)}</span></td>
      <td>${escapeHtml(relativeTime(run.started_at))}</td>
      <td>${escapeHtml(runDuration(run))}</td>
      <td>${escapeHtml(run.trigger)}</td>
    `;
    tr.addEventListener("click", () => showDetail(detailSection, run.id));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  container.appendChild(table);
  return runs;
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

// Coarse relative time for the "Started" column ("just now" / "N min ago" /
// "N hr ago" / "N days ago").
function relativeTime(iso) {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return String(iso ?? "");
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hr ago`;
  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
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
