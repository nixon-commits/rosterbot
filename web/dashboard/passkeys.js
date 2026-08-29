// passkeys.js — the Passkeys section of the Settings page (rosterbot-jxjq).
//
// No longer a standalone tab: passkeys are account state, and the account page
// is Settings. createElement/textContent throughout, following settings.js —
// names are user-supplied and reach the DOM.
import { api } from "./api.js";
import { registerPasskey, ceremonyErrorMessage } from "./webauthn.js";
import { el } from "./render.js";
import { stopLive } from "./live.js";

// renderPasskeysSection appends the Passkeys card to container and manages its
// own refreshes, so settings.js only has to place it.
export async function renderPasskeysSection(container) {
  const c = el("section", "card");
  c.append(el("h2", null, "Passkeys"));
  container.append(c);
  await fillPasskeysCard(c);
}

async function fillPasskeysCard(c) {
  while (c.childNodes.length > 1) c.removeChild(c.lastChild); // keep the <h2>

  let passkeys;
  try {
    const resp = await api.authPasskeys();
    passkeys = resp.passkeys || [];
  } catch (err) {
    c.append(el("p", "error", "Failed to load passkeys: " + (err.message || err)));
    return;
  }

  if (passkeys.length === 0) {
    c.append(el("p", "muted", "No passkeys registered."));
  } else {
    const table = el("table", "data-table");
    const head = el("tr");
    for (const h of ["Name", "Created", ""]) head.append(el("th", null, h));
    table.append(el("thead").appendChild(head).parentNode);

    const body = el("tbody");
    for (const pk of passkeys) body.append(passkeyRow(pk, c));
    table.append(body);
    c.append(table);
  }

  // The lockout state is stated on the page, not only inside the confirm()
  // dialog. A warning that lives only in a modal is wrong invisibly if the
  // count is ever wrong; this line puts the same fact on screen at load, where
  // it can be seen to be missing.
  //
  // Muted rather than .error on purpose: one passkey is an ordinary state for
  // most accounts, and a line that is permanently red is how a page teaches you
  // to stop reading it.
  if (passkeys.length === 1) {
    c.append(el("p", "muted",
      "Only one passkey is registered. Revoking it would lock this account " +
      "out — there is no password, so getting back in needs an admin to issue " +
      "a new enrollment link, and if you are the only admin there is nobody " +
      "else who can issue one."));
  }

  c.append(addButton(c));
}

// isLastPasskeyIn counts the rows the card is currently showing instead of
// taking a total as an argument. A threaded-in count fails silently: drop that
// argument in a later edit and it is `undefined`, the last-passkey branch is
// permanently unreachable, the lockout warning never fires again, and nothing
// anywhere goes red — one click from an account that cannot be recovered from
// this browser.
//
// Unknown fails toward "this might be the last one": only a confirmed count of
// two or more suppresses the warning, so a missing tbody, an emptied card or an
// unexpected DOM shape over-warns. An extra confirmation costs a click; a
// missing one costs the account.
function isLastPasskeyIn(cardEl) {
  const rows = cardEl ? cardEl.querySelectorAll("tbody tr").length : 0;
  return !(rows > 1);
}

function passkeyRow(pk, cardEl) {
  const tr = el("tr");

  const name = el("td");
  if (pk.name) {
    name.append(el("span", null, pk.name));
  } else {
    // The id fragment keeps two unnamed passkeys distinguishable — otherwise
    // "which one is my lost phone?" has no answer on this page.
    name.append(el("span", "muted", "Unnamed (" + pk.id.slice(0, 8) + "…)"));
  }
  tr.append(name);

  // A passkey registered before dates existed has none; showing "—" is honest
  // where a backfilled guess would not be.
  tr.append(el("td", "muted",
    pk.created_at ? new Date(pk.created_at).toLocaleDateString() : "—"));

  const actions = el("td", "tenant-actions");
  const rename = el("button", null, pk.name ? "Rename" : "Name");
  rename.addEventListener("click", async () => {
    const next = window.prompt("Name this passkey (e.g. “Jon's phone”):", pk.name || "");
    if (next === null) return;
    try {
      await api.authRenamePasskey(pk.id, next.trim());
      await fillPasskeysCard(cardEl);
    } catch (err) {
      window.alert("Could not save the name: " + (err.message || err));
    }
  });
  actions.append(rename);

  const revoke = el("button", "danger", "Revoke");
  revoke.addEventListener("click", async () => {
    // Revoking the LAST passkey is not a bigger version of revoking one of
    // five, it is a different outcome: permanent lockout, recoverable only by
    // an admin minting a fresh enrollment link. One confirm string for both
    // cases put that one click away with nothing naming it.
    const isLast = isLastPasskeyIn(cardEl);
    if (!window.confirm(isLast
      ? "This is your LAST passkey.\n\nRevoking it signs you out and leaves " +
        "no way to sign back in — this account has no password, so it can " +
        "only be recovered by an admin issuing a new enrollment link, and if " +
        "you are the only admin there is nobody else who can issue one.\n\n" +
        "Revoke it anyway?"
      : "Revoke this passkey? Its device can no longer sign in, and every " +
        "signed-in session ends — including this one.")) return;
    try {
      await api.authRevokePasskey(pk.id);
    } catch (err) {
      window.alert("Could not revoke: " + (err.message || err));
      return;
    }
    // A successful revoke ALWAYS ends this session: handleRevokePasskey bumps
    // TokenVersion unconditionally before answering 204, so every /v1/* call
    // from this page now 401s. Refreshing the card here therefore repainted it
    // as "Failed to load passkeys: unauthorized" — a broken card in place of
    // any hint that the user had just been signed out.
    //
    // Say so, then re-gate through boot() the way live.js does when its poll
    // hits a 401. stopLive() first for the same reason it does: the reload is
    // not instant and the 5s run poller must not fire into a dead session.
    window.alert(isLast
      ? "Passkey revoked. You are now signed out, and this account has no " +
        "passkeys left — signing back in needs a new enrollment link."
      : "Passkey revoked. Revoking ends every session, so you have been " +
        "signed out and will need to sign in again.");
    try {
      // Same order as app.js's logout button: drop the now-dead cookie server
      // side before tearing down the poller, so the reloaded page doesn't spend
      // a 401 probe rediscovering a session the bumped TokenVersion already
      // killed. Best-effort — the sign-out has already happened either way.
      await api.authLogout();
    } catch {
      // ignore
    }
    stopLive();
    window.location.reload();
  });
  actions.append(revoke);
  tr.append(actions);

  return tr;
}

function addButton(cardEl) {
  const wrap = el("div");
  const btn = el("button", "primary", "Add another passkey");
  const error = el("p", "error");
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    error.textContent = "";
    try {
      await registerPasskey();
      await fillPasskeysCard(cardEl);
    } catch (err) {
      btn.disabled = false;
      error.textContent = ceremonyErrorMessage(err);
    }
  });
  wrap.append(btn, error);
  return wrap;
}
