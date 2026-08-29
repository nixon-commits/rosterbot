// tenants.js — the admin Tenants tab (rosterbot-crq.14, management rosterbot-2twx).
//
// Answers "what is failing, and for whom" across N tenants, and carries the
// operator's controls: invite, recovery links, park/reactivate, and the
// per-tenant lineup-writes kill switch. createElement/textContent throughout:
// every column here is user- or Fantrax-supplied (display name, email, a
// connection error string).
import { api, ApiError } from "./api.js";
import { el } from "./render.js";

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

// renderTenants draws the whole tab. minted, when present, is the response of
// an invite/recovery that just succeeded — the ONE render that can show the
// token, since only its hash is stored and no later fetch can recover it.
export async function renderTenants(root, minted) {
  root.textContent = "";

  let data;
  try {
    data = await api.tenants();
  } catch (err) {
    root.append(errorCard(err));
    return;
  }

  if (minted) root.append(mintedLinkCard(minted));
  root.append(inviteCard(root));

  const c = el("section", "card");
  c.append(el("h2", null, "Tenants"));

  const tenants = data.tenants || [];
  if (tenants.length === 0) {
    c.append(el("p", "muted", "No tenants yet — mint an invite above."));
    root.append(c);
    return;
  }

  // Stated up front, because it is the number that carries real-world weight:
  // how many people's rosters this deployment is allowed to edit.
  const writable = tenants.filter((t) => t.auto_apply).length;
  c.append(el("p", "muted",
    `${tenants.length} tenant${tenants.length === 1 ? "" : "s"}. ` +
    `rosterbot may change the lineup for ${writable} of them.`));

  const table = el("table", "data-table");
  const head = el("tr");
  for (const h of ["Tenant", "Team", "Passkey", "Fantrax", "Lineup writes", "Needs attention from", "Actions"]) {
    head.append(el("th", null, h));
  }
  table.append(el("thead").appendChild(head).parentNode);

  const body = el("tbody");
  for (const t of tenants) {
    body.append(tenantRow(t, root));
  }
  table.append(body);
  c.append(table);
  root.append(c);
}

function tenantRow(t, root) {
  const tr = el("tr");
  const parked = t.status === "parked";

  const who = el("td");
  who.append(el("div", null, t.display_name || String(t.id)));
  if (t.email) who.append(el("div", "muted small", t.email));
  if (t.role === "admin") who.append(el("span", "badge", "admin"));
  if (parked) who.append(el("span", "badge badge-failed", "parked"));
  tr.append(who);

  // An unset team is not blank information: connect refuses a tenant whose
  // record names no team (no_team), so this cell reads the same way the Passkey
  // column's "None yet" does — invited, but not yet able to finish.
  //
  // Only the EMPTY case is claimed here. team_id is a merged field
  // (internal/lineupapi/tenants.go falls back to the connection record's team
  // when the profile has none) while POST /v1/tenants/{id}/team writes the
  // profile, so a present value is not evidence this control has ever run and
  // nothing here may render it as one.
  const team = el("td");
  if (t.team_id) {
    team.append(el("span", "muted", t.team_id));
  } else {
    team.append(el("span", "badge badge-failed", "Not set"));
  }
  tr.append(team);

  // Zero passkeys is the "invited but never registered" signal; absent means
  // the credential store could not answer, which must not read as zero.
  const pk = el("td");
  if (t.passkeys === undefined || t.passkeys === null) {
    pk.append(el("span", "muted", "?"));
  } else if (t.passkeys === 0) {
    pk.append(el("span", "badge badge-failed", "None yet"));
  } else {
    pk.append(el("span", "muted", String(t.passkeys)));
  }
  tr.append(pk);

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
  tr.append(actionsCell(t, root, parked));
  return tr;
}

function actionsCell(t, root, parked) {
  const td = el("td", "tenant-actions");
  const rerender = (minted) => renderTenants(root, minted);
  const fail = (err) =>
    window.alert(err instanceof ApiError ? err.message : "request failed");

  const parkBtn = el("button", null, parked ? "Reactivate" : "Park");
  parkBtn.addEventListener("click", async () => {
    const name = t.display_name || t.email || String(t.id);
    if (!parked && !window.confirm(
      `Park ${name}? Their sign-in stops working and their scheduled jobs stop ` +
      `running until reactivated.`)) return;
    try {
      await api.tenantSetStatus(t.id, parked ? "active" : "parked");
      rerender();
    } catch (err) { fail(err); }
  });
  td.append(parkBtn);

  const writesBtn = el("button", null, t.auto_apply ? "Stop writes" : "Allow writes");
  writesBtn.addEventListener("click", async () => {
    const name = t.display_name || t.email || String(t.id);
    if (!t.auto_apply && !window.confirm(
      `Allow lineup writes for ${name}? rosterbot will APPLY lineup changes to ` +
      `their real Fantrax roster, not just propose them.`)) return;
    try {
      await api.tenantSetAutoApply(t.id, !t.auto_apply);
      rerender();
    } catch (err) { fail(err); }
  });
  td.append(writesBtn);

  // Team binding was CLI-only until rosterbot-yupr (`rosterbot user set-team`,
  // prod env vars required), which is why all three pilot testers were left
  // sitting at the no_team wall with no control on this page that could clear
  // it.
  //
  // The label is unconditional. "Change team" off a present team_id would be a
  // claim the wire cannot support: team_id merges the connection record's team
  // over an empty profile one, so a tenant whose profile is still unassigned
  // would read as already bound and the operator would walk past the one
  // control that repairs them. Labelling both cases "Set team" understates a
  // real reassignment, which the server refuses anyway.
  const teamBtn = el("button", null, "Set team");
  teamBtn.addEventListener("click", async () => {
    const name = t.display_name || t.email || String(t.id);
    const next = window.prompt(
      `Fantrax team id for ${name}. This records which team to PROVE — ` +
      `ownership is still proven at connect against Fantrax itself, so naming ` +
      `the wrong one makes their next connect fail rather than granting it.`,
      t.team_id || "");
    if (next === null) return;
    try {
      // A cleared field is posted rather than swallowed: the server refuses it
      // with the reason (an empty team is exactly what connect rejects), and a
      // silent return would leave the operator staring at an unchanged row with
      // no explanation. The reassignment refusal reaches them the same way.
      await tenantSetTeam(t.id, next.trim());
      rerender();
    } catch (err) { fail(err); }
  });
  td.append(teamBtn);

  const recoverBtn = el("button", null, "Recovery link");
  recoverBtn.addEventListener("click", async () => {
    try {
      const resp = await api.tenantRecovery(t.id);
      rerender({ resp, kind: "recovery" });
    } catch (err) { fail(err); }
  });
  td.append(recoverBtn);

  // Delete is the removal park cannot be, and the confirm spells out the
  // difference: everything about the account goes, and there is no undo —
  // only a fresh invite (which their released email makes possible again).
  const deleteBtn = el("button", "danger", "Delete");
  deleteBtn.addEventListener("click", async () => {
    const name = t.display_name || t.email || String(t.id);
    if (!window.confirm(
      `Delete ${name}? Their account, passkeys and Fantrax connection are ` +
      `removed permanently — there is no undo. Their email and team become ` +
      `available to invite again. (To pause them instead, use Park.)`)) return;
    try {
      await api.tenantDelete(t.id);
      rerender();
    } catch (err) { fail(err); }
  });
  td.append(deleteBtn);

  return td;
}

// tenantSetTeam POSTs the team binding.
//
// It is the one tenant call not on api.js's `api` object, and it belongs there
// beside tenantSetStatus — it is written out here only because api.js was
// outside the scope of the change that added this control. Fold it in and
// delete this. It mirrors request()'s contract exactly: same-origin, JSON
// body, and an ApiError carrying the server's own message, which is what puts
// the reassignment refusal's release instructions in front of the operator.
async function tenantSetTeam(id, teamID) {
  const res = await fetch(`/v1/tenants/${encodeURIComponent(id)}/team`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ team_id: teamID }),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const data = await res.json();
      if (data.error) msg = data.error;
    } catch {
      // body wasn't JSON; keep statusText
    }
    throw new ApiError(res.status, msg);
  }
  return res.json();
}

// inviteCard is the admin's minting form — the dashboard twin of `rosterbot
// invite`. Email is required; team can be attached later via connect.
function inviteCard(root) {
  const c = el("section", "card");
  c.append(el("h2", null, "Invite"));
  c.append(el("p", "muted small",
    "Mints a single-use enrollment link. Deliver it out-of-band (text, DM) — " +
    "that delivery is what attests the email address."));

  const form = el("form", "connect-form");
  const email = el("input");
  email.type = "email";
  email.placeholder = "email (required)";
  email.required = true;
  const name = el("input");
  name.type = "text";
  name.placeholder = "display name";
  const team = el("input");
  team.type = "text";
  // Named for its consequence, not its optionality: every pilot tester was
  // invited with this box empty and hit connect's no_team wall.
  team.placeholder = "Fantrax team id (optional — without it they cannot connect yet)";
  const submit = el("button", "primary", "Mint invite link");
  const status = el("p", "muted small");
  form.append(email, name, team, submit, status);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    status.textContent = "Minting…";
    try {
      const resp = await api.tenantInvite({
        email: email.value.trim(),
        name: name.value.trim(),
        team_id: team.value.trim(),
      });
      renderTenants(root, { resp, kind: "invite" });
    } catch (err) {
      submit.disabled = false;
      status.textContent =
        err instanceof ApiError ? err.message : "could not mint the invite";
    }
  });

  c.append(form);
  return c;
}

// mintedLinkCard is the single showing of a minted enrollment link. Only the
// token's hash is stored server-side, so once this render is gone the link is
// unrecoverable — mint another and let this one expire.
function mintedLinkCard(minted) {
  const { resp, kind } = minted;
  const c = el("section", "card minted-link");
  c.append(el("h2", null, kind === "recovery" ? "Recovery link" : "Invite link"));
  c.append(el("p", null,
    `For ${resp.email || resp.user_id} — expires ${new Date(resp.expires_at).toLocaleString()}.`));

  const link = `${window.location.origin}/?token=${resp.token}`;
  const row = el("div", "link-row");
  const box = el("input");
  box.type = "text";
  box.readOnly = true;
  box.value = link;
  const copy = el("button", "primary", "Copy");
  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(link);
      copy.textContent = "Copied";
    } catch {
      box.select();
      copy.textContent = "Select + copy manually";
    }
  });
  row.append(box, copy);
  c.append(row);

  c.append(el("p", "muted small",
    "Shown once — only its hash is stored. Deliver it out-of-band; " +
    "if it is lost, mint another and let this one expire."));
  return c;
}

function attentionFrom(t) {
  if (t.status === "parked") return "Nobody — parked";
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
