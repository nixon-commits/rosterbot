// trades.js — pending trade offers and the league value table.
//
// Answers one question: should I take this offer? Each offer shows both sides
// priced against HKB dynasty values two ways — a plain sum, and a sum that
// discounts every asset after the best (internal/tradevalue.PackageDecay) —
// and states a verdict only when the two agree. When they disagree the trade
// is inside the model's own uncertainty and the tab says so instead of
// picking a side. That is not pedantry: on the 2-for-1 that motivated this
// feature the two methods name opposite winners.
//
// Rows are built with createElement/textContent rather than interpolated into
// innerHTML. Player and team names come from Fantrax and HKB, and render.js's
// escapeHtml escapes &<> but not quotes, so it is unsafe in an attribute
// context — the same footgun views.js documents. DOM text nodes remove it
// entirely rather than relying on every future edit getting escaping right.
import { api } from "./api.js";
import { help } from "./render.js";

const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

const num = (n) => (n ?? 0).toLocaleString();

function fmtWhen(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso ?? "—");
  return d.toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

// ---------------------------------------------------------------- offers

// The verdict badge deliberately distinguishes four states, not two. "Too
// close" and "incomplete" are different kinds of not-knowing: the first means
// the models disagree, the second means an asset could not be priced at all
// (today, always an unidentified draft pick — Fantrax sends pick rows with an
// empty name, while HKB prices picks up to 1419).
function verdictBadge(v, myTeam) {
  if (v.status === "favors") {
    const mine = v.favored_team === myTeam;
    const b = el("span", `badge ${mine ? "badge-ok" : "badge-failed"}`,
      mine ? "Favors you" : `Favors ${v.favored_team}`);
    return b;
  }
  if (v.status === "too-close") return el("span", "badge badge-info", "Too close to call");
  return el("span", "badge badge-info", "Cannot be priced");
}

// The verdict line is split in two: the numbers stay visible because they are
// the answer, and the reasoning about *why* two methods are used at all — which
// you need once, not on every offer — goes behind the bubble.
function verdictExplain(v, myTeam) {
  if (v.status === "favors") {
    const who = v.favored_team === myTeam ? "your side" : v.favored_team;
    return {
      short: `Both methods agree — ${v.raw_pct.toFixed(1)}% raw, ${v.adj_pct.toFixed(1)}% adjusted.`,
      long:
        `Each side is priced twice. Raw value is a straight sum of HKB dynasty ` +
        `values. Adjusted value discounts every asset after the best one, because ` +
        `a package of good players is worth less than their sum — you can only ` +
        `field so many.\n\nBoth readings favour ${who} here, which is what makes ` +
        `the call safe to state.`,
    };
  }
  if (v.status === "too-close") {
    return {
      short: `Too close — raw favours ${v.raw_leader} by ${v.raw_pct.toFixed(1)}%, ` +
        `adjusted favours ${v.adj_leader} by ${v.adj_pct.toFixed(1)}%.`,
      long:
        `Each side is priced twice. Raw value is a straight sum of HKB dynasty ` +
        `values. Adjusted value discounts every asset after the best one, because ` +
        `a package of good players is worth less than their sum.\n\nHere the two ` +
        `methods name opposite winners, so the trade sits inside the model's own ` +
        `uncertainty and no verdict is stated. A single number would have been ` +
        `confidently wrong — on the 2-for-1 that prompted this feature, raw ` +
        `favoured one side by 12.7% while adjusted favoured the other.`,
    };
  }
  const n = v.unpriced_assets || 0;
  return {
    short: `${n} asset${n === 1 ? "" : "s"} could not be priced, so no verdict is stated.`,
    long:
      `Fantrax sends some draft picks as rows with no name, and a pick can be ` +
      `worth more than everything else in the trade — HKB prices them up to 1419.\n\n` +
      `Rather than compute a verdict from a total that is missing an unknown ` +
      `amount, none is offered. The priced assets are still listed below so you ` +
      `can judge the rest yourself.`,
  };
}

function sidePanel(side, myTeam) {
  const box = el("div", "trade-side");
  const head = el("div", "trade-side-head");
  head.appendChild(el("strong", null, side.team + (side.team === myTeam ? " (you)" : "")));
  const cov = el("span", `badge ${side.priced_count === side.asset_count ? "badge-info" : "badge-failed"}`,
    `${side.priced_count}/${side.asset_count} priced`);
  head.appendChild(cov);
  box.appendChild(head);

  const list = el("ul", "trade-assets");
  for (const a of side.assets || []) {
    const li = el("li");
    li.appendChild(el("span", "trade-val mono", a.priced ? num(a.value) : "—"));
    li.appendChild(el("span", "trade-name", a.name));
    if (a.position) li.appendChild(el("span", "trade-pos muted", a.position));
    if (!a.priced) li.appendChild(el("span", "badge badge-info", a.is_pick ? "pick" : "unranked"));
    list.appendChild(li);
  }
  box.appendChild(list);

  const tot = el("div", "trade-totals");
  tot.appendChild(el("span", null, `Raw ${num(side.raw)}`));
  tot.appendChild(el("span", "muted", `Adjusted ${num(Math.round(side.adjusted))}`));
  box.appendChild(tot);
  return box;
}

// The impact table is the answer the verdict cannot give. A trade can be dead
// even on value and still move you several places in a category — so a rank
// change is highlighted even when the value delta is small.
function impactTable(imp) {
  const table = el("table", "data-table");
  const thead = el("thead");
  const hr = el("tr");
  for (const h of ["", "Before", "After", "Change", "Rank"]) hr.appendChild(el("th", null, h));
  thead.appendChild(hr);
  table.appendChild(thead);

  const tb = el("tbody");
  for (const m of imp.metrics || []) {
    const tr = el("tr");
    tr.appendChild(el("td", null, m.name));
    tr.appendChild(el("td", "num mono", num(m.before)));
    tr.appendChild(el("td", "num mono", num(m.after)));

    const d = el("td", "num mono");
    d.textContent = m.delta > 0 ? `+${num(m.delta)}` : num(m.delta);
    if (m.delta > 0) d.classList.add("good");
    else if (m.delta < 0) d.classList.add("bad");
    tr.appendChild(d);

    const r = el("td", "num");
    if (m.rank_before === m.rank_after) {
      r.className = "num muted";
      r.textContent = `${m.rank_before} of ${m.teams}`;
    } else {
      r.textContent = `${m.rank_before} → ${m.rank_after} of ${m.teams}`;
      // Lower rank number is better.
      r.classList.add(m.rank_after < m.rank_before ? "good" : "bad");
    }
    tr.appendChild(r);
    tb.appendChild(tr);
  }
  table.appendChild(tb);
  return table;
}

function offerCard(offer, myTeam) {
  const card = el("div", "card");

  const head = el("div", "trade-head");
  head.appendChild(el("h2", null, "Offer"));
  head.appendChild(verdictBadge(offer.verdict, myTeam));
  card.appendChild(head);

  const { short, long } = verdictExplain(offer.verdict, myTeam);
  const line = el("p", "sub muted");
  line.append(short, " ", help(long, "How this verdict is reached"));
  card.appendChild(line);

  const grid = el("div", "trade-grid");
  for (const s of offer.sides || []) grid.appendChild(sidePanel(s, myTeam));
  card.appendChild(grid);

  if (offer.impact) {
    card.appendChild(el("h3", null, "If you accept"));
    const tw = el("div", "table-wrap");
    tw.appendChild(impactTable(offer.impact));
    card.appendChild(tw);
  } else if (offer.impact_note) {
    card.appendChild(el("p", "sub muted", `Roster impact unavailable — ${offer.impact_note}.`));
  }
  return card;
}

// ---------------------------------------------------------- values table

function valuesSection(table, myTeam) {
  const card = el("div", "card");
  card.appendChild(el("h2", null, "League values"));

  const coverPct = table.rostered ? (table.matched / table.rostered) * 100 : 0;
  const cover = el("p", "sub muted");
  cover.append(
    `${num(table.matched)} of ${num(table.rostered)} rostered players priced ` +
    `(${coverPct.toFixed(1)}%) · built ${fmtWhen(table.generated_at)} `,
    help(
      "Players are joined to an HKB dynasty value by name. Anyone who cannot be " +
      "matched — usually a call-up too recent for HKB's rankings — shows no value " +
      "and is counted nowhere, so any total built from this table is a floor " +
      "rather than a total.",
      "What priced coverage means",
    ),
  );
  card.appendChild(cover);

  const teams = [...new Set((table.players || []).map((p) => p.owner_name))].sort();

  const controls = el("div", "controls");
  const search = el("input");
  search.type = "search";
  search.placeholder = "Filter by player name…";
  search.className = "trade-search";
  const teamSel = el("select");
  for (const [val, label] of [["", "All teams"], ...teams.map((t) => [t, t])]) {
    const o = el("option", null, label);
    o.value = val;
    teamSel.appendChild(o);
  }
  // Default to my own roster: 515 rows is a wall, and the question that brings
  // you here usually starts with what you already own.
  if (teams.includes(myTeam)) teamSel.value = myTeam;
  controls.appendChild(search);
  controls.appendChild(teamSel);
  card.appendChild(controls);

  const wrap = el("div", "table-wrap");
  const note = el("p", "sub muted");
  card.appendChild(wrap);
  card.appendChild(note);

  const paint = () => {
    const q = search.value.trim().toLowerCase();
    const team = teamSel.value;
    const rows = (table.players || []).filter(
      (p) => (!team || p.owner_name === team) && (!q || p.name.toLowerCase().includes(q)),
    );
    wrap.replaceChildren(playerTable(rows.slice(0, 300)));
    // The cap is a rendering limit, not a filter, so say so — silently showing
    // 300 of 515 would read as "these are all the players on that team".
    note.textContent = rows.length > 300
      ? `Showing the top 300 by value of ${num(rows.length)} matches — narrow the filter to see the rest.`
      : "";
  };
  search.addEventListener("input", paint);
  teamSel.addEventListener("change", paint);
  paint();

  if ((table.picks || []).length) {
    const picksHead = el("h3", null, "Draft picks");
    picksHead.append(" ", help(
      "HKB prices picks by projected slot, but Fantrax never says who holds " +
      "which one — so this is a league-wide reference list, not a list of " +
      "anybody's assets.\n\nIt is also why a verdict gets withheld: an " +
      "unidentified pick inside an offer cannot be priced, and a pick can be " +
      "worth more than everything beside it.",
      "Why picks are unattributed",
    ));
    card.appendChild(picksHead);
    const pw = el("div", "table-wrap");
    pw.appendChild(pickTable(table.picks));
    card.appendChild(pw);
  }
  return card;
}

function playerTable(rows) {
  const t = el("table", "data-table");
  const thead = el("thead");
  const hr = el("tr");
  for (const h of ["Player", "Pos", "Team", "Value", "Rank", "Age"]) hr.appendChild(el("th", null, h));
  thead.appendChild(hr);
  t.appendChild(thead);

  const tb = el("tbody");
  for (const p of rows) {
    const tr = el("tr");
    const nameCell = el("td");
    nameCell.appendChild(el("span", null, p.name));
    if (p.is_minors) nameCell.appendChild(el("span", "badge badge-info", "MiL"));
    tr.appendChild(nameCell);
    tr.appendChild(el("td", "muted", p.position || "—"));
    tr.appendChild(el("td", null, p.owner_name || "—"));
    tr.appendChild(el("td", "num mono", p.matched ? num(p.value) : "—"));
    tr.appendChild(el("td", "num mono muted", p.matched ? num(p.rank) : "—"));
    tr.appendChild(el("td", "num muted", p.age ? p.age.toFixed(1) : "—"));
    tb.appendChild(tr);
  }
  t.appendChild(tb);
  return t;
}

function pickTable(picks) {
  const t = el("table", "data-table");
  const thead = el("thead");
  const hr = el("tr");
  for (const h of ["Pick", "Value", "Rank"]) hr.appendChild(el("th", null, h));
  thead.appendChild(hr);
  t.appendChild(thead);
  const tb = el("tbody");
  for (const p of picks) {
    const tr = el("tr");
    tr.appendChild(el("td", null, p.name));
    tr.appendChild(el("td", "num mono", num(p.value)));
    tr.appendChild(el("td", "num mono muted", num(p.rank)));
    tb.appendChild(tr);
  }
  t.appendChild(tb);
  return t;
}

// ---------------------------------------------------------------- render

export async function renderTrades(root) {
  root.replaceChildren(el("p", "muted", "Loading trades…"));

  // The two artifacts have different producers and cadences, so they fail
  // independently: offers without a values table still answer "is this fair",
  // and a values table without offers is still worth browsing.
  const [snap, table] = await Promise.all([
    api.trades().catch(() => null),
    api.tradeValues().catch(() => null),
  ]);

  root.replaceChildren();

  if (!snap && !table) {
    const c = el("div", "card");
    c.appendChild(el("h2", null, "Trades"));
    c.appendChild(el("p", "muted",
      "No trade data yet — it publishes on the next hourly lineup run."));
    root.appendChild(c);
    return;
  }

  const myTeam = snap?.my_team || "";
  const offers = snap?.offers || [];

  const head = el("div", "card");
  head.appendChild(el("h2", null, offers.length
    ? `${offers.length} pending offer${offers.length === 1 ? "" : "s"}`
    : "No pending offers"));
  head.appendChild(el("p", "sub muted", offers.length
    ? `For ${myTeam} · checked ${fmtWhen(snap.generated_at)}`
    : `Nothing on the table for ${myTeam || "your team"} · checked ${fmtWhen(snap?.generated_at)}`));
  root.appendChild(head);

  for (const o of offers) root.appendChild(offerCard(o, myTeam));
  if (table) root.appendChild(valuesSection(table, myTeam));
}
