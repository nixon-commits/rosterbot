// tenants.js — the admin Tenants tab (rosterbot-crq.14).
//
// Answers "what is failing, and for whom" across N tenants without reading the
// ledger by hand. createElement/textContent throughout: every column here is
// user- or Fantrax-supplied (display name, email, a connection error string).
import { api, ApiError } from "./api.js";

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

const CONN_TONE = {
  verified: ["Connected", "badge-ok"],
  pending: ["Pending", "badge-info"],
  needs_reconnect: ["Needs reconnect", "badge-failed"],
};

// WHO CAN ACT ON THIS — the failure taxonomy from crq.14, applied at the point
// an operator actually reads it. The distinction matters because it decides
// whether the right response is to contact the user or to fix something here;
// without it every red row looks like the operator's problem.
const OPERATOR_ACTIONABLE = new Set(["bot_challenge"]);

export async function renderTenants(root) {
  let data;
  try {
    data = await api.tenants();
  } catch (err) {
    root.append(errorCard(err));
    return;
  }

  const c = el("section", "card");
  c.append(el("h2", null, "Tenants"));

  const tenants = data.tenants || [];
  if (tenants.length === 0) {
    c.append(el("p", "muted", "No active tenants."));
    root.append(c);
    return;
  }

  // Stated up front, because it is the number that carries real-world weight:
  // how many people's rosters this deployment is allowed to edit.
  const writable = tenants.filter((t) => t.auto_apply).length;
  c.append(el("p", "muted",
    `${tenants.length} active. rosterbot may change the lineup for ${writable} of them.`));

  const table = el("table", "data-table");
  const head = el("tr");
  for (const h of ["Tenant", "Team", "Fantrax", "Lineup writes", "Needs attention from"]) {
    head.append(el("th", null, h));
  }
  table.append(el("thead").appendChild(head).parentNode);

  const body = el("tbody");
  for (const t of tenants) {
    body.append(tenantRow(t));
  }
  table.append(body);
  c.append(table);
  root.append(c);
}

function tenantRow(t) {
  const tr = el("tr");

  const who = el("td");
  who.append(el("div", null, t.display_name || String(t.id)));
  if (t.email) who.append(el("div", "muted small", t.email));
  if (t.role === "admin") who.append(el("span", "badge", "admin"));
  tr.append(who);

  tr.append(el("td", null, t.team_id || "—"));

  const conn = el("td");
  const [label, tone] = t.conn_status
    ? CONN_TONE[t.conn_status] || [t.conn_status, "badge-info"]
    : ["Never connected", "badge-info"];
  conn.append(el("span", "badge " + tone, label));
  if (t.conn_error) conn.append(el("div", "muted small", t.conn_error));
  tr.append(conn);

  // The column an operator scans for. "On" means this deployment edits that
  // person's team, so it is spelled out rather than shown as a tick.
  const apply = el("td");
  apply.append(el("span", "badge " + (t.auto_apply ? "badge-ok" : "badge-info"),
    t.auto_apply ? "On" : "Propose only"));
  tr.append(apply);

  tr.append(el("td", null, attentionFrom(t)));
  return tr;
}

// attentionFrom routes a row by WHO CAN ACT, which is the whole point of the
// taxonomy. An expired credential is the tenant's to fix and no amount of
// operator effort helps; a bot challenge is the reverse, and telling that user
// to re-enter a working password would waste their time and mislead them.
function attentionFrom(t) {
  if (!t.conn_status) return "Nobody yet — has not connected";
  if (t.conn_status === "verified") return "—";
  if (OPERATOR_ACTIONABLE.has(t.conn_error)) return "You (not the tenant)";
  if (t.conn_status === "needs_reconnect") return "The tenant — they must reconnect";
  return "—";
}

function errorCard(err) {
  const c = el("section", "card");
  c.append(el("h2", null, "Tenants"));
  c.append(el("p", "error",
    err instanceof ApiError && err.status === 403
      ? "Admins only."
      : "Could not load tenants."));
  return c;
}
