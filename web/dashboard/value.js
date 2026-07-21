// value.js — native team-value view. Fetches the precomputed valuereport.Model
// JSON (internal/valuereport) and renders the multi-team HKB-value time
// series with a client-side metric selector, a team legend, and a standings
// table for the latest captured day. Ported from the deleted
// internal/valuereport/template.html inline <script>; the four value leaves
// (h_mlb, h_min, p_mlb, p_min) ship per (team, day) row, and every displayed
// metric is derived from them client-side — nothing here recomputes value
// from raw player/roster data.
import { api } from "./api.js";
import { lineChart, themeColors } from "./chart.js";
import { escapeHtml } from "./render.js";

// Keys + formulas mirror the template's METRICS table exactly, against
// valuereport.Model's SeriesRow JSON tags (h_mlb/h_min/p_mlb/p_min).
const METRICS = [
  ["total", "Total", (r) => r.h_mlb + r.h_min + r.p_mlb + r.p_min],
  ["mlb", "MLB", (r) => r.h_mlb + r.p_mlb],
  ["minors", "Minors", (r) => r.h_min + r.p_min],
  ["hitter", "Hitter", (r) => r.h_mlb + r.h_min],
  ["pitcher", "Pitcher", (r) => r.p_mlb + r.p_min],
];
const metricFn = (key) => METRICS.find((m) => m[0] === key)[2];
const metricLabel = (key) => METRICS.find((m) => m[0] === key)[1];

const fmt = (n) => (n == null ? "—" : n.toLocaleString());

export async function renderValue(root) {
  root.innerHTML = `<p class="muted">Loading team value…</p>`;
  let model;
  try {
    model = await api.reportValue();
  } catch (err) {
    root.innerHTML = `<div class="card"><p class="muted">No team-value data yet.</p></div>`;
    return;
  }
  if (model.empty) {
    root.innerHTML = `<div class="card"><p class="muted">Collecting team value — check back after the next daily run.</p></div>`;
    return;
  }

  const state = { metric: "total", hidden: new Set() };
  const el = buildLayout(root, model);
  wireMetricSeg(el, state, () => paintChart(el, model, state));
  wireLegend(el, model, state, () => paintChart(el, model, state));
  paintChart(el, model, state);
  paintStandings(el, model);
}

// buildLayout paints the static shell once (metric selector, team legend,
// chart canvas, standings table container) — everything that doesn't change
// across a metric switch or a legend toggle.
function buildLayout(root, model) {
  root.innerHTML = "";
  const wrap = document.createElement("div");
  wrap.className = "value";

  const span = model.first_date === model.last_date
    ? model.first_date
    : `${model.first_date} → ${model.last_date}`;
  const thin = model.dates.length < 5
    ? `<p class="muted">Collecting data since ${escapeHtml(model.first_date)} — this series accumulates one point per day (HKB has no history), so the trend fills in over the coming days.</p>`
    : "";

  wrap.innerHTML = `
    <div class="sub muted">${model.dates.length} day(s) · ${escapeHtml(span)} · ${model.teams.length} teams</div>
    ${thin}
    <div class="controls">
      <div class="toggle-group" data-ref="metricseg"></div>
      <div class="toggle-group" data-ref="legend"></div>
    </div>

    <section class="card">
      <div class="chart-box"><canvas data-ref="chart"></canvas></div>
    </section>

    <section class="card">
      <h2>Standings — ${escapeHtml(model.last_date)} <span class="muted">(HKB dynasty value)</span></h2>
      <div data-ref="standings"></div>
      <p class="sub muted">Matched = players joined to an HKB value / total rostered. A shortfall means the value totals undercount by the unmatched players.</p>
    </section>
  `;
  root.appendChild(wrap);

  const el = {};
  wrap.querySelectorAll("[data-ref]").forEach((n) => { el[n.dataset.ref] = n; });
  el.charts = {};
  return el;
}

// ---- Metric selector --------------------------------------------------------

function wireMetricSeg(el, state, rerender) {
  el.metricseg.innerHTML = "";
  for (const [key, label] of METRICS) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = label;
    b.dataset.key = key;
    b.classList.toggle("active", key === state.metric);
    b.addEventListener("click", () => {
      state.metric = key;
      el.metricseg.querySelectorAll("button").forEach((btn) => btn.classList.toggle("active", btn.dataset.key === key));
      rerender();
    });
    el.metricseg.appendChild(b);
  }
}

// ---- Team legend (+ All/None) -----------------------------------------------
// A custom legend (not Chart.js's built-in one) so hidden-team state survives
// the destroy-and-rebuild that happens on every metric switch — Chart.js's
// own legend-driven visibility lives on the chart instance and would reset.

function wireLegend(el, model, state, rerender) {
  el.legend.innerHTML = "";
  for (const t of model.teams) {
    const b = document.createElement("button");
    b.type = "button";
    b.dataset.team = t.id;
    b.innerHTML = `<span class="swatch" style="background:${t.color}"></span> ${escapeHtml(t.name || t.id)}`;
    b.classList.toggle("active", !state.hidden.has(t.id));
    b.addEventListener("click", () => {
      if (state.hidden.has(t.id)) state.hidden.delete(t.id); else state.hidden.add(t.id);
      b.classList.toggle("active", !state.hidden.has(t.id));
      rerender();
    });
    el.legend.appendChild(b);
  }

  const allBtn = document.createElement("button");
  allBtn.type = "button";
  allBtn.textContent = "All";
  allBtn.addEventListener("click", () => {
    state.hidden.clear();
    el.legend.querySelectorAll("button[data-team]").forEach((b) => b.classList.add("active"));
    rerender();
  });
  const noneBtn = document.createElement("button");
  noneBtn.type = "button";
  noneBtn.textContent = "None";
  noneBtn.addEventListener("click", () => {
    model.teams.forEach((t) => state.hidden.add(t.id));
    el.legend.querySelectorAll("button[data-team]").forEach((b) => b.classList.remove("active"));
    rerender();
  });
  el.legend.appendChild(allBtn);
  el.legend.appendChild(noneBtn);
}

// ---- Chart -------------------------------------------------------------

function destroy(el, id) {
  if (el.charts[id]) { el.charts[id].destroy(); delete el.charts[id]; }
}

// index[teamID][dt] -> series row (ported from the template's buildIndex()).
function buildIndex(model) {
  const idx = {};
  for (const s of model.series) {
    (idx[s.team] = idx[s.team] || {})[s.dt] = s;
  }
  return idx;
}

function datasetsFor(model, state) {
  const fn = metricFn(state.metric);
  const idx = buildIndex(model);
  return model.teams.map((t) => ({
    label: t.name || t.id,
    borderColor: t.color,
    backgroundColor: t.color,
    borderWidth: 2,
    pointRadius: model.dates.length <= 30 ? 2.5 : 0,
    pointHoverRadius: 4,
    tension: 0.2,
    spanGaps: true,
    hidden: state.hidden.has(t.id),
    data: model.dates.map((d) => {
      const row = idx[t.id] && idx[t.id][d];
      return row ? fn(row) : null;
    }),
  }));
}

function paintChart(el, model, state) {
  destroy(el, "chart");
  const t = themeColors();
  el.charts.chart = lineChart(el.chart, {
    data: { labels: model.dates, datasets: datasetsFor(model, state) },
    options: {
      interaction: { mode: "nearest", intersect: false },
      plugins: {
        // Legend visibility is driven by the custom legend above, not
        // Chart.js's built-in one.
        legend: { display: false },
        tooltip: { callbacks: { label: (c) => `${c.dataset.label}: ${c.parsed.y == null ? "—" : fmt(c.parsed.y)}` } },
      },
      // Only the y scale is overridden, so it must re-supply ticks/grid
      // colors itself (chart.js's wrapper merges `scales` shallowly, one
      // level up from x/y) — the x scale is left alone and keeps the
      // wrapper's theme-aware defaults.
      scales: {
        y: {
          ticks: { color: t.muted, callback: (v) => fmt(v) },
          grid: { color: t.border },
          title: { display: true, text: metricLabel(state.metric) + " HKB value", color: t.muted },
        },
      },
    },
  });
}

// ---- Standings ---------------------------------------------------------

function coverageCell(matched, rostered) {
  const warn = matched < rostered ? " warn" : "";
  return `<td class="num cov${warn}">${matched}/${rostered}</td>`;
}

function paintStandings(el, model) {
  const rows = model.latest.map((r, i) => {
    const badge = r.logo
      ? `<img class="logo" src="${escapeHtml(r.logo)}" data-color="${escapeHtml(r.color)}" alt=""/>`
      : `<span class="swatch" style="background:${r.color}"></span>`;
    return `<tr class="${i === 0 ? "lead" : ""}">
      <td><div class="team">${badge}<span>${escapeHtml(r.name || r.team)}</span></div></td>
      <td class="num">${fmt(r.total)}</td>
      <td class="num">${fmt(r.mlb)}</td>
      <td class="num">${fmt(r.minors)}</td>
      <td class="num">${fmt(r.hitter)}</td>
      <td class="num">${fmt(r.pitcher)}</td>
      ${coverageCell(r.matched, r.rostered)}
    </tr>`;
  }).join("");

  el.standings.innerHTML = `
    <table class="data-table">
      <thead><tr>
        <th>Team</th><th class="num">Total</th><th class="num">MLB</th><th class="num">Minors</th>
        <th class="num">Hitter</th><th class="num">Pitcher</th><th class="num">Matched</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table>`;

  // A broken logo URL swaps in the team's color swatch instead of a broken-
  // image icon (parity with the deleted template's onerror fallback), wired
  // via addEventListener rather than an inline handler so no data ever needs
  // to round-trip through an HTML attribute string.
  el.standings.querySelectorAll("img.logo").forEach((img) => {
    img.addEventListener("error", () => {
      const span = document.createElement("span");
      span.className = "swatch";
      span.style.background = img.dataset.color;
      img.replaceWith(span);
    }, { once: true });
  });
}
