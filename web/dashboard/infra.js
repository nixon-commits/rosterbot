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
  orphan:
    "These objects belong to tenants that no longer exist. Deleting a tenant " +
    "releases their account but deliberately leaves their durable artifacts " +
    "behind, so the data outlives the user.\n\n" +
    "They still occupy the bucket, but no job will ever write to them again — " +
    "so they carry no freshness signal and are excluded from this row's " +
    "verdict. Judging them would leave the row permanently red with no action " +
    "that could clear it.",
  skipped:
    "The producer ran on these days and correctly found nothing to record — no " +
    "rostered player appeared in a game, so there was nothing to grade. Each " +
    "one carries a marker object naming the reason and the counts behind it.\n\n" +
    "They are listed because the marker is what stops them being counted as " +
    "missing, which also made them indistinguishable from a graded day. A day " +
    "here needs no action; a day under 'missing' does.",
  lostgap:
    "A day is graded by joining its actual results against the projection " +
    "snapshot captured on that day. These days have no snapshot — the job that " +
    "captures them did not complete — and a rest-of-season projection cannot be " +
    "fetched retroactively, so re-running the producer would write nothing.\n\n" +
    "Days listed as re-runnable are missing a run, not their input. Those do " +
    "fill in.",
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

// fmtGap renders one gap entry. The server sends a bare date unless more than
// one tenant is present, in which case it qualifies it as "<uid>/<date>" to
// keep the two apart. A tenant id is an opaque 87-character WebAuthn handle, so
// the date leads and the id trails as a short muted tag — enough to tell two
// tenants apart at a glance, without burying what the row is about.
function fmtGap(g) {
  const cut = g.lastIndexOf("/");
  if (cut < 0) return `<span class="mono">${escapeHtml(g)}</span>`;
  const uid = g.slice(0, cut);
  const date = g.slice(cut + 1);
  return `<span class="mono">${escapeHtml(date)}</span><span class="muted" title="${escapeHtml(
    uid,
  )}"> ·${escapeHtml(uid.slice(0, 6))}…</span>`;
}

function gapBlock(days, permanent, footer) {
  const shown = days.slice(0, 8).map(fmtGap).join(", ");
  const more = days.length > 8 ? ` +${days.length - 8} more` : "";
  return `<div class="infra-gap${permanent ? " infra-gap-permanent" : ""}">
      ${days.length} missing day${days.length === 1 ? "" : "s"}: ${shown}${more}
      ${footer}
    </div>`;
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
    // Qualify the headline count where some of it is skips, so the number is
    // not read as "N days of data" (rosterbot-36r).
    //
    // Counted as DISTINCT DAYS, not entries. `partitions` is a union of dt=
    // days across tenants, while `skipped` is computed per tenant and carries a
    // "uid/" prefix once there is more than one — so a day skipped for two
    // tenants is two entries against one partition, and the raw lengths could
    // read "5 days (7 skipped)". Stripping the qualifier restores the subset
    // relationship the parenthetical asserts.
    const nSkipped = new Set((a.skipped || []).map((d) => d.slice(d.lastIndexOf("/") + 1))).size;
    if (nSkipped) detail += ` <span class="muted">(${nSkipped} skipped)</span>`;
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
    if (a.no_backfill) {
      // The whole artifact is unbackfillable, so every gap has one story.
      detail += gapBlock(a.gaps, true, `<div class="infra-warn">Gone for good <span data-help="nobackfill"></span></div>`);
    } else {
      // Otherwise the same series holds both kinds at once, and telling an
      // operator to re-run a job that structurally cannot work is worse than
      // saying nothing — it turns permanent loss into a chore that never
      // completes. Split them.
      const lost = new Set(a.lost_gaps || []);
      const rerunnable = a.gaps.filter((g) => !lost.has(g));
      if (rerunnable.length) {
        detail += gapBlock(rerunnable, false, `<div class="muted">Re-runnable</div>`);
      }
      if (lost.size) {
        detail += gapBlock(
          a.gaps.filter((g) => lost.has(g)),
          true,
          `<div class="infra-warn">Input never captured — cannot be re-run <span data-help="lostgap"></span></div>`,
        );
      }
    }
  }

  // Deliberately skipped days. Reported separately from gaps because it is the
  // opposite finding — the producer covered the day and found nothing, rather
  // than never reaching it — and separately from the plain partition count,
  // which cannot tell the two apart. Muted, not warned: nothing is wrong.
  if (a.skipped && a.skipped.length) {
    const days = a.skipped;
    const shown = days.slice(0, 8).map(fmtGap).join(", ");
    const more = days.length > 8 ? ` +${days.length - 8} more` : "";
    detail += `<div class="infra-detail muted">${days.length} skipped day${
      days.length === 1 ? "" : "s"
    }: ${shown}${more} <span data-help="skipped"></span></div>`;
  }

  // Excluded from the verdict, but not from the page: a segment that is
  // filtered out and reported nowhere is indistinguishable from one that was
  // never there.
  if (a.orphan_tenants) {
    const n = a.orphan_tenants;
    detail += `<div class="infra-detail muted">${n} orphaned tenant${
      n === 1 ? "" : "s"
    } <span data-help="orphan"></span></div>`;
  }

  if (a.error) {
    detail += `<div class="infra-gap">Listing failed: <span class="mono">${escapeHtml(a.error)}</span></div>`;
  }

  // worst_tenant names the tenant whose partition produced this row's health,
  // so a red row says WHOSE data is stale rather than sending the operator to
  // list S3 by hand. Same treatment as fmtGap's uid tag: the 87-char WebAuthn
  // handle trails as a short muted token with the full id in the title.
  const worst =
    a.worst_tenant && a.health !== "ok"
      ? `<span class="muted" title="${escapeHtml(a.worst_tenant)}">·${escapeHtml(
          a.worst_tenant.slice(0, 6),
        )}…</span>`
      : "";

  return `<div class="card infra-card">
    <div class="infra-head">
      <strong>${escapeHtml(a.name)}</strong>
      ${healthBadge(a.health)}${worst}
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
