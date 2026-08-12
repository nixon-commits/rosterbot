// views.js — recap-site readership. Fetches views.json (internal/recaplog) and
// renders the recent page reads: when, from what IP, which page, and the
// CloudFront edge that served it.
//
// The recap site is public and unauthenticated, so a client IP is the only
// identifier there is — it names a network, not a person. The view says so
// rather than implying a headcount, and the edge location is a coarse metro
// hint (the POP that served the request), not a geolocation of the reader.
//
// SECURITY: uri comes from CloudFront request logs, so it is attacker-supplied
// — anyone on the internet can request a crafted path against a public site and
// have it land in this table. Rows are therefore built with createElement and
// textContent rather than interpolated into innerHTML. render.js's escapeHtml
// escapes &<> but not quotes, so it is not safe for attribute contexts; using
// DOM text nodes removes that footgun entirely instead of depending on every
// future edit getting the escaping right.
import { api } from "./api.js";
import { help } from "./render.js";

// Edge codes are <AIRPORT><digits>[-P<n>], e.g. SFO5-P3. The airport prefix is
// the only stable, meaningful part; the rest identifies the POP instance.
function edgeAirport(code) {
  const m = /^([A-Z]{3})/.exec(code || "");
  return m ? m[1] : code || "—";
}

function fmtWhen(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso ?? "—");
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// "/" is the distribution's DefaultRootObject; show it as the page it serves
// rather than as a bare slash.
function fmtPage(uri) {
  return uri === "/" ? "index.html" : String(uri || "").replace(/^\//, "");
}

function cell(text, { mono = false, title = "" } = {}) {
  const td = document.createElement("td");
  if (mono) td.className = "mono";
  if (title) td.title = title; // property assignment, not attribute interpolation
  td.textContent = text;
  return td;
}

function buildTable(hits) {
  const table = document.createElement("table");

  const thead = document.createElement("thead");
  const hrow = document.createElement("tr");
  for (const label of ["When", "IP", "Page", "Edge"]) {
    const th = document.createElement("th");
    th.textContent = label;
    hrow.appendChild(th);
  }
  thead.appendChild(hrow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const h of hits) {
    const tr = document.createElement("tr");
    tr.appendChild(cell(fmtWhen(h.time), { mono: true }));
    tr.appendChild(cell(h.ip || "—", { mono: true }));
    tr.appendChild(cell(fmtPage(h.uri)));
    tr.appendChild(cell(edgeAirport(h.edge), { mono: true, title: h.edge || "" }));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  return table;
}

export async function renderViews(root) {
  root.replaceChildren();
  const loading = document.createElement("p");
  loading.className = "muted";
  loading.textContent = "Loading readership…";
  root.appendChild(loading);

  let model;
  try {
    model = await api.reportViews();
  } catch (err) {
    card(root, "Recap readership", [
      "No readership data yet — access logging publishes on the next daily run.",
    ]);
    return;
  }

  // A freshly deployed log prefix is genuinely empty. That is "nobody has
  // opened it yet", not a failure, and must not read as an error.
  const hits = Array.isArray(model.hits) ? model.hits : [];
  if (model.empty || hits.length === 0) {
    card(root, "Recap readership", ["No page reads recorded yet."]);
    return;
  }

  const uniqueIps = new Set(hits.map((h) => h.ip)).size;
  // "Addresses, not people" stays visible because it is the difference between
  // reading this table correctly and reading it as a headcount. The mechanics
  // of why go behind the bubble.
  const box = card(root, "Recap readership", [
    `${hits.length.toLocaleString()} recent page read${hits.length === 1 ? "" : "s"} ` +
      `from ${uniqueIps.toLocaleString()} address${uniqueIps === 1 ? "" : "es"} · ` +
      `collected ${fmtWhen(model.generatedAt)}`,
  ]);

  const note = document.createElement("p");
  note.className = "sub muted";
  note.append("Addresses, not people ", help(
    "The recap site is public, so there is no sign-in to attribute a visit to — " +
    "a network address is all the log records.\n\n" +
    "One household or office reads as a single address no matter how many " +
    "people opened it, and the same person on mobile data appears as a " +
    "different one. Edge is the CloudFront location that served the request, a " +
    "rough metro hint rather than the reader's location.",
    "Why these are addresses, not people",
  ));
  box.appendChild(note);

  const wrap = document.createElement("div");
  wrap.className = "table-wrap";
  wrap.appendChild(buildTable(hits));
  box.appendChild(wrap);
}

// card paints the shell and returns it so callers can append to it.
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
