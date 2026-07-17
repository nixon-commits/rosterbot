// passkeys.js — lists registered passkeys and lets you add another device or
// revoke one you've lost. Mirrors jobs.js's card-per-item + error-card style.
import { api } from "./api.js";
import { registerPasskey } from "./webauthn.js";
import { escapeHtml } from "./render.js";

export async function renderPasskeys(root) {
  root.innerHTML = "<p class=\"muted\">Loading passkeys…</p>";
  let passkeys;
  try {
    const resp = await api.authPasskeys();
    passkeys = resp.passkeys;
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";
  root.appendChild(addButton(root));
  for (const pk of passkeys) {
    root.appendChild(passkeyCard(pk));
  }
}

function addButton(root) {
  const wrapper = document.createElement("div");
  wrapper.className = "card";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.textContent = "Add another passkey";
  const error = document.createElement("p");
  error.className = "error";
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    error.textContent = "";
    try {
      await registerPasskey();
      await renderPasskeys(root);
    } catch (err) {
      btn.disabled = false;
      error.textContent = "Could not add a passkey — try again.";
    }
  });
  wrapper.append(btn, error);
  return wrapper;
}

function passkeyCard(pk) {
  const card = document.createElement("div");
  card.className = "card";

  const id = document.createElement("p");
  id.textContent = "Passkey " + pk.id.slice(0, 12) + "…";
  card.appendChild(id);

  const revoke = document.createElement("button");
  revoke.type = "button";
  revoke.className = "danger";
  revoke.textContent = "Revoke";
  revoke.addEventListener("click", async () => {
    if (!confirm("Revoke this passkey? Its device will no longer be able to sign in.")) return;
    try {
      await api.authRevokePasskey(pk.id);
      card.remove();
    } catch (err) {
      alert("Could not revoke: " + escapeHtml(err.message));
    }
  });
  card.appendChild(revoke);

  return card;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  const p = document.createElement("p");
  p.className = "error";
  p.textContent = "Failed to load passkeys: " + err.message;
  card.appendChild(p);
  return card;
}
