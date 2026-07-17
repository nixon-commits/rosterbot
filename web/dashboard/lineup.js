// lineup.js — the home view: today's optimized lineup (GET /v1/lineup/today).
import { api, ApiError } from "./api.js";
import { escapeHtml } from "./render.js";

export async function renderLineup(root) {
  root.innerHTML = "<p class=\"muted\">Loading today's lineup…</p>";
  let data;
  try {
    data = await api.lineupToday();
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";

  const header = document.createElement("div");
  header.className = "card";
  header.innerHTML = `
    <h2>${escapeHtml(data.date)}</h2>
    <p>Projected: <strong>${data.projected_points.toFixed(1)}</strong> pts</p>
  `;
  root.appendChild(header);

  if (data.warnings && data.warnings.length > 0) {
    const warn = document.createElement("div");
    warn.className = "card";
    warn.innerHTML = "<strong>Warnings</strong>";
    const ul = document.createElement("ul");
    for (const w of data.warnings) {
      const li = document.createElement("li");
      li.textContent = w;
      ul.appendChild(li);
    }
    warn.appendChild(ul);
    root.appendChild(warn);
  }

  const grid = document.createElement("div");
  grid.className = "slot-grid";
  for (const slot of data.slots) {
    grid.appendChild(slotCard(slot));
  }
  root.appendChild(grid);
}

function slotCard(slot) {
  const card = document.createElement("div");
  card.className = "card";
  if (!slot.player) {
    card.innerHTML = `<div class="muted">${escapeHtml(slot.slot)}</div><div class="muted">— empty —</div>`;
    return card;
  }
  const p = slot.player;
  card.innerHTML = `
    <div class="muted">${escapeHtml(slot.slot)}</div>
    <div><strong>${escapeHtml(p.name)}</strong> <span class="badge badge-${p.status.toLowerCase()}">${escapeHtml(p.status)}</span></div>
    <div class="muted">${escapeHtml(p.team)} · ${escapeHtml(p.pos.join("/"))}</div>
    <div>${p.proj.toFixed(1)} proj pts</div>
  `;
  return card;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  if (err instanceof ApiError && err.status === 404) {
    card.innerHTML = "<p class=\"muted\">No lineup published yet — the hourly optimize run hasn't run today.</p>";
  } else {
    card.innerHTML = `<p class="error">Failed to load lineup: ${escapeHtml(err.message)}</p>`;
  }
  return card;
}
