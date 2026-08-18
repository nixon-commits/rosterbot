// passkeys.js — the Passkeys section of the Settings page (rosterbot-jxjq).
//
// No longer a standalone tab: passkeys are account state, and the account page
// is Settings. createElement/textContent throughout, following settings.js —
// names are user-supplied and reach the DOM.
import { api } from "./api.js";
import { registerPasskey, ceremonyErrorMessage } from "./webauthn.js";

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

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

  c.append(addButton(c));
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
    if (!window.confirm(
      "Revoke this passkey? Its device can no longer sign in, and every " +
      "signed-in session ends — including this one.")) return;
    try {
      await api.authRevokePasskey(pk.id);
      await fillPasskeysCard(cardEl);
    } catch (err) {
      window.alert("Could not revoke: " + (err.message || err));
    }
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
