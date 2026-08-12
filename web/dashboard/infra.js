// infra.js — state-bucket health view. Fetches GET /v1/infra, which lists S3
// live on every request rather than serving a precomputed file.
//
// That distinction is the whole design: the Projections and Value tabs read
// JSON written by a scheduled job, which is fine for data that is *about*
// yesterday. A status page built that way would go stale in exactly the
// situation it exists to detect — it could show "all healthy" while the job
// that writes it is the thing that died. So this view reads through, and
// displays the server's generated_at to prove the reading is current.
import { api } from "./api.js";
import { escapeHtml, help } from "./render.js";

// Cards are assembled as HTML strings, so help bubbles are attached in a pass
// afterwards: the markup leaves a <span data-help="key"> placeholder and
// attachHelp fills it from this table. Keeps the string-building style intact
// without interpolating a DOM node into it.
const HELP = {
  nobackfill:
    "These days cannot be recovered. HKB publishes only current rankings with " +
    "no history, and the daily rosters they were joined against are not " +
    "archived — so the composition of every team on those dates is unknowable " +
    "now, and re-running the job would produce nothing.\n\n" +
    "Every other gap on this page is a missing day the producing job can simply " +
    "be re-run for.",
  ephemeral:
    "A cache, not a record. Anything here is re-fetchable from upstream, so age " +
    "is not a health signal and losing it costs only the time to fetch again.",
};

function attachHelp(root) {
  root.querySelectorAll("[data-help]").forEach((slot) => {
    const text = HELP[slot.dataset.help];
    if (text) slot.replaceWith(help(text));
  });
}

const HEALTH = {
  ok: { label: "OK", cls: "health-ok" },
  gap: { label: "GAP", cls: "health-gap" },
  stale: { label: "STALE", cls: "health-stale" },
  missing: { label: "MISSING", cls: "health-missing" },
  unknown: { label: "?", cls: "health-unknown" },
};

// Worst-first, so the row that needs attention is never below the fold.
const SEVERITY = { gap: 0, missing: 1, stale: 2, unknown: 3, ok: 4 };

function fmtBytes(n) {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)} ${units[i]}`;
}

function fmtAge(seconds) {
  if (seconds == null || seconds <= 0) return "—";
  const m = seconds / 60;
  if (m < 60) return `${Math.round(m)}m ago`;
  const h = m / 60;
  if (h < 48) return `${Math.round(h)}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

function healthBadge(h) {
  const cfg = HEALTH[h] || HEALTH.unknown;
  return `<span class="badge ${cfg.cls}">${cfg.label}</span>`;
}

function artifactCard(a) {
  const bits = [];

  bits.push(`<span class="muted mono">${escapeHtml(a.prefix)}</span>`);
  // An empty prefix has no size or age worth printing — showing "0 obj · — · —"
  // is three tokens saying the same nothing.
  if (a.objects > 0) {
    bits.push(`${a.objects.toLocaleString()} obj · ${fmtBytes(a.bytes)}`);
    if (a.durable) bits.push(fmtAge(a.age_seconds));
  } else {
    bits.push(`<span class="muted">empty</span>`);
  }
  if (!a.durable) bits.push(`<span class="muted">ephemeral</span> <span data-help="ephemeral"></span>`);
  if (a.producer) bits.push(`<span class="muted">← ${escapeHtml(a.producer)}</span>`);

  let detail = "";

  if (a.partitioned && a.partitions) {
    detail += `<div class="infra-detail">${a.partitions} days`;
    if (a.latest_partition) detail += ` · latest <span class="mono">${escapeHtml(a.latest_partition)}</span>`;
    detail += `</div>`;
  }

  // Sub-dimensions: the four projection systems under analysis/grades/, the
  // per-source directories under archive/. A missing entry here is the
  // "one shadow system quietly stopped" case that raises no error anywhere.
  if (a.subkeys && a.subkeys.length) {
    detail += `<div class="infra-detail">${a.subkeys
      .map((s) => `<span class="chip">${escapeHtml(s)}</span>`)
      .join(" ")}</div>`;
  }

  if (a.gaps && a.gaps.length) {
    const permanent = a.no_backfill;
    const shown = a.gaps.slice(0, 8).map(escapeHtml).join(", ");
    const more = a.gaps.length > 8 ? ` +${a.gaps.length - 8} more` : "";
    detail += `<div class="infra-gap${permanent ? " infra-gap-permanent" : ""}">
      ${a.gaps.length} missing day${a.gaps.length === 1 ? "" : "s"}: <span class="mono">${shown}</span>${more}
      ${
        permanent
          ? `<div class="infra-warn">Gone for good <span data-help="nobackfill"></span></div>`
          : `<div class="muted">Re-runnable</div>`
      }
    </div>`;
  }

  if (a.error) {
    detail += `<div class="infra-gap">Listing failed: <span class="mono">${escapeHtml(a.error)}</span></div>`;
  }

  return `<div class="card infra-card">
    <div class="infra-head">
      <strong>${escapeHtml(a.name)}</strong>
      ${healthBadge(a.health)}
    </div>
    <div class="infra-meta">${bits.join(" · ")}</div>
    ${detail}
  </div>`;
}

export async function renderInfra(root) {
  root.innerHTML = `<p class="muted">Listing state bucket…</p>`;

  let status;
  try {
    status = await api.infra();
  } catch (err) {
    // Report the failure rather than an empty page: "couldn't read" and
    // "nothing is wrong" must never look the same on a status page.
    root.innerHTML = `<div class="card"><p class="error">Could not read infra status: ${escapeHtml(
      err.message || String(err),
    )}</p></div>`;
    return;
  }

  const items = [...(status.artifacts || [])].sort(
    (a, b) => (SEVERITY[a.health] ?? 9) - (SEVERITY[b.health] ?? 9),
  );

  const problems = items.filter((a) => a.health !== "ok").length;
  const summary = problems
    ? `<span class="badge health-stale">${problems} need${problems === 1 ? "s" : ""} attention</span>`
    : `<span class="badge health-ok">All healthy</span>`;

  root.innerHTML = `
    <div class="infra-summary">
      ${summary}
      <span class="muted">read live · ${new Date(status.generated_at).toLocaleTimeString()}</span>
      <button id="infra-refresh" type="button">Refresh</button>
    </div>
    <div class="infra-grid">${items.map(artifactCard).join("")}</div>
  `;

  attachHelp(root);
  root.querySelector("#infra-refresh")?.addEventListener("click", () => renderInfra(root));
}
