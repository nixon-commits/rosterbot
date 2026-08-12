// lineup.js — the home view: today's optimized lineup (GET /v1/lineup/today).
//
// Rendered as one row per slot rather than one card per slot. A card is a lot
// of frame for four short fields, and at 19 slots it cost 2841px — 3.5 screens
// on a phone — to read a lineup you mostly scan for anomalies. Rows put the
// whole lineup in about one screen and let the exceptions carry the emphasis.
//
// Built with createElement/textContent: player and team names come from
// Fantrax, and render.js's escapeHtml does not escape quotes, so it is unsafe
// in an attribute context (the footgun views.js and trades.js both document).
import { api, ApiError } from "./api.js";

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

// A slot needs attention when it is empty, when its player is not in a normal
// active state, or when a filled slot is projected to score nothing — the last
// one catches a player whose team has no game, which no status flag reports.
function slotAlert(slot) {
  if (!slot.player) return "empty";
  const status = String(slot.player.status || "").toUpperCase();
  if (status && status !== "OK" && status !== "ACTIVE") return status.toLowerCase();
  if (!slot.player.proj) return "zero";
  return "";
}

function slotRow(slot) {
  const alert = slotAlert(slot);
  const row = el("li", `lineup-row${alert ? " alert" : ""}`);
  row.appendChild(el("span", "lineup-slot", slot.slot));

  if (!slot.player) {
    const name = el("span", "lineup-name lineup-empty", "empty");
    row.appendChild(name);
    row.appendChild(el("span", "lineup-meta"));
    row.appendChild(el("span", "lineup-proj", "—"));
    return row;
  }

  const p = slot.player;
  const nameCell = el("span", "lineup-name");
  nameCell.appendChild(el("span", null, p.name));
  // The status badge only appears when the status is worth reading. Printing
  // "OK" on sixteen of nineteen rows is noise that hides the three that matter.
  if (alert && alert !== "zero") {
    nameCell.appendChild(el("span", `badge badge-${alert}`, p.status));
  }
  row.appendChild(nameCell);

  const meta = el("span", "lineup-meta");
  meta.appendChild(el("span", "lineup-team", p.team));
  meta.appendChild(el("span", "lineup-pos", (p.pos || []).join("/")));
  row.appendChild(meta);

  const proj = el("span", "lineup-proj", p.proj.toFixed(1));
  if (!p.proj) proj.classList.add("muted");
  row.appendChild(proj);
  return row;
}

export async function renderLineup(root) {
  root.replaceChildren(el("p", "muted", "Loading today's lineup…"));
  let data;
  try {
    data = await api.lineupToday();
  } catch (err) {
    root.replaceChildren(errorCard(err));
    return;
  }
  root.replaceChildren();

  const head = el("div", "lineup-head");
  head.appendChild(el("h2", null, data.date));
  const pts = el("div", "lineup-total");
  pts.appendChild(el("strong", null, data.projected_points.toFixed(1)));
  pts.appendChild(el("span", "muted", "expected points"));
  head.appendChild(pts);
  root.appendChild(head);

  if (data.warnings && data.warnings.length > 0) {
    const box = el("div", "lineup-warnings");
    const list = el("ul");
    for (const w of data.warnings) list.appendChild(el("li", null, w));
    box.appendChild(list);
    root.appendChild(box);
  }

  const flagged = (data.slots || []).filter((s) => slotAlert(s)).length;
  if (flagged) {
    root.appendChild(el("p", "sub muted",
      `${flagged} of ${data.slots.length} slots need a look — marked below.`));
  }

  const list = el("ol", "lineup-list");
  for (const slot of data.slots) list.appendChild(slotRow(slot));
  root.appendChild(list);
}

function errorCard(err) {
  const card = el("div", "card");
  if (err instanceof ApiError && err.status === 404) {
    card.appendChild(el("p", "muted",
      "No lineup published yet — the hourly optimize run hasn't run today."));
  } else {
    card.appendChild(el("p", "error", `Failed to load lineup: ${err.message}`));
  }
  return card;
}
