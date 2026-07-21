// chart.js — thin wrappers over the vendored global Chart with theme-aware
// defaults read from CSS custom properties, so charts recolor with light/dark.
function css(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

export function themeColors() {
  return {
    fg: css("--fg", "#1a1a1a"),
    muted: css("--muted", "#6b7280"),
    border: css("--border", "#e5e7eb"),
    accent: css("--accent", "#2563eb"),
    palette: [
      css("--c1", "#4e79a7"), css("--c2", "#f28e2b"), css("--c3", "#59a14f"),
      css("--c4", "#e15759"), css("--c5", "#76b7b2"), css("--c6", "#edc948"),
      css("--c7", "#b07aa1"), css("--c8", "#ff9da7"),
    ],
  };
}

function base(t) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: { legend: { labels: { color: t.fg } } },
    scales: {
      x: { ticks: { color: t.muted }, grid: { color: t.border } },
      y: { ticks: { color: t.muted }, grid: { color: t.border } },
    },
  };
}

function make(type, canvas, cfg) {
  const t = themeColors();
  const b = base(t);
  return new Chart(canvas, {
    type,
    data: cfg.data,
    options: { ...b, ...(cfg.options || {}), plugins: { ...b.plugins, ...(cfg.options?.plugins || {}) }, scales: { ...b.scales, ...(cfg.options?.scales || {}) } },
  });
}

export const lineChart = (c, cfg) => make("line", c, cfg);
export const scatterChart = (c, cfg) => make("scatter", c, cfg);
export const barChart = (c, cfg) => make("bar", c, cfg);
