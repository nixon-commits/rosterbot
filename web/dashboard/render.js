// render.js — shared DOM helpers: HTML-escaping and a generic JSON->DOM
// renderer used for the run-output viewer (arrays of objects -> table, plain
// objects -> key/value rows, everything else -> a simple list/text node).

// el builds a DOM element with an optional class and text content — the
// shared createElement helper every tab module used to carry its own
// byte-identical copy of.
export const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

// escapeHtml escapes a value for interpolation into HTML.
//
// IT ESCAPES QUOTES, and that is the whole reason it is not the two-line
// textContent/innerHTML trick it used to be. That trick delegates to the HTML
// fragment-serialization algorithm, which escapes only &, <, > and U+00A0 —
// never " or '. Every use in a TEXT position was therefore fine, and every use
// in an ATTRIBUTE was a hole: value.js interpolated a Fantrax-supplied logo URL
// into src="…", where one embedded quote closes the attribute and the rest of
// the string becomes markup, onerror= included.
//
// Escaping here rather than only fixing that one call site is deliberate: the
// footgun was that the function's name promised safety it did not deliver, and
// five files interpolate its output. Prefer createElement/textContent for new
// code regardless — see views.js, trades.js, football.js — but a caller that
// does reach for this now gets what the name says.
export function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

// ---- Help bubbles ----------------------------------------------------------
// The dashboard carries a lot of load-bearing explanation: why a verdict was
// withheld, why a total is a floor, why a gap can never be refilled. All of it
// was rendered permanently at full size, which is the right priority for the
// first read and the wrong one for the next hundred.
//
// help() moves the *reasoning* one tap away while the fact it qualifies stays
// on screen. Three rules, in order of how easily they get broken:
//
//  1. Tap, not hover. Hover does not exist on a phone, and the phone is where
//     the screen is tightest — a hover-only bubble hides the text exactly where
//     hiding it costs the most.
//  2. The visible line must stand alone. What is hidden is *why*, never *what*:
//     "Totals are a floor" stays visible, "because unmatched players are
//     counted nowhere" goes in the bubble.
//  3. Plain language, no shorthand. The reader may not be the person who wrote
//     the pipeline.
//
// Built on the native popover API for light-dismiss, Escape handling and
// top-layer stacking without a line of JS. Popovers without CSS anchor
// positioning center in the viewport, which is deliberate here rather than
// merely tolerated: centered reads as a sheet on a phone, and anchor
// positioning is still Chromium-only.
let helpSeq = 0;

export function help(text, label = "Explain this") {
  const id = `help-${++helpSeq}`;

  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "help-btn";
  btn.textContent = "?";
  btn.setAttribute("aria-label", label);
  btn.setAttribute("popovertarget", id);

  const pop = document.createElement("div");
  pop.className = "help-pop";
  pop.id = id;
  pop.setAttribute("popover", "auto");
  pop.textContent = text;

  const wrap = document.createElement("span");
  wrap.className = "help-wrap";
  wrap.append(btn, pop);
  return wrap;
}

// caption builds the "short visible line + reasoning behind a tap" pair these
// views need over and over. Returns a <span class="cap"> ready to append to a
// heading or a row.
export function caption(shortText, longText) {
  const span = document.createElement("span");
  span.className = "cap";
  span.append(document.createTextNode(shortText));
  if (longText) span.append(" ", help(longText));
  return span;
}

// relativeTime renders a coarse "how long ago" for ledger timestamps. Shared by
// the Runs table and the status line so the two never disagree about how old
// the same run is.
export function relativeTime(iso) {
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

export function renderAuto(data) {
  if (data === null || data === undefined) return textNode("(empty)");
  if (Array.isArray(data)) {
    if (data.length === 0) return textNode("(empty list)");
    if (typeof data[0] === "object" && data[0] !== null) return renderTable(data);
    return renderList(data);
  }
  if (typeof data === "object") return renderKeyValue(data);
  return textNode(String(data));
}

function textNode(text) {
  const p = document.createElement("p");
  p.className = "muted";
  p.textContent = text;
  return p;
}

function renderList(items) {
  const ul = document.createElement("ul");
  for (const item of items) {
    const li = document.createElement("li");
    li.textContent = String(item);
    ul.appendChild(li);
  }
  return ul;
}

function renderKeyValue(obj) {
  const table = document.createElement("table");
  table.className = "kv-table";
  for (const [key, value] of Object.entries(obj)) {
    const row = document.createElement("tr");
    const th = document.createElement("th");
    th.textContent = key;
    const td = document.createElement("td");
    if (value !== null && typeof value === "object") {
      td.appendChild(renderAuto(value));
    } else {
      td.textContent = value === null || value === undefined ? "" : String(value);
    }
    row.append(th, td);
    table.appendChild(row);
  }
  return table;
}

function renderTable(rows) {
  // Union of keys across all rows, in first-seen order, so a row missing an
  // optional field doesn't shift columns.
  const cols = [];
  const seen = new Set();
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        cols.push(key);
      }
    }
  }

  const table = document.createElement("table");
  table.className = "data-table";

  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const col of cols) {
    const th = document.createElement("th");
    th.textContent = col;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const row of rows) {
    const tr = document.createElement("tr");
    for (const col of cols) {
      const td = document.createElement("td");
      const value = row[col];
      if (value !== null && typeof value === "object") {
        td.appendChild(renderAuto(value));
      } else {
        td.textContent = value === null || value === undefined ? "" : String(value);
      }
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);

  const wrap = document.createElement("div");
  wrap.className = "table-wrap";
  wrap.appendChild(table);
  return wrap;
}
