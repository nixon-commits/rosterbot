// app.js — login/bootstrap gate + hash router + nav wiring. ROUTES maps a URL
// hash to a view's render(root) function; later tasks add entries here.
import { api, ApiError } from "./api.js";
import { registerPasskey, loginWithPasskey } from "./webauthn.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";
import { renderRuns } from "./runs.js";
import { renderPasskeys } from "./passkeys.js";
import { renderProjections } from "./projections.js";
import { renderValue } from "./value.js";
import { renderTrades } from "./trades.js";
import { renderViews } from "./views.js";
import { renderInfra } from "./infra.js";
import { renderFootball } from "./football.js";
import { renderSettings } from "./settings.js";
import { renderTenants } from "./tenants.js";
import { startLive, stopLive } from "./live.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
  "#passkeys": renderPasskeys,
  "#projections": renderProjections,
  "#value": renderValue,
  "#trades": renderTrades,
  "#views": renderViews,
  "#infra": renderInfra,
  "#football": renderFootball,
  "#settings": renderSettings,
  "#tenants": renderTenants,
};
const DEFAULT_ROUTE = "#lineup";

const root = document.getElementById("view-root");
const loginScreen = document.getElementById("login-screen");
const bootstrapScreen = document.getElementById("bootstrap-screen");
const bootstrapPrompt = document.getElementById("bootstrap-prompt");
const bootstrapSubmit = document.getElementById("bootstrap-submit");
const bootstrapTokenField = document.getElementById("bootstrap-token-field");
const shell = document.getElementById("shell");
const passkeyLoginBtn = document.getElementById("passkey-login-btn");
const loginError = document.getElementById("login-error");
const bootstrapForm = document.getElementById("bootstrap-form");
const bootstrapError = document.getElementById("bootstrap-error");
const logoutBtn = document.getElementById("logout-btn");

async function boot() {
  try {
    await api.jobs();
    showShell();
    return;
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 401) {
      // Non-auth failure (e.g. a network hiccup): don't lock the user out on
      // a transient error. Individual views handle their own load failures.
      showShell();
      return;
    }
  }
  await showLoginOrBootstrap();
}

// enrollTokenFromURL reads a single-use enrollment token off an invite or
// recovery link (?token=...).
//
// The token is removed from the address bar as soon as it is read: it is a
// credential, and leaving it in the URL puts it in browser history, in the
// Referer header of anything the page later loads, and in any screenshot of
// the window.
function enrollTokenFromURL() {
  const t = new URLSearchParams(window.location.search).get("token");
  if (!t) return "";
  const clean = window.location.pathname + window.location.hash;
  window.history.replaceState(null, "", clean);
  return t;
}

// showLoginOrBootstrap decides which pre-login screen to show.
//
// An enrollment token in the link wins outright: whoever holds one is being
// invited to create their FIRST passkey, so there is nothing for them to log in
// with and offering a login button would be a dead end.
//
// Otherwise it probes login/begin. A 404 means no identity has ever been
// registered, which is now a state only an enrollment link can resolve — the
// API token used to and deliberately no longer does (rosterbot-crq.9).
async function showLoginOrBootstrap() {
  const token = enrollTokenFromURL();
  if (token) {
    showEnroll(token);
    return;
  }
  try {
    await api.authLoginBegin();
    showLogin();
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      showBootstrap();
    } else {
      showLogin("Could not reach the login API.");
    }
  }
}

function showLogin(message) {
  loginScreen.hidden = false;
  bootstrapScreen.hidden = true;
  shell.hidden = true;
  loginError.textContent = message || "";
}

function showBootstrap(message) {
  loginScreen.hidden = true;
  bootstrapScreen.hidden = false;
  shell.hidden = true;
  bootstrapError.textContent = message || "";
}

// applyRole reveals the admin-only navigation.
//
// PRESENTATION ONLY. Authorization stays server-side in adminOnlyRoutes, so a
// member who un-hides this in their browser gets a tab that answers 403. Hiding
// it is about not offering people doors they cannot open, not about locking
// them.
//
// A failure leaves the tab hidden, which is the harmless direction: an admin
// sees one fewer link and can still reach #tenants by typing it.
async function applyRole() {
  const adminNav = document.querySelectorAll("[data-admin-only]");
  try {
    const me = await api.me();
    const isAdmin = me.role === "admin";
    for (const n of adminNav) n.hidden = !isAdmin;
    showConnectionBanner(me);
  } catch {
    for (const n of adminNav) n.hidden = true;
  }
}

// showConnectionBanner is the TENANT-facing half of crq.14's failure routing.
//
// The admin Tenants tab answers "who needs to act" for the operator. This
// answers it for the person who can actually act, and it has to be unmissable:
// a broken connection means the bot has silently stopped managing their team,
// and a user who only discovers that by visiting Settings will discover it
// late.
//
// It deliberately does NOT fire for bot_challenge. That is operator-actionable
// — the credentials are fine and nothing the user does helps — so telling them
// to reconnect would waste their time and mislead them, which is the specific
// failure the taxonomy exists to prevent.
function showConnectionBanner(me) {
  const host = document.getElementById("status-line");
  if (!host || !me.fantrax) return;
  const conn = me.fantrax;
  if (conn.status !== "needs_reconnect") return;
  if (conn.last_error === "bot_challenge") return;

  const b = document.createElement("div");
  b.className = "banner bad";
  b.append(document.createTextNode(
    "rosterbot is not connected to your Fantrax account, so it is not managing your team. "));
  const a = document.createElement("a");
  a.href = "#settings";
  a.textContent = "Reconnect in Settings";
  b.append(a);
  host.prepend(b);
}

function showShell() {
  loginScreen.hidden = true;
  bootstrapScreen.hidden = true;
  shell.hidden = false;
  void applyRole();
  window.addEventListener("hashchange", route);
  route();
  startLive();
}

passkeyLoginBtn.addEventListener("click", async () => {
  loginError.textContent = "";
  try {
    await loginWithPasskey();
    showShell();
  } catch (err) {
    loginError.textContent = "Passkey sign-in failed or was cancelled.";
  }
});

// showEnroll runs the ceremony for someone arriving on an invite link. It
// reuses the bootstrap screen's markup rather than adding a third pre-login
// screen; only the wording and what the button does differ.
function showEnroll(token) {
  loginScreen.hidden = true;
  shell.hidden = true;
  bootstrapScreen.hidden = false;
  bootstrapError.textContent = "";
  if (bootstrapPrompt) {
    bootstrapPrompt.textContent =
      "You have been invited to rosterbot. Create a passkey to finish setting up your account.";
  }
  if (bootstrapTokenField) bootstrapTokenField.hidden = true;
  bootstrapSubmit.textContent = "Create your passkey";
  bootstrapForm.onsubmit = async (e) => {
    e.preventDefault();
    bootstrapError.textContent = "";
    try {
      await registerPasskey(token);
      showShell();
    } catch (err) {
      // The link is single-use and time-limited, so "expired or already used"
      // is the likeliest cause by far and is worth naming — a bare "failed"
      // sends someone hunting for a browser problem they do not have.
      bootstrapError.textContent =
        "Could not create the passkey. The invite link may have expired or already been used — ask for a new one.";
    }
  };
}

bootstrapForm.addEventListener("submit", async (e) => {
  // The legacy API-token path. The server no longer accepts that token for
  // registration (rosterbot-crq.9), so this remains only to give an
  // intelligible answer to anyone who still has the old instructions.
  if (bootstrapForm.onsubmit) return;
  e.preventDefault();
  bootstrapError.textContent =
    "Setting up with an API token is no longer supported. Ask for an invite link instead.";
});

logoutBtn.addEventListener("click", async () => {
  try {
    await api.authLogout();
  } catch {
    // Logging out is best-effort client-side too — clearing local UI state
    // shouldn't hang on a failed network call.
  }
  stopLive(); // halt the run poller so it doesn't 401-loop after auth is gone
  window.location.hash = "";
  showLogin();
});

// The More panel exists only in the mobile bottom-bar layout; on desktop the
// button is display:none and this listener never fires.
const nav = document.getElementById("nav");
const navMore = document.getElementById("nav-more");
navMore.addEventListener("click", () => {
  const open = nav.classList.toggle("open");
  navMore.setAttribute("aria-expanded", String(open));
});

function route() {
  const hash = window.location.hash || DEFAULT_ROUTE;
  // Picking a destination closes the panel; leaving it open over the new view
  // would hide the content the tap was for.
  nav.classList.remove("open");
  navMore.setAttribute("aria-expanded", "false");
  // Only ever dispatch to an own-property of ROUTES: a URL fragment like
  // "#constructor"/"#__proto__" must not resolve to an inherited Object method
  // and get invoked as a view renderer (CodeQL js/unvalidated-dynamic-method-call).
  const render = Object.hasOwn(ROUTES, hash) ? ROUTES[hash] : ROUTES[DEFAULT_ROUTE];
  document.querySelectorAll("nav a").forEach((a) => {
    a.classList.toggle("active", a.getAttribute("href") === hash);
  });
  root.innerHTML = "";
  render(root);
}

boot();
