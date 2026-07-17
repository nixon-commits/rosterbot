// app.js — login gate + hash router + nav wiring. ROUTES maps a URL hash to a
// view's render(root) function; later tasks add entries here.
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
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
const shell = document.getElementById("shell");
const loginForm = document.getElementById("login-form");
const tokenInput = document.getElementById("token-input");
const loginError = document.getElementById("login-error");
const logoutBtn = document.getElementById("logout-btn");

async function boot() {
  if (!isLoggedIn()) {
    showLogin();
    return;
  }
  // Verify the stored token still works before showing the shell — GET
  // /v1/jobs is always 200 for any valid token (it never touches ECS).
  try {
    await api.jobs();
    showShell();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      showLogin("Saved token was rejected — please log in again.");
    } else {
      // Non-auth failure (e.g. a network hiccup): don't lock the user out on
      // a transient error. Individual views handle their own load failures.
      showShell();
    }
  }
}

function showLogin(message) {
  loginScreen.hidden = false;
  shell.hidden = true;
  loginError.textContent = message || "";
}

function showShell() {
  loginScreen.hidden = true;
  shell.hidden = false;
  window.addEventListener("hashchange", route);
  route();
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const token = tokenInput.value.trim();
  if (!token) return;
  setToken(token);
  try {
    await api.jobs();
    tokenInput.value = "";
    showShell();
  } catch (err) {
    clearToken();
    loginError.textContent = "That token was rejected.";
  }
});

logoutBtn.addEventListener("click", () => {
  clearToken();
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
