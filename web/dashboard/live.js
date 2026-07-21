// live.js — background poller for in-flight runs. Shows a "Now Running" hero
// with a phased progress bar (or an indeterminate bar for jobs that emit no
// progress.json), fires a toast when a run finishes, and badges the Runs nav.
import { api, ApiError } from "./api.js";

const RUNS_POLL_MS = 5000;
const PROG_POLL_MS = 2000;
const MAX_RUN_MS = 2 * 60 * 60 * 1000; // mirrors backend maxJobDuration (2h)

let runsTimer = null;
let progTimer = null;
let watchedId = null;
const lastStatus = new Map(); // run id -> last seen status, for completion toasts

const heroEl = () => document.getElementById("live-hero");
const badgeEl = () => document.querySelector('nav a[href="#runs"]');

function isLive(run) {
  if (run.status !== "RUNNING") return false;
  const started = Date.parse(run.started_at);
  return !Number.isNaN(started) && Date.now() - started < MAX_RUN_MS;
}

export function toast(msg, kind = "ok") {
  const host = document.getElementById("toasts");
  if (!host) return;
  const t = document.createElement("div");
  t.className = `toast toast-${kind}`;
  t.textContent = msg;
  host.appendChild(t);
  setTimeout(() => t.remove(), 5000);
}

async function pollRuns() {
  let runs = [];
  try {
    const res = await api.runs(25);
    runs = res.runs || [];
  } catch {
    schedule();
    return;
  }
  // Completion detection: any id that was RUNNING last tick and is now terminal.
  for (const r of runs) {
    const prev = lastStatus.get(r.id);
    if (prev === "RUNNING" && r.status !== "RUNNING") {
      toast(`${r.command.split(" ")[0]} ${r.status === "SUCCESS" ? "finished" : "failed"}`, r.status === "SUCCESS" ? "ok" : "fail");
    }
    lastStatus.set(r.id, r.status);
  }
  const live = runs.filter(isLive);
  const badge = badgeEl();
  if (badge) badge.classList.toggle("has-live", live.length > 0);

  const target = live.find((r) => r.id === watchedId) || live[0];
  if (target) {
    renderHero(target);
    pollProgress(target.id);
  } else {
    clearHero();
  }
  schedule();
}

function schedule() {
  clearTimeout(runsTimer);
  runsTimer = setTimeout(pollRuns, RUNS_POLL_MS);
}

async function pollProgress(id) {
  clearTimeout(progTimer);
  let snap = null;
  try {
    snap = await api.runProgress(id);
  } catch (err) {
    // 404 => job emits no phases; leave hero indeterminate.
    if (!(err instanceof ApiError) || err.status !== 404) { /* transient */ }
  }
  updateHeroProgress(id, snap);
  progTimer = setTimeout(() => pollProgress(id), PROG_POLL_MS);
}

function renderHero(run) {
  const host = heroEl();
  if (!host) return;
  if (host.dataset.runId === run.id) return; // already showing; progress updates in place
  host.dataset.runId = run.id;
  host.innerHTML = `
    <div class="hero card">
      <div class="hero-head"><span class="badge badge-running">RUNNING</span>
        <strong>${run.command}</strong><span class="muted hero-elapsed"></span></div>
      <div class="progress"><div class="progress-fill" style="width:0%"></div></div>
      <ol class="phases"></ol>
    </div>`;
  startElapsed(host, run.started_at);
}

function updateHeroProgress(id, snap) {
  const host = heroEl();
  if (!host || host.dataset.runId !== id) return;
  const fill = host.querySelector(".progress-fill");
  const phases = host.querySelector(".phases");
  if (!snap) { // indeterminate
    host.querySelector(".progress").classList.add("indeterminate");
    return;
  }
  host.querySelector(".progress").classList.remove("indeterminate");
  if (fill) fill.style.width = `${snap.pct}%`;
  if (phases) {
    phases.innerHTML = (snap.phases || [])
      .map((p) => `<li class="phase phase-${p.state}">${p.name}</li>`)
      .join("");
  }
}

let elapsedTimer = null;
function startElapsed(host, startedAt) {
  clearInterval(elapsedTimer);
  const started = Date.parse(startedAt);
  const tick = () => {
    const s = Math.max(0, Math.floor((Date.now() - started) / 1000));
    const el = host.querySelector(".hero-elapsed");
    if (el) el.textContent = `  ${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  };
  tick();
  elapsedTimer = setInterval(tick, 1000);
}

function clearHero() {
  const host = heroEl();
  if (host) { host.innerHTML = ""; delete host.dataset.runId; }
  clearTimeout(progTimer);
  clearInterval(elapsedTimer);
  watchedId = null;
}

export function watchRun(id) { watchedId = id; pollRuns(); }
export function startLive() { pollRuns(); }
