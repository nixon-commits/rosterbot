// app.js — login/bootstrap gate + hash router + nav wiring. ROUTES maps a URL
// hash to a view's render(root) function; later tasks add entries here.
import { api, ApiError } from "./api.js";
import { registerPasskey, loginWithPasskey } from "./webauthn.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";
import { renderRuns } from "./runs.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
};
const DEFAULT_ROUTE = "#lineup";

const root = document.getElementById("view-root");
const loginScreen = document.getElementById("login-screen");
const bootstrapScreen = document.getElementById("bootstrap-screen");
const shell = document.getElementById("shell");
const passkeyLoginBtn = document.getElementById("passkey-login-btn");
const loginError = document.getElementById("login-error");
const bootstrapForm = document.getElementById("bootstrap-form");
const bootstrapTokenInput = document.getElementById("bootstrap-token-input");
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

// showLoginOrBootstrap decides which pre-login screen to show by probing
// login/begin: a 404 means no identity has ever been registered (first run,
// or every passkey was revoked/lost), so the token-bootstrap screen is the
// only way forward; any other outcome means a real login attempt is possible.
async function showLoginOrBootstrap() {
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

function showShell() {
  loginScreen.hidden = true;
  bootstrapScreen.hidden = true;
  shell.hidden = false;
  window.addEventListener("hashchange", route);
  route();
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

bootstrapForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const token = bootstrapTokenInput.value.trim();
  if (!token) return;
  bootstrapError.textContent = "";
  try {
    await registerPasskey(token);
    bootstrapTokenInput.value = "";
    showShell();
  } catch (err) {
    bootstrapError.textContent = "Setup failed — check the token and try again.";
  }
});

logoutBtn.addEventListener("click", async () => {
  try {
    await api.authLogout();
  } catch {
    // Logging out is best-effort client-side too — clearing local UI state
    // shouldn't hang on a failed network call.
  }
  window.location.hash = "";
  showLogin();
});

function route() {
  const hash = window.location.hash || DEFAULT_ROUTE;
  const render = ROUTES[hash] || ROUTES[DEFAULT_ROUTE];
  document.querySelectorAll("nav a").forEach((a) => {
    a.classList.toggle("active", a.getAttribute("href") === hash);
  });
  root.innerHTML = "";
  render(root);
}

boot();
