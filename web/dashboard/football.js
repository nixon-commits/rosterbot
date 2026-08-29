// football.js — the dynasty football tab. Two independent sections:
//
//   1. Standings, from report/football.json (internal/dynasty.Model) — each
//      team's Starters Value (the headline) beside its full-roster total
//      (visually demoted) and join-coverage counts. The gate pre-commitment
//      from the 2026-08-10 coverage spike (see CLAUDE.md's Dynasty Football
//      section): starters coverage is consistently near-complete, full-roster
//      coverage isn't, so the headline must be the number the join supports.
//
//   2. Graded trades, from report/football-trades.json
//      (internal/dynasty.TradeLogModel) — the durable log written when each
//      trade was alerted.
//
// THE TWO ARTIFACTS FETCH AND FAIL INDEPENDENTLY. This function used to return
// early whenever the standings model was missing or empty, so a second section
// added inside it would have been invisible on exactly the fresh deploy where
// someone would go looking for it. Neither section can suppress the other now;
// each paints its own "no data yet" card.
//
// Rows are built with createElement/textContent, not innerHTML, following
// views.js's rule (render.js's escapeHtml escapes quotes now, but the doc
// comment still directs new code here) — team names come from Sleeper display
// names and asset names from the NFL player dump, neither of them vetted. A
// real league team is named "Zatch's mom Hawk Tua'd".
import { api } from "./api.js";
import { el, help, unmatchedText, wireUnmatchedPopovers } from "./render.js";

const FORMATS = [
  ["sf_dynasty", "SF Dynasty"],
  ["non_sf_dynasty", "Non-SF Dynasty"],
  ["sf_redraft", "SF Redraft"],
  ["non_sf_redraft", "Non-SF Redraft"],
];

// FORMAT_LABEL is the human name for a format key, used when the view has to
// name a format other than the selected one (e.g. which one the alert used).
const FORMAT_LABEL = Object.fromEntries(FORMATS);

const fmt = (n) => (typeof n === "number" ? n.toLocaleString() : "—");

const sentenceCase = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : "");

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
  const loading = el("p", "muted", "Loading dynasty football…");
  root.appendChild(loading);

  // api.request throws an ApiError on any non-2xx, 404 included, so each fetch
  // is caught on its own. Promise.all over already-caught promises never
  // rejects, so one absent artifact cannot take the other down.
  const [standings, trades] = await Promise.all([
    api.reportFootball().catch(() => null),
    api.reportFootballTrades().catch(() => null),
  ]);

  const state = { format: "sf_dynasty" };
  const view = buildLayout(root, standings, trades);
  wireFormatSeg(view, state, () => paint(view, standings, trades, state));
  paint(view, standings, trades, state);
}

function paint(view, standings, trades, state) {
  paintStandings(view.standingsHost, standings, state);
  paintTrades(view.tradesHost, trades, state);
}

function buildLayout(root, standings, trades) {
  root.replaceChildren();

  const wrap = el("div", "football");

  const sub = el("div", "sub muted");
  sub.textContent = summaryLine(standings, trades);
  wrap.appendChild(sub);

  const controls = el("div", "controls");
  const seg = el("div", "toggle-group");
  controls.appendChild(seg);
  wrap.appendChild(controls);

  const standingsSection = el("section", "card");
  standingsSection.appendChild(el("h2", null, standingsHeading(standings)));
  standingsSection.appendChild(
    el(
      "p",
      "sub muted",
      "Starters Value leads: it's what the StatsGuy join can reliably price. " +
        "Full Roster is shown for reference, alongside how many rostered players matched a value.",
    ),
  );
  const standingsHost = el("div", "table-wrap");
  standingsSection.appendChild(standingsHost);
  wrap.appendChild(standingsSection);

  const tradesSection = el("section", "card");
  tradesSection.appendChild(el("h2", null, "Graded trades"));
  // The caveat note only earns its place when there are rows to caveat. With an
  // empty log it rendered as a lone help bubble after an empty sentence.
  if (hasTrades(trades)) tradesSection.appendChild(tradesNote(trades));
  wrap.appendChild(tradesSection);

  // tradesHost is a SIBLING of the header card, not a child of it. Each trade
  // renders as its own .card, and .card carries a border, shadow and padding —
  // nesting them produced a bordered box inside a bordered box with doubled
  // padding. trades.js, the view this mirrors, appends its offer cards as
  // siblings for exactly this reason.
  const tradesHost = el("div");
  wrap.appendChild(tradesHost);

  root.appendChild(wrap);
  return { seg, standingsHost, tradesHost };
}

function tradesNote(trades) {
  const note = el("p", "sub muted");
  note.append(
    tradesCoverageLine(trades) + " ",
    help(
      "Every value here is what StatsGuy said WHEN THE TRADE WAS GRADED, not what it " +
        "says now. StatsGuy publishes no history, so re-pricing an old trade today would " +
        "answer a different question — what it would be worth if made now — while looking " +
        "like what it was worth then. Rows marked 'regraded' are the exception: they were " +
        "rebuilt after the fact, so their values ARE today's, and the alert's original " +
        "verdict is shown beside them. The log is also written by the six-hourly trade " +
        "poller but published by the daily dashboard job, so a brand-new trade can take up " +
        "to a day to appear.",
      "What these values mean",
    ),
  );
  return note;
}

function hasStandings(m) {
  return !!m && !m.empty && Array.isArray(m.teams) && m.teams.length > 0;
}
function hasTrades(m) {
  return !!m && !m.empty && Array.isArray(m.trades) && m.trades.length > 0;
}

function summaryLine(standings, trades) {
  const parts = [];
  if (hasStandings(standings)) parts.push(`${standings.teams.length} teams · ${standings.latestDate}`);
  if (hasTrades(trades)) parts.push(`${trades.trades.length} graded trade${trades.trades.length === 1 ? "" : "s"}`);
  return parts.length ? parts.join(" · ") : "No dynasty football data yet.";
}

function standingsHeading(standings) {
  return hasStandings(standings) ? `Standings — ${standings.latestDate}` : "Standings";
}

function tradesCoverageLine(trades) {
  if (!hasTrades(trades)) return "";
  // Stated in TRADE dates, because that is what a reader compares against the
  // card headings. The model's coversFrom is the earliest trade in the list,
  // never the earliest partition — --relog files months-old trades under
  // today's grade date, so a partition reading would claim the list starts
  // today directly above a card headed 2026-01-23.
  const n = trades.trades.length;
  const span = trades.coversFrom ? ` from ${trades.coversFrom} onward` : "";
  const regraded = trades.regraded
    ? ` ${trades.regraded} of them ${trades.regraded === 1 ? "was" : "were"} rebuilt after the fact and carry today's prices, badged Regraded.`
    : "";
  return `${n} trade${n === 1 ? "" : "s"}${span}, each valued as priced when it was graded.${regraded}`;
}

// ------------------------------------------------------------- standings

function numCell(text, extraClass) {
  const td = el("td", extraClass ? `num ${extraClass}` : "num");
  td.textContent = text;
  return td;
}

function coverageCell(matched, rostered, unmatched) {
  const td = el("td", "num cov" + (matched < rostered ? " warn" : ""));
  td.textContent = `${matched}/${rostered}`;
  // dataset, not title: a native tooltip is invisible on touch, so the names
  // are handed to render.js's hover/focus panel instead. Property assignment,
  // not attribute interpolation — these are Sleeper player-dump names, on the
  // same footing as the team names above.
  const text = unmatchedText(matched, rostered, unmatched);
  if (text) td.dataset.unmatched = text;
  return td;
}

function paintStandings(host, model, state) {
  if (!hasStandings(model)) {
    host.replaceChildren(
      el(
        "p",
        "muted",
        model
          ? "Collecting dynasty football data — check back after the next daily run."
          : "No dynasty football standings yet.",
      ),
    );
    return;
  }

  const sKey = startersKey(state.format);
  const fKey = fullRosterKey(state.format);

  const sorted = model.teams.slice().sort((a, b) => {
    if (b[sKey] !== a[sKey]) return b[sKey] - a[sKey];
    return String(a.teamId).localeCompare(String(b.teamId));
  });

  const table = el("table", "data-table");

  const thead = el("thead");
  const hrow = el("tr");
  for (const [text, cls] of [
    ["Team", ""],
    ["Starters Value", "num"],
    ["Full Roster", "num"],
    ["Matched", "num"],
  ]) {
    const th = el("th", cls || null, text);
    hrow.appendChild(th);
  }
  thead.appendChild(hrow);
  table.appendChild(thead);

  const tbody = el("tbody");
  sorted.forEach((t, i) => {
    const tr = el("tr");
    if (i === 0) tr.className = "lead";

    const nameTd = el("td");
    const teamWrap = el("div", "team");
    teamWrap.appendChild(el("span", null, t.teamName || t.teamId));
    nameTd.appendChild(teamWrap);
    tr.appendChild(nameTd);

    tr.appendChild(numCell(fmt(t[sKey])));
    tr.appendChild(numCell(fmt(t[fKey]), "muted"));
    tr.appendChild(coverageCell(t.matchedCount, t.rosteredCount, t.unmatched));

    tbody.appendChild(tr);
  });
  table.appendChild(tbody);

  host.replaceChildren(table);
  wireUnmatchedPopovers(host);
}

// ---------------------------------------------------------------- trades

// verdictBadge renders the THREE states a football trade can be in. Baseball's
// trades.js has four because tradevalue compares two pricing methods and can
// report them disagreeing; dynasty.GradeTrade is deliberately two-branch (no
// PackageDecay is ported), so "too close" does not exist here. The third state
// is the log being empty, handled by the caller.
//
// Neither badge is colored good or bad. There is no "your team" in a
// league-wide view, so favoring someone is a fact, not an outcome; the favored
// side is marked instead on its own panel, where it means something.
function verdictBadge(v) {
  if (!v) return el("span", "badge badge-info", "Not graded");
  if (v.status === "favors") {
    // Rendered from the STORED summary, never by re-rounding v.pct. Go's %.0f
    // rounds half to even (62.5 -> 62) and JS Math.round rounds half up
    // (62.5 -> 63), so a re-render would eventually print a badge that
    // contradicts the alert text quoted directly beneath it. v.pct stays in the
    // JSON for anyone who wants the real number.
    return el("span", "badge", sentenceCase(v.summary) || `Favors ${v.favoredTeamName || v.favoredTeamId}`);
  }
  // Two different reasons a verdict is withheld, and conflating them would
  // blame an asset that priced perfectly well. unpricedAssets > 0 means
  // something had no value at all (FAAB, an unlisted pick tier); 0 means
  // everything priced and all of it is worth nothing IN THIS FORMAT — the
  // ordinary case for a pick-for-pick trade under either redraft toggle.
  // A tie is its own status precisely because it does not fit either branch
  // below: everything priced, and the value is real rather than absent
  // (rosterbot-h688). "No value in this format" would send the reader to the
  // format toggles to fix something no toggle can change.
  if (v.status === "dead-even") return el("span", "badge badge-info", "Dead even");
  const n = v.unpricedAssets || 0;
  return el(
    "span",
    "badge badge-info",
    n ? `Cannot be priced — ${n} asset${n === 1 ? "" : "s"} unvalued` : "No value in this format",
  );
}

// verdictNote explains the selected format's verdict.
//
// It takes the whole trade, not just the verdict, for two reasons. Only ONE
// format was ever alerted (trade.alertFormat), so "Alert reported: …" is a
// claim the view may only make in that one toggle position — anywhere else it
// would present a message that was never sent as the record of what was sent,
// on a tab whose entire point is provenance. And a withheld verdict has three
// distinct causes, only one of which switching format can fix.
function verdictNote(trade, format, v) {
  if (!v) return "This trade carries no verdict for the selected format.";

  if (v.status === "favors") {
    if (!v.summary) return "";
    if (format === trade.alertFormat) {
      // The stored summary is the exact string the alert used, %.0f rounding
      // included, so the page cannot drift from what was reported.
      return `Alert reported: ${v.summary}.`;
    }
    const sent = FORMAT_LABEL[trade.alertFormat];
    return sent ? `Graded here in ${FORMAT_LABEL[format]}; the alert was sent in ${sent}.` : "";
  }

  if (v.unpricedAssets) {
    return (
      "At least one asset had no StatsGuy value — a FAAB amount, or a draft pick tier " +
      "StatsGuy does not list. A side total that silently omits it would be a floor, not a " +
      "total, so no verdict is stated."
    );
  }
  // Distinguishing this from the zero-value case matters: no format change can
  // fix a trade in which nothing this system models changed hands.
  const sides = Array.isArray(trade.sides) ? trade.sides : [];
  const assets = sides.reduce((n, s) => n + (Array.isArray(s.assets) ? s.assets.length : 0), 0);
  if (sides.length < 2 || assets === 0) {
    return (
      "Nothing this system models changed hands on at least one side, so there is nothing to " +
      "compare. Switching format will not change that."
    );
  }
  // Reached only when the sides are exactly level at a real, non-zero value.
  // The zero-value copy below would be actively misleading here: it blames the
  // format and prescribes switching, when no format can separate equal sides.
  if (v.status === "dead-even") {
    return (
      "Every asset priced, and the leading sides come out exactly level in this format. There " +
      "is nothing to separate them, so no verdict is stated — naming a winner would only " +
      "report the order the teams happen to sort in. In a trade with more than two rosters " +
      "this means no side leads, not that every side is equal. Another format may still " +
      "separate them."
    );
  }
  return (
    "Every asset priced, and every side totals zero in this format — StatsGuy gives future " +
    "draft picks no redraft value. Comparing nothing to nothing is not a verdict, so none is " +
    "stated. Switch to a dynasty format to grade this trade."
  );
}

function assetLine(asset, format) {
  const li = el("li");
  const val = el("span", "trade-val");
  // An unpriced asset has no number at all. Rendering 0 would state a value
  // that was never measured.
  val.textContent = asset.priced ? fmt(asset.values?.[format] ?? 0) : "—";
  if (!asset.priced) val.classList.add("muted");
  li.appendChild(val);
  li.appendChild(el("span", "trade-name", asset.name || "(unnamed asset)"));
  return li;
}

function sidePanel(side, format, verdict) {
  const panel = el("div", "trade-side");

  const head = el("div", "trade-side-head");
  head.appendChild(el("strong", null, side.teamName || side.teamId));
  if (verdict && verdict.status === "favors" && verdict.favoredTeamId === side.teamId) {
    head.appendChild(el("span", "badge badge-ok", "Favored"));
  }
  panel.appendChild(head);

  const ul = el("ul", "trade-assets");
  const assets = Array.isArray(side.assets) ? side.assets : [];
  if (assets.length === 0) {
    ul.appendChild(el("li", "muted", "Received nothing this system models"));
  }
  for (const a of assets) ul.appendChild(assetLine(a, format));
  panel.appendChild(ul);

  const unpriced = assets.filter((a) => !a.priced).length;
  const totals = el("div", "trade-totals");
  totals.appendChild(el("span", "muted", unpriced ? "Priced subtotal" : "Total"));
  totals.appendChild(el("span", null, fmt(side.totals?.[format] ?? 0)));
  panel.appendChild(totals);

  return panel;
}

function tradeCard(trade, format) {
  const card = el("div", "card");
  const verdict = trade.verdicts ? trade.verdicts[format] : null;

  const head = el("div", "trade-head");
  // h2, not h3: style.css resets `.trade-head h2 { margin: 0 }` for trades.js's
  // card, and an h3 keeps its bottom margin, pushing the date off the flex row's
  // vertical centre relative to the badge beside it.
  head.appendChild(el("h2", null, trade.tradeDate || "Date unknown"));
  head.appendChild(verdictBadge(verdict));
  if (trade.regraded) {
    head.appendChild(el("span", "badge badge-info", "Regraded"));
  }
  card.appendChild(head);

  // Both notes, when both apply. They answer different questions — why these
  // values are today's, and why this format states no verdict — and an
  // either/or silently dropped the second on every regraded row.
  if (trade.regraded) {
    const provenance = el("p", "sub muted");
    provenance.append(
      `Rebuilt on ${trade.gradedAt} at that day's prices, not the trade's — the original alert ` +
        (trade.originalSummary ? `reported: ${trade.originalSummary}.` : "summary was not recoverable."),
    );
    card.appendChild(provenance);
  }
  const text = verdictNote(trade, format, verdict);
  if (text) card.appendChild(el("p", "sub muted", text));

  const grid = el("div", "trade-grid");
  for (const s of trade.sides || []) grid.appendChild(sidePanel(s, format, verdict));
  card.appendChild(grid);

  return card;
}

function paintTrades(host, model, state) {
  if (!hasTrades(model)) {
    const box = el("div", "card");
    box.appendChild(
      el(
        "p",
        "muted",
        model
          ? "No graded trades logged yet — the log fills in as trades complete."
          : "No graded trades yet — the log publishes with the next daily dashboard run.",
      ),
    );
    host.replaceChildren(box);
    return;
  }
  const cards = model.trades.map((t) => tradeCard(t, state.format));
  host.replaceChildren(...cards);
}

// ------------------------------------------------------------------ shell

function wireFormatSeg(view, state, rerender) {
  view.seg.replaceChildren();
  for (const [key, label] of FORMATS) {
    const b = el("button", null, label);
    b.type = "button";
    b.dataset.key = key;
    b.classList.toggle("active", key === state.format);
    b.addEventListener("click", () => {
      state.format = key;
      view.seg.querySelectorAll("button").forEach((btn) => btn.classList.toggle("active", btn.dataset.key === key));
      rerender();
    });
    view.seg.appendChild(b);
  }
}
