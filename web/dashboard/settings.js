// settings.js — the per-user account page (rosterbot-crq.5).
//
// Built with createElement/textContent throughout, following views.js,
// trades.js and football.js. NOT value.js, which predates the rule and uses
// innerHTML with an escapeHtml that historically did not escape quotes — this
// page renders a user's own email and a Fantrax error string, both of which
// reach an attribute context.
import { api, ApiError } from "./api.js";
import { renderLeaguesSection } from "./leagues.js";
import { renderPasskeysSection } from "./passkeys.js";
import { el } from "./render.js";
// CONNECTION_COPY and FAILURE_COPY moved to connstate.js when the Runs tab
// started rendering the same vocabulary (rosterbot-jg92); two copies would let
// Settings and Runs disagree about what went wrong.
import { CONNECTION_COPY, FAILURE_COPY } from "./connstate.js";

function card(title) {
  const c = el("section", "card");
  c.append(el("h2", null, title));
  return c;
}

// Rows use the established .kv-table pattern rather than bare spans: the
// dashboard already styles it, and hand-rolled spans rendered "NameAlice
// Manager" with no separation.
function kvTable(pairs) {
  const t = el("table", "kv-table");
  const body = el("tbody");
  for (const [k, v, note] of pairs) {
    const tr = el("tr");
    tr.append(el("th", null, k));
    const td = el("td", null, v);
    if (note) td.append(el("div", "muted small", note));
    tr.append(td);
    body.append(tr);
  }
  t.append(body);
  return t;
}

export async function renderSettings(root) {
  let me;
  try {
    me = await api.me();
  } catch (err) {
    root.append(errorCard(err));
    return;
  }

  root.append(accountCard(me));
  root.append(fantraxCard(me));
  // Sleeper sits directly under Fantrax: both answer "which leagues am I in",
  // and separating them by the auto-apply card would read as two unrelated
  // features. The section manages its own refreshes, like passkeys below.
  renderLeaguesSection(root);
  root.append(autoApplyCard(me));
  root.append(projectionCard(me));
  // Passkeys are account state, so they live here rather than on a tab of
  // their own (rosterbot-jxjq). The section manages its own refreshes.
  renderPasskeysSection(root);
}

function accountCard(me) {
  const c = card("Account");
  const rows = [
    ["Name", me.display_name || "—"],
    ["Email", me.email || "—"],
    ["Role", me.role === "admin" ? "Admin" : "Member"],
  ];
  if (me.team_id) {
    // The note is there because a read-only field with no explanation reads as
    // a bug rather than as a deliberate constraint.
    rows.push(["Fantrax team", me.team_id,
      "Proven against Fantrax when you connect, not typed in."]);
  }
  c.append(kvTable(rows));
  return c;
}

function fantraxCard(me) {
  const c = card("Fantrax");
  const conn = me.fantrax;
  let [label, tone] = conn
    ? CONNECTION_COPY[conn.status] || [conn.status, "badge-info"]
    : ["Not connected", "badge-info"];
  // no_team is the one failure where the credentials are FINE — the sign-in
  // succeeded and the refusal came from the missing team assignment. The
  // generic needs_reconnect headline ("your saved credentials no longer
  // work") sent people back to re-typing a password that was never wrong.
  if (conn && conn.last_error === "no_team") {
    [label, tone] = ["Not connected — no team assigned to this account", "badge-failed"];
  }

  const badge = el("span", "badge " + tone, label);
  c.append(badge);

  // A last_error on a VERIFIED connection is not something the tenant can act
  // on, and telling them otherwise is actively harmful. cmd/session_ladder.go's
  // stop() writes conn.LastError on all three of its routes but only moves the
  // status on the tenant-actionable one, so both the operator route
  // (bot_challenge) and the post-auth route (verification_interrupted, added by
  // rosterbot-ch0s) leave a verified connection carrying a class. Rendering
  // FAILURE_COPY there put a red "Try connecting again in a minute" under a
  // green "Connected" badge — and that retry is a fresh chromedp login, which
  // is the documented Fantrax lockout trigger. The connection works and the
  // next scheduled run retries the refresh on its own.
  //
  // Still said, not swallowed: the note names the refresh so a tenant looking
  // at the page during an outage is not told everything is fine. Degrade to
  // noise, never to silence.
  if (conn && conn.last_error && conn.status === "verified") {
    c.append(el("p", "muted small",
      "A background session refresh did not finish. rosterbot is still " +
      "connected and will retry on its own — there is nothing to do here."));
  } else if (conn && conn.last_error) {
    c.append(el("p", "error", FAILURE_COPY[conn.last_error] || conn.last_error));
  }
  if (conn && conn.last_verified_at && conn.status === "verified") {
    c.append(el("p", "muted small",
      "Last checked " + new Date(conn.last_verified_at).toLocaleString()));
  }

  c.append(disclosure());
  c.append(connectForm(conn && conn.status === "verified"));
  return c;
}

// disclosure is a SHIPPING ARTIFACT, not a nicety (rosterbot-crq.14).
//
// Two things were decided in the design that a person must be told plainly
// BEFORE they hand over credentials: their Fantrax password is stored, and the
// admin can read any tenant's data. The admin is a competitor in this league.
// Someone who would decline that deserves to decline it before typing, not
// after — so this sits above the form, unfoldable, not behind a link.
function disclosure() {
  const d = el("div", "disclosure");
  d.append(el("h3", null, "Before you connect, please read this"));
  const ul = el("ul");
  for (const item of [
    "Your Fantrax password is stored, encrypted, so the bot can sign in as you " +
      "when Fantrax ends its session. It is not stored as a one-way hash — it " +
      "can be decrypted by the part of the system that signs in.",
    "The administrator of this deployment can read any account's data, " +
      "including your roster and your trade valuations. They also play in this " +
      "league.",
    "The bot will not change your lineup unless you turn that on below. It is " +
      "off until you do.",
  ]) {
    ul.append(el("li", null, item));
  }
  d.append(ul);
  return d;
}

function connectForm(connected) {
  const form = el("form", "connect-form");
  form.append(el("h3", null, connected ? "Reconnect" : "Connect your Fantrax account"));

  const user = el("input");
  user.type = "text";
  user.name = "username";
  user.autocomplete = "username";
  user.placeholder = "Fantrax username or email";
  user.required = true;

  const pass = el("input");
  pass.type = "password";
  pass.name = "password";
  pass.autocomplete = "current-password";
  pass.placeholder = "Fantrax password";
  pass.required = true;

  const submit = el("button", null, connected ? "Reconnect" : "Connect");
  submit.type = "submit";

  const status = el("p", "muted small");
  form.append(user, pass, submit, status);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    status.className = "muted small";
    status.textContent = "Signing in to Fantrax… this takes a moment.";
    try {
      await api.connect(user.value, pass.value);
      // Cleared immediately: the value has served its purpose and there is no
      // reason for it to sit in a DOM node afterwards.
      pass.value = "";
      status.textContent =
        "Started. Fantrax sign-in runs in the background — reload this page in a " +
        "minute to see the result.";
    } catch (err) {
      status.className = "error";
      status.textContent =
        err instanceof ApiError && err.status === 400
          ? "Please fill in both fields."
          : "Could not start the connection. Try again shortly.";
    } finally {
      submit.disabled = false;
    }
  });
  return form;
}

// autoApplyCard is the one control that decides whether the bot WRITES to
// somebody's roster, so it gets a card of its own rather than a checkbox in a
// row of them.
function autoApplyCard(me) {
  const c = card("Let the bot change my lineup");
  c.append(el("p", "muted",
    "Off by default. While this is off, rosterbot works out what it would do " +
    "and shows you, but never changes your Fantrax roster."));

  const label = el("label", "toggle");
  const box = el("input");
  box.type = "checkbox";
  box.checked = !!me.auto_apply;
  const state = el("span", null, box.checked
    ? "On — rosterbot may set your lineup"
    : "Off — rosterbot will only suggest");
  label.append(box, state);

  const status = el("p", "muted small");
  c.append(label, status);

  box.addEventListener("change", async () => {
    const want = box.checked;
    box.disabled = true;
    status.className = "muted small";
    status.textContent = "Saving…";
    try {
      await api.setPreferences({ auto_apply: want });
      state.textContent = want
        ? "On — rosterbot may set your lineup"
        : "Off — rosterbot will only suggest";
      status.textContent = "Saved.";
    } catch {
      // Put the control back where it was. A toggle that shows the new state
      // after failing to save it is worse than one that fails visibly — this
      // one decides whether somebody's team gets edited.
      box.checked = !want;
      status.className = "error";
      status.textContent = "Could not save that. Your setting is unchanged.";
    } finally {
      box.disabled = false;
    }
  });
  return c;
}

// PROJECTION_CHOICES: value "" is "follow the deployment default" — stored as
// empty on purpose (rosterbot-5qvs), so anyone who never chose tracks a future
// default change instead of being pinned to whatever it was the day they
// saved. The names are the base families; the run applies the rest-of-season
// variant itself in season.
const PROJECTION_CHOICES = [
  ["", "Default (Depth Charts)"],
  ["steamer", "Steamer"],
  ["depthcharts", "Depth Charts"],
  ["thebatx", "THE BAT X"],
  ["atc", "ATC"],
];

// projectionCard: which model values players in this user's lineup runs, per
// role. Harmless to flip (unlike auto-apply it can't touch a roster), so it
// follows the page's autosave-on-change pattern, reverting visibly on failure.
function projectionCard(me) {
  const c = card("Projection model");
  c.append(el("p", "muted",
    "Which projection system rosterbot uses to value your players — hitters " +
    "and pitchers can use different models. In season, the rest-of-season " +
    "version of the model is used automatically. Changes take effect from " +
    "the next hourly run."));

  c.append(projectionRow("Hitters", "hitter_projection", me.hitter_projection));
  c.append(projectionRow("Pitchers", "pitcher_projection", me.pitcher_projection));

  const hint = el("p", "muted small");
  const link = el("a", null, "the Projections tab");
  link.href = "#projections";
  hint.append("See how each model has been grading on ", link, ".");
  c.append(hint);
  return c;
}

function projectionRow(label, field, current) {
  const row = el("label", "projection-row");
  row.append(el("span", null, label));
  // Each row owns its status line: with one shared element, two quick saves
  // race and a Pitchers failure can replace a Hitters "Saved." with an error
  // that names neither control.
  const status = el("span", "muted small");

  const sel = el("select");
  for (const [value, name] of PROJECTION_CHOICES) {
    const opt = el("option", null, name);
    opt.value = value;
    sel.append(opt);
  }
  // A stored value this build doesn't know (a newer system) would silently
  // snap the control to "Default" and then SAVE that on the next change —
  // surface it as an extra option instead of misrepresenting the record.
  let saved = current || "";
  if (saved && !PROJECTION_CHOICES.some(([v]) => v === saved)) {
    const opt = el("option", null, saved);
    opt.value = saved;
    sel.append(opt);
  }
  sel.value = saved;

  sel.addEventListener("change", async () => {
    const want = sel.value;
    sel.disabled = true;
    status.className = "muted small";
    status.textContent = "Saving…";
    try {
      await api.setPreferences({ [field]: want });
      saved = want;
      status.textContent = "Saved.";
    } catch {
      // Same rule as the auto-apply toggle: a control showing a state it
      // failed to save is worse than one that fails visibly.
      sel.value = saved;
      status.className = "error";
      status.textContent = "Could not save that. Your setting is unchanged.";
    } finally {
      sel.disabled = false;
    }
  });

  row.append(sel, status);
  return row;
}

function errorCard(err) {
  const c = card("Settings");
  c.append(el("p", "error",
    err instanceof ApiError && err.status === 403
      ? "Sign in with a passkey to see your settings."
      : "Could not load your settings."));
  return c;
}
