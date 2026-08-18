// football.js — dynasty football standings. Fetches football.json
// (internal/dynasty.Model) and renders each team's Starters Value (the
// headline) beside its full-roster total (visually demoted), with a
// client-side StatsGuy-format toggle and join-coverage counts — the gate
// pre-commitment from the 2026-08-10 coverage-gate spike (see CLAUDE.md's
// Dynasty Football section): starters coverage is consistently
// near-complete, full-roster coverage isn't, so the headline number must be
// the one the join can actually support.
//
// Rows are built with createElement/textContent, not innerHTML, following
// views.js's rule (render.js's escapeHtml escapes &<> but not quotes, so it
// is not safe for attribute contexts) — team names here come from Sleeper
// display names, not vetted input, so the same discipline applies even
// though the immediate risk is lower than views.js's attacker-supplied URIs.
import { api } from "./api.js";

const FORMATS = [
  ["sf_dynasty", "SF Dynasty"],
  ["non_sf_dynasty", "Non-SF Dynasty"],
  ["sf_redraft", "SF Redraft"],
  ["non_sf_redraft", "Non-SF Redraft"],
];

const fmt = (n) => (typeof n === "number" ? n.toLocaleString() : "—");

function startersKey(format) {
  return "starters" + toPascal(format);
}
function fullRosterKey(format) {
  return "fullRoster" + toPascal(format);
}
function toPascal(format) {
  // "sf_dynasty" -> "SfDynasty", matching the JSON tags dynasty.TeamStanding carries.
  return format
    .split("_")
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join("");
}

export async function renderFootball(root) {
  root.replaceChildren();
  const loading = document.createElement("p");
  loading.className = "muted";
  loading.textContent = "Loading dynasty football standings…";
  root.appendChild(loading);

  let model;
  try {
    model = await api.reportFootball();
  } catch (err) {
    card(root, "Dynasty football", ["No dynasty football data yet."]);
    return;
  }

  const teams = Array.isArray(model.teams) ? model.teams : [];
  if (model.empty || teams.length === 0) {
    card(root, "Dynasty football", [
      "Collecting dynasty football data — check back after the next daily run.",
    ]);
    return;
  }

  const state = { format: "sf_dynasty" };
  const el = buildLayout(root, model);
  wireFormatSeg(el, state, () => paintStandings(el, model, state));
  paintStandings(el, model, state);
}

function buildLayout(root, model) {
  root.replaceChildren();

  const wrap = document.createElement("div");
  wrap.className = "football";

  const sub = document.createElement("div");
  sub.className = "sub muted";
  sub.textContent = `${model.teams.length} teams · ${model.latestDate}`;
  wrap.appendChild(sub);

  const controls = document.createElement("div");
  controls.className = "controls";
  const seg = document.createElement("div");
  seg.className = "toggle-group";
  controls.appendChild(seg);
  wrap.appendChild(controls);

  const section = document.createElement("section");
  section.className = "card";
  const h2 = document.createElement("h2");
  h2.textContent = `Standings — ${model.latestDate}`;
  section.appendChild(h2);
  const p = document.createElement("p");
  p.className = "sub muted";
  p.textContent =
    "Starters Value leads: it's what the StatsGuy join can reliably price. " +
    "Full Roster is shown for reference, alongside how many rostered players matched a value.";
  section.appendChild(p);
  const tableHost = document.createElement("div");
  tableHost.className = "table-wrap";
  section.appendChild(tableHost);
  wrap.appendChild(section);

  root.appendChild(wrap);
  return { seg, tableHost };
}

function wireFormatSeg(el, state, rerender) {
  el.seg.replaceChildren();
  for (const [key, label] of FORMATS) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = label;
    b.dataset.key = key;
    b.classList.toggle("active", key === state.format);
    b.addEventListener("click", () => {
      state.format = key;
      el.seg.querySelectorAll("button").forEach((btn) => btn.classList.toggle("active", btn.dataset.key === key));
      rerender();
    });
    el.seg.appendChild(b);
  }
}

function numCell(text, extraClass) {
  const td = document.createElement("td");
  td.className = extraClass ? `num ${extraClass}` : "num";
  td.textContent = text;
  return td;
}

function coverageCell(matched, rostered) {
  const td = document.createElement("td");
  td.className = "num cov" + (matched < rostered ? " warn" : "");
  td.textContent = `${matched}/${rostered}`;
  return td;
}

function paintStandings(el, model, state) {
  const sKey = startersKey(state.format);
  const fKey = fullRosterKey(state.format);

  const sorted = model.teams.slice().sort((a, b) => {
    if (b[sKey] !== a[sKey]) return b[sKey] - a[sKey];
    return String(a.teamId).localeCompare(String(b.teamId));
  });

  const table = document.createElement("table");
  table.className = "data-table";

  const thead = document.createElement("thead");
  const hrow = document.createElement("tr");
  for (const [text, cls] of [
    ["Team", ""],
    ["Starters Value", "num"],
    ["Full Roster", "num"],
    ["Matched", "num"],
  ]) {
    const th = document.createElement("th");
    if (cls) th.className = cls;
    th.textContent = text;
    hrow.appendChild(th);
  }
  thead.appendChild(hrow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  sorted.forEach((t, i) => {
    const tr = document.createElement("tr");
    if (i === 0) tr.className = "lead";

    const nameTd = document.createElement("td");
    const teamWrap = document.createElement("div");
    teamWrap.className = "team";
    const nameSpan = document.createElement("span");
    nameSpan.textContent = t.teamName || t.teamId;
    teamWrap.appendChild(nameSpan);
    nameTd.appendChild(teamWrap);
    tr.appendChild(nameTd);

    tr.appendChild(numCell(fmt(t[sKey])));
    tr.appendChild(numCell(fmt(t[fKey]), "muted"));
    tr.appendChild(coverageCell(t.matchedCount, t.rosteredCount));

    tbody.appendChild(tr);
  });
  table.appendChild(tbody);

  el.tableHost.replaceChildren(table);
}

// card paints a simple message shell and returns it, mirroring views.js.
function card(root, heading, paragraphs) {
  root.replaceChildren();
  const box = document.createElement("div");
  box.className = "card";
  const h = document.createElement("h2");
  h.textContent = heading;
  box.appendChild(h);
  for (const text of paragraphs) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = text;
    box.appendChild(p);
  }
  root.appendChild(box);
  return box;
}
