// projections.js — native projection-accuracy view. Fetches the precomputed
// report.Model JSON (internal/report) and renders the scorecard, by-position,
// calibration, misses, trend, and the head-to-head system-comparison panel with
// client-side window/role/system toggles. Ported from the deleted
// internal/report/template.html inline <script>; the Model already carries every
// computed metric, so nothing here recomputes accuracy — it only reshapes the
// precomputed values into DOM + Chart.js (via the Task-6 chart.js wrappers).
import { api } from "./api.js";
import { lineChart, scatterChart, barChart, themeColors } from "./chart.js";
import { escapeHtml } from "./render.js";

const WINDOW_LABELS = { 7: "7d", 14: "14d", 30: "30d", 0: "Season" };
const ROLE_LABELS = { all: "All", hitters: "Hitters", pitchers: "Pitchers" };

const winLabel = (w) => WINDOW_LABELS[w] ?? (w <= 0 ? "Season" : w + "d");
const roleLabel = (r) => ROLE_LABELS[r] ?? (r ? r[0].toUpperCase() + r.slice(1) : String(r));
const sysLabel = (s) => String(s).replace(/-ros$/, " RoS");
const fmt = (x, d = 1) => (x === 0 ? 0 : x).toFixed(d);

// Fallback so a (system|window|role) triple missing from Views never crashes a
// render (Aggregate builds every triple, so this is defensive only). Never
// mutated — readers copy out of it.
const EMPTY_VIEW = { scorecard: { cur: {}, prior: {} }, byPos: [], calib: [], misses: [], insights: [] };

export async function renderProjections(root) {
  root.innerHTML = `<p class="muted">Loading projections…</p>`;
  let model;
  try {
    model = await api.reportModel();
  } catch (err) {
    root.innerHTML = `<div class="card"><p class="muted">No projection data yet. The daily grade + projection-site run publishes it.</p></div>`;
    return;
  }

  // Prefer the production (detail) system, but only if it actually has captured
  // rows; otherwise land on whichever system does, so the Detail panel never
  // opens on an empty view. (Ported from the template's boot logic.)
  const systems = model.systems || [];
  const defaultSystem = systems.includes(model.detailSystem)
    ? model.detailSystem
    : (systems[0] || model.detailSystem || "");

  const state = { window: 30, role: "all", system: defaultSystem };
  const el = buildLayout(root);
  const rerender = () => paint(el, model, state);
  wireToggles(el, model, state, rerender);
  paint(el, model, state);
}

// buildLayout paints the static shell once and returns a map of data-ref nodes
// (plus a live-chart registry and misses-sort state) that paint() fills in.
function buildLayout(root) {
  root.innerHTML = "";
  const wrap = document.createElement("div");
  wrap.className = "projections";
  wrap.innerHTML = `
    <div class="sub muted" data-ref="meta"></div>

    <div class="controls">
      <div class="toggle-group" data-ref="winseg"></div>
      <div class="toggle-group" data-ref="roleseg"></div>
    </div>

    <section class="card">
      <h2>Projection system comparison <span class="muted" data-ref="compareSub"></span></h2>
      <div data-ref="compareTable"></div>
      <div class="chart-box"><canvas data-ref="compareChart"></canvas></div>
    </section>

    <div class="controls">
      <div class="toggle-group" data-ref="sysseg"></div>
    </div>
    <h2>Detail — <span data-ref="detailSysLabel"></span></h2>
    <div class="sub muted" data-ref="detailSub"></div>

    <div class="stat-row" data-ref="scorecard"></div>

    <section class="card">
      <h2 data-ref="trendTitle"></h2>
      <div class="chart-box"><canvas data-ref="trendChart"></canvas></div>
    </section>

    <section class="card">
      <h2>Accuracy by position</h2>
      <div class="chart-box"><canvas data-ref="posChart"></canvas></div>
      <div data-ref="posTable"></div>
    </section>

    <section class="card">
      <h2>Calibration — projected vs actual</h2>
      <div class="chart-box"><canvas data-ref="calibChart"></canvas></div>
    </section>

    <section class="card">
      <h2>Insights</h2>
      <ul class="insights" data-ref="insightList"></ul>
    </section>

    <section class="card">
      <h2>Worst misses</h2>
      <div data-ref="missTable"></div>
    </section>
  `;
  root.appendChild(wrap);

  const el = {};
  wrap.querySelectorAll("[data-ref]").forEach((n) => { el[n.dataset.ref] = n; });
  el.charts = {};
  el.lastMisses = [];
  el.missState = { key: null, dir: "desc" };
  return el;
}

// wireToggles builds each toggle-group's buttons once and attaches click
// handlers that mutate state + rerender. The active class is (re)applied in
// paint, so handlers never need re-binding.
function wireToggles(el, model, state, rerender) {
  buildSeg(el.winseg, (model.windows || []).map((w) => ({ val: w, label: winLabel(w) })), state, "window", rerender);
  buildSeg(el.roleseg, (model.roles || []).map((r) => ({ val: r, label: roleLabel(r) })), state, "role", rerender);
  buildSeg(el.sysseg, (model.systems || []).map((s) => ({ val: s, label: sysLabel(s) })), state, "system", rerender);
}

function buildSeg(container, items, state, key, rerender) {
  container.innerHTML = "";
  for (const it of items) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = it.label;
    b.dataset.val = String(it.val);
    b.addEventListener("click", () => { state[key] = it.val; rerender(); });
    container.appendChild(b);
  }
}

function paintSegActive(container, state, key) {
  for (const b of container.children) {
    b.classList.toggle("active", String(state[key]) === b.dataset.val);
  }
}

function destroy(el, id) {
  if (el.charts[id]) { el.charts[id].destroy(); delete el.charts[id]; }
}

function paint(el, model, state) {
  el.meta.textContent =
    "Latest graded: " + model.latestDate + " · season since " + model.seasonStart + " · generated " + model.generatedAt;

  paintSegActive(el.winseg, state, "window");
  paintSegActive(el.roleseg, state, "role");
  paintSegActive(el.sysseg, state, "system");

  renderCompare(el, model, state);

  el.detailSysLabel.textContent = sysLabel(state.system || "");
  el.detailSub.textContent = state.system === model.detailSystem
    ? "The bot's production projection, broken down below."
    : "Captured for comparison — not used in production lineups.";

  const view = (model.views && model.views[detailKey(state)]) || EMPTY_VIEW;
  renderScorecard(el, view);
  renderTrend(el, model, state);
  renderPos(el, view);
  renderCalib(el, view);
  renderInsights(el, view);
  renderMisses(el, view);
}

// Keys mirror internal/report/model.go exactly:
//   detailKey = system + "|" + fmt.Sprintf("%d|%s", window, role)  → "depthcharts-ros|30|all"
//   compareKey =                fmt.Sprintf("%d|%s", window, role)  → "30|all"
// state.window is a Number; string concatenation coerces it to its int form, so
// no zero-padding or float formatting sneaks in.
const detailKey = (s) => s.system + "|" + s.window + "|" + s.role;
const compareKey = (s) => s.window + "|" + s.role;

// ---- Scorecard -------------------------------------------------------------

function renderScorecard(el, view) {
  const s = view.scorecard || {};
  const c = s.cur || {};
  const p = s.prior || {};
  if (!c.n) {
    el.scorecard.innerHTML = `<p class="muted">No graded data in this window yet.</p>`;
    return;
  }
  el.scorecard.innerHTML =
    tile("MAE", fmt(c.mae), deltaCell(c.mae, p.mae, true)) +
    tile("Bias", fmt(c.bias), deltaCell(Math.abs(c.bias), Math.abs(p.bias), true)) +
    tile("RMSE", fmt(c.rmse), deltaCell(c.rmse, p.rmse, true)) +
    tile("Sample (player-days)", (c.n || 0).toLocaleString(), `<div class="delta flat">prior ${(p.n || 0).toLocaleString()}</div>`);
}

function tile(label, value, delta) {
  return `<div class="stat-tile"><div class="label">${escapeHtml(label)}</div><div class="value">${value}</div>${delta}</div>`;
}

// deltaCell renders the current-vs-prior-window change. lowerBetter maps the
// sign to good/bad coloring (MAE/RMSE/|Bias| are all lower-is-better).
function deltaCell(cur, prior, lowerBetter = true) {
  if (!prior) return `<div class="delta flat">—</div>`;
  const diff = cur - prior;
  const improved = lowerBetter ? diff < 0 : diff > 0;
  const cls = Math.abs(diff) < 1e-9 ? "flat" : (improved ? "good" : "bad");
  const sign = diff > 0 ? "+" : "";
  return `<div class="delta ${cls}">${sign}${fmt(diff, 2)} vs prior</div>`;
}

// ---- System comparison panel ----------------------------------------------

function renderCompare(el, model, state) {
  const key = compareKey(state);
  const scores = (model.compare && model.compare[key]) || [];
  const withData = scores.filter((s) => s.n > 0);

  el.compareSub.textContent = withData.length
    ? "· ranked by MAE (lower = better) · " + winLabel(state.window) + " · " + state.role
    : "";

  const t = themeColors();
  const systems = model.systems || [];
  const colorFor = (sys) => t.palette[Math.max(0, systems.indexOf(sys)) % t.palette.length];

  if (!withData.length) {
    el.compareTable.innerHTML = `<p class="muted">No graded data across systems in this window yet.</p>`;
  } else {
    const head = `<tr><th>System</th><th class="num">MAE</th><th class="num">Bias</th><th class="num">RMSE</th><th class="num">Sample</th></tr>`;
    const body = scores.map((s) => {
      if (s.n === 0) return "";
      const prod = s.system === model.detailSystem ? ` <span class="badge">prod</span>` : "";
      const best = s.best ? ` <span class="badge">best</span>` : "";
      const sw = `<span class="swatch" style="background:${colorFor(s.system)}"></span>`;
      return `<tr class="${s.best ? "best" : ""}"><td>${sw}${escapeHtml(sysLabel(s.system))}${prod}${best}</td>` +
        `<td class="num">${fmt(s.mae, 2)}</td>` +
        `<td class="num">${s.bias > 0 ? "+" : ""}${fmt(s.bias, 2)}</td>` +
        `<td class="num">${fmt(s.rmse, 2)}</td>` +
        `<td class="num">${s.n.toLocaleString()}</td></tr>`;
    }).join("");
    el.compareTable.innerHTML = `<table class="data-table"><thead>${head}</thead><tbody>${body}</tbody></table>`;
  }

  // Overlaid MAE trend, one line per system, aligned on the union of dates.
  destroy(el, "compareChart");
  const trends = (model.compareTrends && model.compareTrends[key]) || {};
  const labelSet = {};
  systems.forEach((sys) => (trends[sys] || []).forEach((p) => { labelSet[p.date] = true; }));
  const labels = Object.keys(labelSet).sort();
  const datasets = systems.map((sys) => {
    const byDate = {};
    (trends[sys] || []).forEach((p) => { byDate[p.date] = p.mae; });
    const color = colorFor(sys);
    return {
      label: sysLabel(sys),
      data: labels.map((d) => (d in byDate ? byDate[d] : null)),
      borderColor: color,
      backgroundColor: color,
      pointRadius: 0,
      tension: 0.25,
      spanGaps: true,
      borderWidth: sys === model.detailSystem ? 3 : 1.5,
    };
  });
  el.charts.compareChart = lineChart(el.compareChart, {
    data: { labels, datasets },
    options: {
      interaction: { mode: "index", intersect: false },
      plugins: {
        tooltip: { callbacks: { label: (ctx) => ctx.dataset.label + ": " + (ctx.parsed.y == null ? "—" : fmt(ctx.parsed.y, 2)) } },
      },
    },
  });
}

// ---- Trend -----------------------------------------------------------------

function renderTrend(el, model, state) {
  const tp = (model.trends && model.trends[detailKey(state)]) || [];
  el.trendTitle.textContent = state.window <= 0
    ? "Rolling 7-day accuracy over the season"
    : "Daily accuracy — last " + state.window + " days";

  destroy(el, "trendChart");
  const t = themeColors();
  el.charts.trendChart = lineChart(el.trendChart, {
    data: {
      labels: tp.map((p) => p.date),
      datasets: [
        { label: "MAE", data: tp.map((p) => p.mae), borderColor: t.palette[0], backgroundColor: t.palette[0], pointRadius: 0, tension: 0.25 },
        { label: "Bias", data: tp.map((p) => p.bias), borderColor: t.palette[1], backgroundColor: t.palette[1], pointRadius: 0, tension: 0.25 },
      ],
    },
    options: {
      interaction: { mode: "index", intersect: false },
      plugins: { tooltip: { callbacks: { label: (ctx) => ctx.dataset.label + ": " + fmt(ctx.parsed.y, 2) } } },
    },
  });
}

// ---- By-position -----------------------------------------------------------

function renderPos(el, view) {
  const byPos = view.byPos || [];
  destroy(el, "posChart");
  const t = themeColors();
  el.charts.posChart = barChart(el.posChart, {
    data: {
      labels: byPos.map((p) => p.bucket),
      datasets: [
        { label: "MAE", data: byPos.map((p) => p.mae), backgroundColor: t.palette[0] },
        { label: "Bias", data: byPos.map((p) => p.bias), backgroundColor: t.palette[1] },
      ],
    },
  });

  if (byPos.length === 0) {
    el.posTable.innerHTML = `<p class="muted">No data.</p>`;
    return;
  }
  const rows = byPos.map((p) =>
    `<tr><td>${escapeHtml(p.bucket)}</td><td class="num">${fmt(p.mae, 2)}</td>` +
    `<td class="num">${p.bias > 0 ? "+" : ""}${fmt(p.bias, 2)}</td><td class="num">${(p.n || 0).toLocaleString()}</td></tr>`
  ).join("");
  el.posTable.innerHTML =
    `<table class="data-table"><thead><tr><th>Bucket</th><th class="num">MAE</th><th class="num">Bias</th><th class="num">N</th></tr></thead><tbody>${rows}</tbody></table>`;
}

// ---- Calibration -----------------------------------------------------------

function renderCalib(el, view) {
  const calib = view.calib || [];
  destroy(el, "calibChart");
  const t = themeColors();
  const pts = calib.map((p) => ({ x: p.proj, y: p.actual, n: p.n }));
  let max = 1;
  pts.forEach((p) => { max = Math.max(max, p.x, p.y); });
  el.charts.calibChart = scatterChart(el.calibChart, {
    data: {
      datasets: [
        { label: "avg actual per projected bucket", data: pts, backgroundColor: t.palette[2], pointRadius: 5 },
        { label: "perfect (y=x)", type: "line", data: [{ x: 0, y: 0 }, { x: max, y: max }], borderColor: t.muted, borderDash: [5, 5], pointRadius: 0 },
      ],
    },
    options: {
      // Overriding scales replaces the wrapper's themed axis, so ticks/grid are
      // re-supplied here (chart.js merges scales shallowly).
      scales: {
        x: { title: { display: true, text: "projected", color: t.muted }, ticks: { color: t.muted }, grid: { color: t.border } },
        y: { title: { display: true, text: "actual", color: t.muted }, ticks: { color: t.muted }, grid: { color: t.border } },
      },
      plugins: {
        tooltip: {
          callbacks: {
            label: (ctx) => ctx.datasetIndex === 0
              ? "proj " + ctx.raw.x.toFixed(1) + " → actual " + ctx.raw.y.toFixed(1) + "  (n=" + ctx.raw.n + ")"
              : "y=x",
          },
        },
      },
    },
  });
}

// ---- Insights --------------------------------------------------------------

function renderInsights(el, view) {
  const insights = view.insights || [];
  if (insights.length === 0) {
    el.insightList.innerHTML = `<li class="flat">No notable signals in this window.</li>`;
    return;
  }
  el.insightList.innerHTML = insights
    .map((i) => `<li class="${i.severity === "warn" ? "warn" : ""}">${escapeHtml(i.text)}</li>`)
    .join("");
}

// ---- Worst misses (client-sortable) ----------------------------------------

const MISS_COLS = [
  ["name", "Player", ""],
  ["bucket", "Pos", ""],
  ["date", "Date", ""],
  ["projected", "Proj", "num"],
  ["actual", "Actual", "num"],
  ["diff", "Diff", "num"],
];
const MISS_NUM_KEYS = new Set(["projected", "actual", "diff"]);

function renderMisses(el, view) {
  const misses = view.misses || [];
  if (misses.length === 0) {
    el.lastMisses = [];
    el.missTable.innerHTML = `<p class="muted">No data.</p>`;
    return;
  }
  el.lastMisses = misses;
  drawMisses(el);
}

function drawMisses(el) {
  const sort = el.missState;
  const data = el.lastMisses.slice(); // null key keeps server order (|diff| desc)
  if (sort.key) {
    const k = sort.key;
    const num = MISS_NUM_KEYS.has(k);
    data.sort((a, b) => {
      const c = num ? (a[k] - b[k]) : String(a[k]).localeCompare(String(b[k]));
      return sort.dir === "asc" ? c : -c;
    });
  }
  const arrow = (k) => (sort.key === k ? (sort.dir === "asc" ? " ▲" : " ▼") : "");
  const head = MISS_COLS
    .map(([k, label, c]) => `<th class="${c ? c + " " : ""}sortable" data-key="${k}">${escapeHtml(label)}${arrow(k)}</th>`)
    .join("");
  const rows = data.map((m) => {
    const cls = m.diff < 0 ? "over" : "under";
    return `<tr><td>${escapeHtml(m.name)}</td><td>${escapeHtml(m.bucket)}</td><td>${escapeHtml(m.date)}</td>` +
      `<td class="num">${fmt(m.projected)}</td><td class="num">${fmt(m.actual)}</td>` +
      `<td class="num ${cls}">${m.diff > 0 ? "+" : ""}${fmt(m.diff)}</td></tr>`;
  }).join("");
  el.missTable.innerHTML = `<table class="data-table"><thead><tr>${head}</tr></thead><tbody>${rows}</tbody></table>`;

  el.missTable.querySelectorAll("th[data-key]").forEach((th) => {
    th.addEventListener("click", () => {
      const key = th.dataset.key;
      if (sort.key === key) {
        sort.dir = sort.dir === "asc" ? "desc" : "asc";
      } else {
        sort.key = key;
        // Text columns default ascending; numeric columns descending (biggest first).
        sort.dir = MISS_NUM_KEYS.has(key) ? "desc" : "asc";
      }
      drawMisses(el);
    });
  });
}
