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
import { help } from "./render.js";

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

// slotState names what is true about a slot: empty, a player status that isn't
// plain active, or a filled slot projected to score nothing — the last one
// catches a player whose team has no game, which no status flag reports.
function slotState(slot) {
  if (!slot.player) return "empty";
  const status = String(slot.player.status || "").toUpperCase();
  if (status && status !== "OK" && status !== "ACTIVE") return status.toLowerCase();
  if (!slot.player.proj) return "zero";
  return "";
}

// Not every non-active state is a problem, and conflating the two was what made
// this page read as a wall of red. LOCKED means the player's game has started
// and the lineup is frozen — routine, and by the evening it is most of the
// board (21 of 36 slots on 2026-08-13). Marking it the same way as BENCHED gave
// the majority of rows an anomaly treatment calibrated, per the stripe's own
// comment, for "three rows that need attention among nineteen" — so the marker
// stopped marking anything and the header claimed 21 slots needed a look when
// one did.
//
// A state earns row emphasis only if the manager can still act on it. Every
// state still gets its tag; this decides which ones also get the stripe, and
// it is the single predicate behind both the stripe and the header count, so
// the two can never disagree about what "needs a look" means.
const INFORMATIONAL_STATES = new Set(["locked"]);

function slotNeedsAttention(slot) {
  const state = slotState(slot);
  return state !== "" && !INFORMATIONAL_STATES.has(state);
}

// ---- Heatmap scales --------------------------------------------------------
// Age and HKB value are both magnitude, so each gets a sequential single-hue
// ramp (light -> dark), never a rainbow and never a diverging pair: neither has
// a meaningful midpoint to diverge around. Both ramps run in the same direction
// — more is darker — so one rule covers the whole row; the legend states which
// end is which rather than leaving the reader to infer that older is "worse".
//
// The domain is the players on THIS page, not an absolute league-wide range.
// A lineup's HKB values routinely sit inside a narrow band, and an absolute
// 0..10000 domain would paint all of them the same shade — which is a scale
// that answers no question. The trade-off is that shading is not comparable
// across days, so the legend prints the domain it is actually using.
//
// Color never carries a fact on its own here: every shaded cell prints its own
// number, so the ramp is redundant encoding for anyone who cannot separate the
// steps.
const MAX_TINT = 26; // percent; the dark-mode hue lives in style.css

// Age is shaded linearly and HKB value on a square root, because the two
// distributions are not the same shape. A lineup's ages span maybe 23 to 34 and
// spread evenly across it, so linear uses the whole ramp. HKB values are
// heavily right-skewed — one franchise bat can be worth more than the next six
// players together — and under a linear ramp that single player takes the dark
// end and everyone below him collapses into the same near-white step, which is
// a scale that answers nothing about the fifteen rows the reader is comparing.
// The square root keeps the order and still puts the elite asset on top; what
// it gives up is proportionality, so the help text says so rather than letting
// the reader assume twice the tint means twice the value.
function scaleFor(values, transform = (v) => v) {
  const nums = values.filter((v) => typeof v === "number" && v > 0);
  if (nums.length === 0) return null;
  const min = Math.min(...nums);
  const max = Math.max(...nums);
  const lo = transform(min);
  const hi = transform(max);
  return {
    min,
    max,
    // A constant domain gets no tint at all. Shading it would draw a gradient
    // over a distinction that does not exist.
    t: (v) => (hi > lo ? (transform(v) - lo) / (hi - lo) : 0),
  };
}

// heatCell renders one shaded numeric cell, or a muted em-dash when the value
// is missing. Zero means "HKB has no row for this player" on both fields (see
// lineupapi.Player), so it is rendered as unknown rather than as a low reading.
function heatCell(cls, value, scale, format) {
  if (!value) {
    const n = el("span", `${cls} muted`, "—");
    n.title = "not in HKB";
    return n;
  }
  const n = el("span", cls, format(value));
  if (scale) {
    n.style.setProperty("--heat-pct", `${(scale.t(value) * MAX_TINT).toFixed(1)}%`);
  }
  return n;
}

const fmtAge = (v) => v.toFixed(1);
const fmtValue = (v) => v.toLocaleString();

// ---- Sorting ---------------------------------------------------------------
// One table drives both the header and the comparator, so a column cannot be
// rendered without being sortable or sorted without being rendered.
//
// `get` returns the value to order by, and returning `undefined` is how a cell
// declares itself UNKNOWN. Which zeros count as unknown differs by column, and
// the split is the same one heatCell already makes:
//
//   age, hkb_value  0 means "HKB has no row for this player" (see
//                   lineupapi.Player — both fields are omitempty), which is a
//                   missing measurement, not a low one. The cell renders as a
//                   muted dash, so it sorts as unknown.
//   proj            0 is a genuine reading — a player whose team has no game
//                   today. The cell prints "0.0", so it sorts as the low
//                   number it is. Only a slot with no player at all is unknown.
//   slot            never unknown; it is the row's index in the published
//                   lineup, so ascending reproduces the lineup card exactly
//                   and doubles as the reset back to the default view.
// `num` picks the comparison, `first` the direction of the FIRST click on that
// column. The two are deliberately separate: numeric columns open biggest-first
// because that is the question being asked of them, but Slot is numeric and
// must open ascending — ascending Slot IS the published lineup, and opening it
// reversed would make the one control that gets you back to the default view
// the one that does not.
const COLUMNS = [
  { key: "slot",  label: "Slot",   cls: "lineup-slot",  num: true,  first: "asc",  get: (s, i) => i },
  { key: "name",  label: "Player", cls: "lineup-name",  num: false, first: "asc",  get: (s) => s.player?.name },
  { key: "team",  label: "Team",   cls: "lineup-meta",  num: false, first: "asc",  get: (s) => s.player?.team },
  { key: "age",   label: "Age",    cls: "lineup-age",   num: true,  first: "desc", get: (s) => s.player?.age || undefined },
  { key: "value", label: "HKB",    cls: "lineup-value", num: true,  first: "desc", get: (s) => s.player?.hkb_value || undefined },
  { key: "proj",  label: "Proj",   cls: "lineup-proj",  num: true,  first: "desc", get: (s) => s.player?.proj },
];

// compareValues orders two cells of one column. It is the whole sort rule.
//
// Contract:
//   - Return <0 to put `a` above `b`, >0 to put it below, 0 for a tie.
//     Array.prototype.sort is stable, so ties keep lineup order for free.
//   - `dir` is "asc" or "desc", and flips the order of two KNOWN values.
//   - `undefined` is unknown: a missing HKB row, a missing age, or an empty
//     slot. An unknown always sorts BELOW a known value in BOTH directions,
//     so the top of the list stays meaningful whichever way you sort. Two
//     unknowns tie.
//   - `num` picks the comparison: numeric subtraction, or localeCompare for
//     the text columns (Player, Team).
function compareValues(a, b, num, dir) {
  // Unknowns are settled BEFORE `dir` is consulted, and these returns are not
  // flipped. Folding them into the flipped comparison below is the one bug this
  // function can have: it would send every dash to the top of a descending sort
  // and silently invert the rule.
  const aKnown = a !== undefined && a !== null;
  const bKnown = b !== undefined && b !== null;
  if (!aKnown || !bKnown) return aKnown === bKnown ? 0 : (aKnown ? -1 : 1);

  const c = num ? a - b : String(a).localeCompare(String(b));
  return dir === "asc" ? c : -c;
}

function slotRow(slot, scales) {
  const state = slotState(slot);
  const row = el("li", `lineup-row${slotNeedsAttention(slot) ? " alert" : ""}`);
  row.appendChild(el("span", "lineup-slot", slot.slot));

  if (!slot.player) {
    row.appendChild(el("span", "lineup-name lineup-empty", "empty"));
    row.appendChild(el("span", "lineup-meta"));
    row.appendChild(el("span", "lineup-age muted", "—"));
    row.appendChild(el("span", "lineup-value muted", "—"));
    row.appendChild(el("span", "lineup-proj", "—"));
    return gridRow(row);
  }

  const p = slot.player;
  const nameCell = el("span", "lineup-name");
  nameCell.appendChild(el("span", null, p.name));
  // The tag is now the primary marker, so every non-active state carries one —
  // including the informational ones the row no longer stripes. "zero" stays
  // untagged because it is not a roster state: it is a projection of 0.0, which
  // the PROJ cell already prints and mutes.
  if (state && state !== "zero") {
    nameCell.appendChild(el("span", `badge badge-${state}`, p.status));
  }
  row.appendChild(nameCell);

  const meta = el("span", "lineup-meta");
  meta.appendChild(el("span", "lineup-team", p.team));
  meta.appendChild(el("span", "lineup-pos", (p.pos || []).join("/")));
  row.appendChild(meta);

  row.appendChild(heatCell("lineup-age", p.age, scales.age, fmtAge));
  row.appendChild(heatCell("lineup-value", p.hkb_value, scales.value, fmtValue));

  const proj = el("span", "lineup-proj", p.proj.toFixed(1));
  if (!p.proj) proj.classList.add("muted");
  row.appendChild(proj);
  return gridRow(row);
}

// The list is an <ol> of <li> on a CSS grid, not a <table>, so aria-sort on a
// header cell means nothing to a screen reader without an explicit grid role
// around it. Declaring the roles here is what makes the sort state in
// headerRow announceable; partial ARIA would be worse than none.
function gridRow(row) {
  row.setAttribute("role", "row");
  for (const cell of row.children) cell.setAttribute("role", "cell");
  return row;
}

// The column header is a real row in the same grid rather than a floating
// caption, so the two new numeric columns are labelled where they are read.
// Without it "27.4" and "5,210" sit side by side with nothing saying which is
// which.
//
// Each label is a real <button> rather than a click handler on the span: the
// control has to be reachable by keyboard and announced as pressable, and a
// bare span with a listener is neither. The arrow is a child span so the
// button's accessible name stays the plain column label — "Proj ▼" read aloud
// is worse than "Proj" plus the aria-sort the cell already carries.
function headerRow(sort, onSort) {
  const row = el("li", "lineup-row lineup-colhead");
  row.setAttribute("role", "row");
  for (const col of COLUMNS) {
    const cell = el("span", col.cls);
    cell.setAttribute("role", "columnheader");
    const active = sort.key === col.key;
    cell.setAttribute("aria-sort",
      active ? (sort.dir === "asc" ? "ascending" : "descending") : "none");

    const btn = el("button", `lineup-sort${active ? " active" : ""}`, col.label);
    btn.type = "button";
    if (active) btn.appendChild(el("span", "lineup-arrow", sort.dir === "asc" ? "▲" : "▼"));
    btn.addEventListener("click", () => onSort(col));
    cell.appendChild(btn);
    row.appendChild(cell);
  }
  return row;
}

function rampKey(cls, label, scale, format) {
  const key = el("span", "lineup-key");
  key.appendChild(el("span", `lineup-ramp ${cls}`));
  const text = scale
    ? `${label} ${format(scale.min)} → ${format(scale.max)}`
    : `${label} — no data`;
  key.appendChild(el("span", null, text));
  return key;
}

function legend(scales) {
  const box = el("div", "lineup-legend");
  box.appendChild(rampKey("ramp-age", "Age", scales.age, fmtAge));
  box.appendChild(rampKey("ramp-value", "HKB value", scales.value, fmtValue));
  box.appendChild(help(
    "Age and HKB dynasty value come from the Harry Knows Ball rankings, matched " +
    "to each player by name. In both columns a darker cell means more — older, " +
    "and more valuable. The shading is scaled to the highest and lowest numbers " +
    "in today's lineup, not to the whole league, so it shows how these players " +
    "compare with each other today and cannot be compared with another day's " +
    "page. Age shading is proportional; value shading is not — dynasty values " +
    "are so top-heavy that a proportional ramp would leave everyone below the " +
    "best player looking identical, so the value column is shaded on a square " +
    "root. Order is still exact; the size of the gap is not. A dash means HKB " +
    "has no entry for that player — often a recent call-up.",
    "How the shading works"));
  return box;
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

  const flagged = (data.slots || []).filter(slotNeedsAttention).length;
  if (flagged) {
    root.appendChild(el("p", "sub muted",
      `${flagged} of ${data.slots.length} slots need a look — marked below.`));
  }

  const players = (data.slots || []).map((s) => s.player).filter(Boolean);
  const scales = {
    age: scaleFor(players.map((p) => p.age)),
    value: scaleFor(players.map((p) => p.hkb_value), Math.sqrt),
  };
  // The legend is only meaningful once something is shaded. An older published
  // lineup predates the enrichment and carries neither field.
  if (scales.age || scales.value) root.appendChild(legend(scales));

  // Sorting re-orders the rows already on the page: it changes no membership,
  // so `scales` is computed once here and never recomputed. Shading is derived
  // from who is in the lineup, not from what order they are in — re-deriving it
  // per sort would make the same player change color for no reason.
  //
  // A null key is the published lineup order, which is also what Slot-ascending
  // reproduces; the default view therefore needs no sort applied at all.
  const sort = { key: null, dir: "asc" };
  const list = el("ol", "lineup-list");
  list.setAttribute("role", "table");
  list.setAttribute("aria-label", `Lineup for ${data.date}`);
  root.appendChild(list);

  const drawList = () => {
    const rows = (data.slots || []).map((slot, i) => ({ slot, i }));
    if (sort.key) {
      const col = COLUMNS.find((c) => c.key === sort.key);
      rows.sort((x, y) => compareValues(
        col.get(x.slot, x.i), col.get(y.slot, y.i), col.num, sort.dir));
    }
    list.replaceChildren(headerRow(sort, onSort));
    for (const r of rows) list.appendChild(slotRow(r.slot, scales));
  };

  const onSort = (col) => {
    if (sort.key === col.key) {
      sort.dir = sort.dir === "asc" ? "desc" : "asc";
    } else {
      sort.key = col.key;
      sort.dir = col.first;
    }
    drawList();
    // replaceChildren threw away the button that was just clicked, so focus
    // would fall back to <body> and a keyboard user would lose their place
    // mid-sort. Put it back on the equivalent button in the new header.
    const i = COLUMNS.indexOf(col);
    list.querySelectorAll(".lineup-sort")[i]?.focus();
  };

  drawList();
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
