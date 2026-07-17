# Web Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a private, single-user web dashboard — hosted on the existing AWS infra — that shows today's lineup, triggers any of the 9 allowlisted background jobs, browses run history and per-run output, and links out to the existing recap/report sites.

**Architecture:** A no-build static site (`web/dashboard/`) served from a new S3 bucket behind a new CloudFront distribution (`DashboardCdn`). That same distribution path-routes `/v1/*` straight to the existing Lambda Function URL (`LineupApi`) as a second origin, so the browser sees one same-origin app with zero CORS handling. Auth is a login screen that stores the existing `ROSTERBOT_API_TOKEN` in `localStorage` and sends it as `Authorization: Bearer` on every API call — no new auth infra. `rosterbot serve` gains a `--web` flag that reproduces the exact same same-origin split locally (static files at `/`, the real API handler at `/v1/*`), so every view is testable against a real local server before anything touches AWS. The dashboard ships through the existing CodeBuild pipeline: a new buildspec step syncs `web/dashboard/` to the new bucket and invalidates the new distribution on every push to `main`.

**Tech Stack:** Vanilla HTML/CSS/JS (native ES modules via `<script type="module">`, no bundler, no npm/node), Go 1.25+ (`cmd/serve.go`, `infra/infra.go`), AWS CDK v2 Go bindings (`aws-cdk-go v2.260.0`, already vendored), existing `internal/lineupapi` package (unmodified — this plan is a pure consumer of its existing contract).

## Global Constraints

- No new build tooling: no npm/node/webpack/vite anywhere in the repo. Static files only.
- No CORS changes to `internal/lineupapi` or the Lambda: same-origin via CloudFront path routing is the only supported topology.
- No new auth infra (no Cognito, no IP allowlist): reuse the existing `ROSTERBOT_API_TOKEN` SSM parameter as the login gate.
- No custom domain: the dashboard gets a default `*.cloudfront.net` URL, matching `SiteCdn`/`ReportCdn`.
- Don't touch the existing `SiteBucket`/`SiteCdn` (recap) or `ReportBucket`/`ReportCdn` (projection/value dashboard) resources or their sync path (`cmd/sync.go`) — the new dashboard is fully additive.
- All 9 allowlisted jobs are exposed; which ones prompt for confirmation is driven entirely by the `mutating` field `GET /v1/jobs` already returns — never hardcode a job-risk list client-side.
- `cdk deploy` and the first live smoke test against AWS are **manual, user-run steps** at the end of this plan, not something an agent executes unattended — deploying new IAM/S3/CloudFront resources is a shared-infra change per this repo's own safety conventions.

---

## File Structure

```
cmd/
  serve.go            # MODIFY: add --web flag + newServeMux() (same-origin local dev)
  serve_test.go        # CREATE: routing tests for newServeMux()
web/dashboard/
  index.html          # CREATE: page shell, login form, nav, view-root container
  style.css           # CREATE: layout, light/dark theme, badges, tables, forms
  api.js              # CREATE: token storage + fetch wrapper (Authorization header, ApiError)
  render.js            # CREATE: escapeHtml + generic JSON->DOM renderer (table/kv/list)
  app.js               # CREATE: login gate, hash router, nav wiring
  lineup.js            # CREATE: GET /v1/lineup/today view (bespoke slot grid)
  jobs.js              # CREATE: GET /v1/jobs + POST /v1/jobs/{name} view (dynamic forms)
  runs.js              # CREATE: GET /v1/runs (+ polling), /v1/runs/{id}, /v1/runs/{id}/output, /v1/notifications
infra/
  infra.go             # MODIFY: DashboardBucket, DashboardCdn, CodeBuild env/grants
buildspec.yml          # MODIFY: sync web/dashboard/ to S3 + invalidate on every build
README.md               # MODIFY: document the dashboard + local dev flow
docs/aws-deployment.md  # MODIFY: document DashboardBucket/DashboardCdn in the architecture list
```

---

### Task 1: Local same-origin dev server (`rosterbot serve --web`)

**Files:**
- Modify: `cmd/serve.go`
- Create: `cmd/serve_test.go`

**Interfaces:**
- Produces: `newServeMux(token, lineupDir, webDir string) http.Handler` — routes `/v1/*` to the existing `lineupapi.Handler`, everything else to `http.FileServer(http.Dir(webDir))` when `webDir` exists, 404 otherwise. Later tasks' manual verification steps rely on this to serve `web/dashboard/` locally.

This gives every later frontend task a real, same-origin local server to test against — mirroring exactly what CloudFront will do in production (default behavior = static files, `/v1/*` behavior = the API), so there's no local/prod behavioral gap and no CORS code is ever needed.

- [ ] **Step 1: Write the failing test**

Create `cmd/serve_test.go`:

```go
package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeMux_RoutesAPIAndStatic(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<h1>dashboard</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := newServeMux("test-token", t.TempDir(), webDir)

	// Static file at "/" needs no auth — CloudFront's default behavior doesn't
	// touch the Lambda either.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dashboard") {
		t.Fatalf("GET / body = %q, want it to contain the static file's content", rec.Body.String())
	}

	// /v1/* requires the bearer token, exactly like the deployed Lambda.
	req = httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/jobs (no auth) = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/jobs (authed) = %d, want 200", rec.Code)
	}
}

func TestServeMux_NoWebDirConfigured(t *testing.T) {
	mux := newServeMux("test-token", t.TempDir(), "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / with no web dir = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestServeMux -v`
Expected: FAIL — `undefined: newServeMux` (compile error, function doesn't exist yet).

- [ ] **Step 3: Implement `newServeMux` and wire it into `runServe`**

Replace the full contents of `cmd/serve.go`:

```go
package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/spf13/cobra"
)

var (
	serveAddr   string
	serveDir    string
	serveWebDir string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the read-only lineup HTTP API locally (GET /v1/lineup/today)",
	Long: `Serve the read-only lineup API over HTTP for local testing before deploy.

It reads the precomputed JSON written by ` + "`optimize --publish-lineup`" + ` (the
same bytes the deployed Lambda serves) — it does NOT run the optimizer or touch
Fantrax. Requires ROSTERBOT_API_TOKEN; requests need an "Authorization: Bearer
<token>" header.

It also serves the dashboard's static files (web/dashboard by default) at "/",
same-origin with the API — the same split CloudFront does in production between
its default behavior (static files) and its "/v1/*" behavior (the Lambda API),
so the dashboard's relative "/v1/..." fetches behave identically in both places.

Typical local flow:
  go run . optimize --dry-run --publish-lineup   # writes .lineup/lineup-today.json
  ROSTERBOT_API_TOKEN=test go run . serve
  open http://localhost:8080/                    # dashboard, same-origin API calls
  curl -H "Authorization: Bearer test" localhost:8080/v1/lineup/today`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080", "address to listen on")
	serveCmd.Flags().StringVar(&serveDir, "dir", ".lineup", "directory holding published lineup JSON")
	serveCmd.Flags().StringVar(&serveWebDir, "web", "web/dashboard", "directory holding the dashboard's static files, served at / (empty to disable)")
	rootCmd.AddCommand(serveCmd)
}

// newServeMux builds the local dev router: "/v1/*" goes to the lineup API
// (bearer-token authenticated, the same handler the deployed Lambda uses), and
// everything else is served as static files from webDir — mirroring the
// CloudFront default-behavior/"/v1/*"-behavior split used in production. An
// empty or missing webDir disables static serving (unmatched paths 404),
// which keeps the pre-dashboard `serve` workflow (curl-only lineup testing)
// working unchanged.
func newServeMux(token, lineupDir, webDir string) http.Handler {
	apiHandler := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineupapi.NewFileStore(lineupDir),
		Runs:          lineupapi.NewFileRunStore(lineupDir + "/runs"),
		Notifications: lineupapi.NewFileNotificationStore(lineupDir + "/notifications"),
		Output:        lineupapi.NewFileOutputStore(lineupDir + "/output"),
		// Jobs is nil locally: triggering real ECS tasks only makes sense on AWS.
		// POST /v1/jobs/* returns 501 from `serve`.
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	if webDir != "" {
		if _, err := os.Stat(webDir); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(webDir)))
		}
	}
	return mux
}

func runServe(cmd *cobra.Command, args []string) error {
	token := os.Getenv("ROSTERBOT_API_TOKEN")
	if token == "" {
		return fmt.Errorf("ROSTERBOT_API_TOKEN is not set — the server needs a bearer token to authenticate requests")
	}
	if _, err := os.Stat(serveWebDir); err != nil {
		fmt.Printf("serving lineup API on %s (reading %s; jobs disabled locally; dashboard not served: %s not found)\n", serveAddr, serveDir, serveWebDir)
	} else {
		fmt.Printf("serving lineup API + dashboard on %s (reading %s; jobs disabled locally)\n", serveAddr, serveDir)
	}
	return http.ListenAndServe(serveAddr, newServeMux(token, serveDir, serveWebDir))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -run TestServeMux -v`
Expected: PASS (both `TestServeMux_RoutesAPIAndStatic` and `TestServeMux_NoWebDirConfigured`).

- [ ] **Step 5: Run `go vet` and the full cmd test suite**

Run: `go vet ./cmd/... && go test ./cmd/...`
Expected: no vet warnings, all tests pass (existing `cmd` tests untouched).

- [ ] **Step 6: Commit**

```bash
git add cmd/serve.go cmd/serve_test.go
git commit -m "feat(serve): add --web flag for same-origin local dashboard testing"
```

---

### Task 2: Dashboard shell — login gate, nav, Lineup view

**Files:**
- Create: `web/dashboard/index.html`
- Create: `web/dashboard/style.css`
- Create: `web/dashboard/api.js`
- Create: `web/dashboard/render.js`
- Create: `web/dashboard/app.js`
- Create: `web/dashboard/lineup.js`

**Interfaces:**
- Consumes: `newServeMux` from Task 1 (via `rosterbot serve --web web/dashboard`).
- Produces (for Tasks 3–4 to import): `api.js` exports `{ getToken, setToken, clearToken, isLoggedIn, ApiError, api }` where `api = { lineupToday, runs, runDetail, runOutput, notifications, jobs, triggerJob }`. `render.js` exports `{ escapeHtml, renderAuto }`. `app.js`'s `ROUTES` map and the `#view-root`/`nav` DOM structure in `index.html` are the extension points later tasks hook into.

- [ ] **Step 1: Create `web/dashboard/api.js`**

```js
// api.js — thin fetch wrapper around the rosterbot control API. Every request
// is a same-origin relative path (CloudFront path-routes /v1/* to the Lambda
// in production; `rosterbot serve --web` does the same locally), so no CORS
// handling is needed anywhere in this app.
const TOKEN_KEY = "rosterbot_token";

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(token) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

export function isLoggedIn() {
  return getToken() !== "";
}

// ApiError carries the HTTP status so callers can special-case 401 (bad
// token), 404 (nothing published yet), 409 (job already running), and 501
// (job triggering unavailable locally) without parsing message strings.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function request(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: {
      "Authorization": "Bearer " + getToken(),
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    clearToken();
    throw new ApiError(401, "unauthorized");
  }
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
  if (res.status === 204) return null;
  return res.json();
}

export const api = {
  lineupToday: () => request("GET", "/v1/lineup/today"),
  runs: (limit = 25) => request("GET", `/v1/runs?limit=${limit}`),
  runDetail: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}`),
  runOutput: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}/output`),
  notifications: (limit = 25) => request("GET", `/v1/notifications?limit=${limit}`),
  jobs: () => request("GET", "/v1/jobs"),
  triggerJob: (name, params) => request("POST", `/v1/jobs/${encodeURIComponent(name)}`, { params }),
};
```

- [ ] **Step 2: Create `web/dashboard/render.js`**

```js
// render.js — shared DOM helpers: HTML-escaping and a generic JSON->DOM
// renderer used for the run-output viewer (arrays of objects -> table, plain
// objects -> key/value rows, everything else -> a simple list/text node).
export function escapeHtml(value) {
  const div = document.createElement("div");
  div.textContent = String(value);
  return div.innerHTML;
}

export function renderAuto(data) {
  if (data === null || data === undefined) return textNode("(empty)");
  if (Array.isArray(data)) {
    if (data.length === 0) return textNode("(empty list)");
    if (typeof data[0] === "object" && data[0] !== null) return renderTable(data);
    return renderList(data);
  }
  if (typeof data === "object") return renderKeyValue(data);
  return textNode(String(data));
}

function textNode(text) {
  const p = document.createElement("p");
  p.className = "muted";
  p.textContent = text;
  return p;
}

function renderList(items) {
  const ul = document.createElement("ul");
  for (const item of items) {
    const li = document.createElement("li");
    li.textContent = String(item);
    ul.appendChild(li);
  }
  return ul;
}

function renderKeyValue(obj) {
  const table = document.createElement("table");
  table.className = "kv-table";
  for (const [key, value] of Object.entries(obj)) {
    const row = document.createElement("tr");
    const th = document.createElement("th");
    th.textContent = key;
    const td = document.createElement("td");
    if (value !== null && typeof value === "object") {
      td.appendChild(renderAuto(value));
    } else {
      td.textContent = value === null || value === undefined ? "" : String(value);
    }
    row.append(th, td);
    table.appendChild(row);
  }
  return table;
}

function renderTable(rows) {
  // Union of keys across all rows, in first-seen order, so a row missing an
  // optional field doesn't shift columns.
  const cols = [];
  const seen = new Set();
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        cols.push(key);
      }
    }
  }

  const table = document.createElement("table");
  table.className = "data-table";

  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const col of cols) {
    const th = document.createElement("th");
    th.textContent = col;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const row of rows) {
    const tr = document.createElement("tr");
    for (const col of cols) {
      const td = document.createElement("td");
      const value = row[col];
      if (value !== null && typeof value === "object") {
        td.appendChild(renderAuto(value));
      } else {
        td.textContent = value === null || value === undefined ? "" : String(value);
      }
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);

  return table;
}
```

- [ ] **Step 3: Create `web/dashboard/lineup.js`**

```js
// lineup.js — the home view: today's optimized lineup (GET /v1/lineup/today).
import { api, ApiError } from "./api.js";
import { escapeHtml } from "./render.js";

export async function renderLineup(root) {
  root.innerHTML = "<p class=\"muted\">Loading today's lineup…</p>";
  let data;
  try {
    data = await api.lineupToday();
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";

  const header = document.createElement("div");
  header.className = "card";
  header.innerHTML = `
    <h2>${escapeHtml(data.date)}</h2>
    <p>Projected: <strong>${data.projected_points.toFixed(1)}</strong> pts</p>
  `;
  root.appendChild(header);

  if (data.warnings && data.warnings.length > 0) {
    const warn = document.createElement("div");
    warn.className = "card";
    warn.innerHTML = "<strong>Warnings</strong>";
    const ul = document.createElement("ul");
    for (const w of data.warnings) {
      const li = document.createElement("li");
      li.textContent = w;
      ul.appendChild(li);
    }
    warn.appendChild(ul);
    root.appendChild(warn);
  }

  const grid = document.createElement("div");
  grid.className = "slot-grid";
  for (const slot of data.slots) {
    grid.appendChild(slotCard(slot));
  }
  root.appendChild(grid);
}

function slotCard(slot) {
  const card = document.createElement("div");
  card.className = "card";
  if (!slot.player) {
    card.innerHTML = `<div class="muted">${escapeHtml(slot.slot)}</div><div class="muted">— empty —</div>`;
    return card;
  }
  const p = slot.player;
  card.innerHTML = `
    <div class="muted">${escapeHtml(slot.slot)}</div>
    <div><strong>${escapeHtml(p.name)}</strong> <span class="badge badge-${p.status.toLowerCase()}">${escapeHtml(p.status)}</span></div>
    <div class="muted">${escapeHtml(p.team)} · ${escapeHtml(p.pos.join("/"))}</div>
    <div>${p.proj.toFixed(1)} proj pts</div>
  `;
  return card;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  if (err instanceof ApiError && err.status === 404) {
    card.innerHTML = "<p class=\"muted\">No lineup published yet — the hourly optimize run hasn't run today.</p>";
  } else {
    card.innerHTML = `<p class="error">Failed to load lineup: ${escapeHtml(err.message)}</p>`;
  }
  return card;
}
```

- [ ] **Step 4: Create `web/dashboard/app.js`**

```js
// app.js — login gate + hash router + nav wiring. ROUTES maps a URL hash to a
// view's render(root) function; later tasks add entries here.
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
import { renderLineup } from "./lineup.js";

const ROUTES = {
  "#lineup": renderLineup,
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
```

- [ ] **Step 5: Create `web/dashboard/index.html`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rosterbot</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<div id="login-screen">
  <form id="login-form">
    <h1>rosterbot</h1>
    <label for="token-input">API token</label>
    <input id="token-input" type="password" autocomplete="off" placeholder="Paste ROSTERBOT_API_TOKEN">
    <button type="submit">Log in</button>
    <p id="login-error" class="error"></p>
  </form>
</div>

<div id="shell" hidden>
  <header>
    <h1>rosterbot</h1>
    <nav>
      <a href="#lineup">Lineup</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
      <a href="https://d3lfzksum77fj7.cloudfront.net" target="_blank" rel="noopener">Projections ↗</a>
    </nav>
    <button id="logout-btn" type="button">Log out</button>
  </header>
  <main id="view-root"></main>
</div>

<script type="module" src="app.js"></script>
</body>
</html>
```

> The Recap/Projections URLs above are the current `SiteUrl`/`ReportUrl` CDK stack outputs. Before Step 7's verification, confirm they're still current: `aws cloudformation describe-stacks --stack-name InfraStack --region us-west-1 --query "Stacks[0].Outputs" --output table` (or `cd infra && cdk deploy --require-approval never` prints them). Update the two `href`s in this file if either has changed.

- [ ] **Step 6: Create `web/dashboard/style.css`**

```css
:root {
  color-scheme: light dark;
  --bg: #ffffff;
  --fg: #1a1a1a;
  --muted: #6b7280;
  --border: #e5e7eb;
  --accent: #2563eb;
  --danger: #dc2626;
  --success: #16a34a;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #14161a;
    --fg: #e5e7eb;
    --muted: #9ca3af;
    --border: #2a2d33;
    --accent: #60a5fa;
    --danger: #f87171;
    --success: #4ade80;
  }
}

* { box-sizing: border-box; }

body {
  margin: 0;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  background: var(--bg);
  color: var(--fg);
}

#login-screen {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 1rem;
}
#login-screen[hidden] { display: none; }

#login-form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  width: 100%;
  max-width: 320px;
}

#login-form input {
  padding: 0.5rem;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--fg);
}

#login-form button {
  padding: 0.5rem;
  border: none;
  border-radius: 6px;
  background: var(--accent);
  color: white;
  cursor: pointer;
}

.error { color: var(--danger); min-height: 1.2em; }

#shell[hidden] { display: none; }

header {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}

header h1 { font-size: 1.1rem; margin: 0; }

nav { display: flex; gap: 0.75rem; flex-wrap: wrap; }

nav a {
  color: var(--muted);
  text-decoration: none;
  padding: 0.25rem 0.5rem;
  border-radius: 6px;
}

nav a.active, nav a:hover { color: var(--fg); background: var(--border); }

#logout-btn {
  margin-left: auto;
  padding: 0.25rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: transparent;
  color: var(--fg);
  cursor: pointer;
}

main { padding: 1rem; max-width: 960px; margin: 0 auto; }

table { border-collapse: collapse; width: 100%; margin: 0.5rem 0; }
th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border); }
.kv-table th { color: var(--muted); width: 12rem; }

.muted { color: var(--muted); }

.card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 0.75rem 1rem;
  margin-bottom: 0.75rem;
}

.badge {
  display: inline-block;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
}
.badge-ok, .badge-success { background: color-mix(in srgb, var(--success) 20%, transparent); color: var(--success); }
.badge-locked, .badge-running { background: color-mix(in srgb, var(--accent) 20%, transparent); color: var(--accent); }
.badge-benched, .badge-failed, .badge-failure { background: color-mix(in srgb, var(--danger) 20%, transparent); color: var(--danger); }
.badge-info { background: color-mix(in srgb, var(--muted) 20%, transparent); color: var(--muted); }

.slot-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 0.75rem;
}

form.job-form { display: flex; flex-direction: column; gap: 0.6rem; max-width: 420px; }
form.job-form > div { display: flex; flex-direction: column; gap: 0.2rem; }
form.job-form label { font-size: 0.85rem; color: var(--muted); }
form.job-form input, form.job-form select { padding: 0.4rem; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--fg); }

button.primary {
  padding: 0.5rem 1rem;
  border: none;
  border-radius: 6px;
  background: var(--accent);
  color: white;
  cursor: pointer;
  align-self: flex-start;
}
button.primary:disabled { opacity: 0.5; cursor: not-allowed; }

pre {
  white-space: pre-wrap;
  word-break: break-word;
  background: color-mix(in srgb, var(--border) 40%, transparent);
  padding: 0.5rem;
  border-radius: 6px;
  max-height: 300px;
  overflow-y: auto;
}

@media (max-width: 600px) {
  header { flex-direction: column; align-items: flex-start; }
  #logout-btn { margin-left: 0; }
}
```

- [ ] **Step 7: Manual verification against a real local server**

```bash
go run . optimize --dry-run --publish-lineup
ROSTERBOT_API_TOKEN=test go run . serve
```

In a browser, open `http://localhost:8080/`. Expected:
- A login screen appears (not the shell).
- Entering any non-empty text and clicking "Log in" fails with "That token was rejected" (the real token is `test`, not arbitrary text) — confirms the 401 path.
- Entering `test` logs in successfully and shows today's lineup: date, projected points, a grid of slot cards with player name/team/pos/status/proj, and any warnings.
- Reloading the page stays logged in (token persisted in `localStorage`).
- Clicking "Log out" returns to the login screen and clears the token (confirm via browser devtools `localStorage.getItem("rosterbot_token")` is null).

- [ ] **Step 8: Commit**

```bash
git add web/dashboard/
git commit -m "feat(dashboard): add static shell, login gate, and today's lineup view"
```

---

### Task 3: Jobs trigger panel

**Files:**
- Create: `web/dashboard/jobs.js`
- Modify: `web/dashboard/app.js`
- Modify: `web/dashboard/index.html`

**Interfaces:**
- Consumes: `api.jobs()`, `api.triggerJob(name, params)`, `ApiError` from `api.js`; `escapeHtml` from `render.js` (Task 2).
- Produces: `renderJobs(root)` — a Task 4 sibling, no downstream dependents.

- [ ] **Step 1: Create `web/dashboard/jobs.js`**

```js
// jobs.js — renders one card per allowlisted job from GET /v1/jobs, with a
// form built from each job's declared Param schema. Confirmation before
// firing is driven entirely by the job's `mutating` flag from the API — never
// hardcode which jobs are "risky" here.
import { api, ApiError } from "./api.js";
import { escapeHtml } from "./render.js";

export async function renderJobs(root) {
  root.innerHTML = "<p class=\"muted\">Loading jobs…</p>";
  let jobs;
  try {
    const resp = await api.jobs();
    jobs = resp.jobs;
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";
  for (const job of jobs) {
    root.appendChild(jobCard(job));
  }
}

function jobCard(job) {
  const card = document.createElement("div");
  card.className = "card";

  const title = document.createElement("h3");
  title.textContent = job.label + (job.mutating ? " ⚠" : "");
  card.appendChild(title);

  const desc = document.createElement("p");
  desc.className = "muted";
  desc.textContent = job.description;
  card.appendChild(desc);

  const form = document.createElement("form");
  form.className = "job-form";
  const inputs = {};

  for (const param of job.params) {
    const wrapper = document.createElement("div");
    const id = `${job.name}-${param.name}`;

    const label = document.createElement("label");
    label.textContent = param.help ? `${param.label} — ${param.help}` : param.label;
    label.htmlFor = id;

    let input;
    if (param.type === "bool") {
      input = document.createElement("input");
      input.type = "checkbox";
      input.checked = param.default === "true";
    } else if (param.type === "enum") {
      input = document.createElement("select");
      // A native <select> always has *some* value selected — unlike a text
      // input, it can't be "empty" — so a param with no declared default
      // (e.g. optimize's "Projection system") needs an explicit blank option.
      // Without it the form would always send the first listed option,
      // silently overriding the job's own default every time it runs.
      const blank = document.createElement("option");
      blank.value = "";
      blank.textContent = "(default)";
      input.appendChild(blank);
      for (const opt of param.options || []) {
        const o = document.createElement("option");
        o.value = opt;
        o.textContent = opt;
        if (opt === param.default) o.selected = true;
        input.appendChild(o);
      }
    } else if (param.type === "int") {
      input = document.createElement("input");
      input.type = "number";
      if (param.min != null) input.min = String(param.min);
      if (param.max != null) input.max = String(param.max);
      if (param.default) input.value = param.default;
    } else {
      input = document.createElement("input");
      input.type = "text";
      if (param.default) input.value = param.default;
      if (param.pattern) input.pattern = param.pattern;
    }
    input.id = id;
    input.name = param.name;
    inputs[param.name] = input;

    wrapper.append(label, input);
    form.appendChild(wrapper);
  }

  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "primary";
  submit.textContent = job.mutating ? "Run (real changes)" : "Run";
  form.appendChild(submit);

  const status = document.createElement("p");
  status.className = "muted";
  form.appendChild(status);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (job.mutating && !window.confirm(`${job.label} will make real changes (Fantrax roster and/or a push notification). Continue?`)) {
      return;
    }
    const params = {};
    for (const [name, input] of Object.entries(inputs)) {
      params[name] = input.type === "checkbox" ? String(input.checked) : input.value;
    }
    submit.disabled = true;
    status.textContent = "Starting…";
    status.classList.remove("error");
    try {
      const resp = await api.triggerJob(job.name, params);
      status.textContent = `Started: ${resp.command} (run ${resp.id})`;
    } catch (err) {
      status.textContent = errorMessage(err);
      status.classList.add("error");
    } finally {
      submit.disabled = false;
    }
  });

  card.appendChild(form);
  return card;
}

function errorMessage(err) {
  if (err instanceof ApiError && err.status === 409) return "Already running — check the Runs tab.";
  if (err instanceof ApiError && err.status === 501) return "Job triggering isn't available on this server (local dev has no ECS).";
  return "Failed: " + err.message;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  card.innerHTML = `<p class="error">Failed to load jobs: ${escapeHtml(err.message)}</p>`;
  return card;
}
```

- [ ] **Step 2: Wire the route into `web/dashboard/app.js`**

In `web/dashboard/app.js`, change:

```js
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
import { renderLineup } from "./lineup.js";

const ROUTES = {
  "#lineup": renderLineup,
};
```

to:

```js
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
};
```

- [ ] **Step 3: Add the nav link in `web/dashboard/index.html`**

Change:

```html
    <nav>
      <a href="#lineup">Lineup</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
```

to:

```html
    <nav>
      <a href="#lineup">Lineup</a>
      <a href="#jobs">Jobs</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
```

- [ ] **Step 4: Manual verification**

```bash
ROSTERBOT_API_TOKEN=test go run . serve
```

Open `http://localhost:8080/`, log in, click "Jobs". Expected:
- 9 cards render (one per allowlisted job), each with its description and any declared form fields (`optimize` has Period/Custom date/Projection system/Dry run; `waivers` has How many/Positions/Dry run; etc.).
- `optimize`, `waivers`, `claims`, `gs-check`, `transactions` show a ⚠ in the title and "Run (real changes)" on the button; `backtest`, `prospects`, `grade`, `recap-site` don't.
- Clicking "Run" on a mutating job (e.g. `gs-check`) shows a native confirm dialog first; cancelling does nothing.
- Confirming (or submitting a non-mutating job) shows "Failed: Job triggering isn't available on this server (local dev has no ECS)." — expected locally (`Jobs` is `nil` in `serve`), confirms the 501 path is handled gracefully. Full success-path verification happens after AWS deploy (final section of this plan).

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/jobs.js web/dashboard/app.js web/dashboard/index.html
git commit -m "feat(dashboard): add jobs trigger panel with mutating-flag confirmation"
```

---

### Task 4: Run history, output viewer, activity feed

**Files:**
- Create: `web/dashboard/runs.js`
- Modify: `web/dashboard/app.js`
- Modify: `web/dashboard/index.html`

**Interfaces:**
- Consumes: `api.runs()`, `api.runDetail(id)`, `api.runOutput(id)`, `api.notifications()`, `ApiError` from `api.js`; `renderAuto`, `escapeHtml` from `render.js` (Task 2).
- Produces: `renderRuns(root)`, no downstream dependents.

- [ ] **Step 1: Create `web/dashboard/runs.js`**

```js
// runs.js — run history (polls while anything is RUNNING), a per-run detail +
// output viewer, and the notifications activity feed.
import { api, ApiError } from "./api.js";
import { renderAuto, escapeHtml } from "./render.js";

const POLL_MS = 5000;
let pollTimer = null;

export async function renderRuns(root) {
  stopPolling();
  root.innerHTML = "";

  const runsSection = document.createElement("section");
  runsSection.innerHTML = "<h2>Recent runs</h2>";
  const runsList = document.createElement("div");
  runsSection.appendChild(runsList);
  root.appendChild(runsSection);

  const detailSection = document.createElement("section");
  detailSection.id = "run-detail";
  root.appendChild(detailSection);

  const notifSection = document.createElement("section");
  notifSection.innerHTML = "<h2>Activity</h2>";
  const notifList = document.createElement("div");
  notifSection.appendChild(notifList);
  root.appendChild(notifSection);

  await Promise.all([
    loadRuns(runsList, detailSection),
    loadNotifications(notifList),
  ]);

  pollTimer = setInterval(() => loadRuns(runsList, detailSection, /* silent */ true), POLL_MS);
  // Stop polling once the user navigates away from this view.
  window.addEventListener("hashchange", stopPolling, { once: true });
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

async function loadRuns(container, detailSection, silent) {
  let runs;
  try {
    const resp = await api.runs();
    runs = resp.runs;
  } catch (err) {
    if (!silent) container.innerHTML = `<p class="error">Failed to load runs: ${escapeHtml(err.message)}</p>`;
    return;
  }
  container.innerHTML = "";
  if (runs.length === 0) {
    container.innerHTML = "<p class=\"muted\">No runs yet.</p>";
    return;
  }
  const table = document.createElement("table");
  table.className = "data-table";
  table.innerHTML = "<thead><tr><th>Command</th><th>Status</th><th>Started</th><th>Trigger</th></tr></thead>";
  const tbody = document.createElement("tbody");
  for (const run of runs) {
    const tr = document.createElement("tr");
    tr.style.cursor = "pointer";
    tr.innerHTML = `
      <td>${escapeHtml(run.command)}</td>
      <td><span class="badge badge-${run.status.toLowerCase()}">${escapeHtml(run.status)}</span></td>
      <td>${escapeHtml(run.started_at)}</td>
      <td>${escapeHtml(run.trigger)}</td>
    `;
    tr.addEventListener("click", () => showDetail(detailSection, run.id));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  container.appendChild(table);
}

async function showDetail(section, id) {
  section.innerHTML = "<p class=\"muted\">Loading…</p>";
  let detail;
  try {
    detail = await api.runDetail(id);
  } catch (err) {
    section.innerHTML = `<p class="error">Failed to load run: ${escapeHtml(err.message)}</p>`;
    return;
  }
  section.innerHTML = "";
  const card = document.createElement("div");
  card.className = "card";
  card.innerHTML = `
    <h3>${escapeHtml(detail.command)}</h3>
    <p>Status: <span class="badge badge-${detail.status.toLowerCase()}">${escapeHtml(detail.status)}</span> · Trigger: ${escapeHtml(detail.trigger)}</p>
    <p class="muted">Started ${escapeHtml(detail.started_at)}${detail.ended_at ? " · Ended " + escapeHtml(detail.ended_at) : ""}</p>
  `;
  if (detail.log_tail) {
    const pre = document.createElement("pre");
    pre.textContent = detail.log_tail;
    card.appendChild(pre);
  }
  section.appendChild(card);

  try {
    const output = await api.runOutput(id);
    const outCard = document.createElement("div");
    outCard.className = "card";
    outCard.innerHTML = `<h3>Output (${escapeHtml(output.type)})</h3>`;
    outCard.appendChild(renderAuto(output.data));
    section.appendChild(outCard);
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 404)) {
      const errCard = document.createElement("div");
      errCard.className = "card";
      errCard.innerHTML = `<p class="error">Failed to load output: ${escapeHtml(err.message)}</p>`;
      section.appendChild(errCard);
    }
    // 404 just means this run's job type doesn't record typed output
    // (optimize, recap-site) — not an error worth surfacing.
  }
}

async function loadNotifications(container) {
  let notifs;
  try {
    const resp = await api.notifications();
    notifs = resp.notifications;
  } catch (err) {
    container.innerHTML = `<p class="error">Failed to load activity: ${escapeHtml(err.message)}</p>`;
    return;
  }
  container.innerHTML = "";
  if (notifs.length === 0) {
    container.innerHTML = "<p class=\"muted\">No activity yet.</p>";
    return;
  }
  for (const n of notifs) {
    const card = document.createElement("div");
    card.className = "card";
    card.innerHTML = `
      <div><span class="badge badge-${n.status}">${escapeHtml(n.status)}</span> <span class="muted">${escapeHtml(n.kind)}</span> <strong>${escapeHtml(n.title)}</strong></div>
      <p>${escapeHtml(n.message)}</p>
      <p class="muted">${escapeHtml(n.created_at)}</p>
    `;
    container.appendChild(card);
  }
}
```

- [ ] **Step 2: Wire the route into `web/dashboard/app.js`**

Change:

```js
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
};
```

to:

```js
import { isLoggedIn, setToken, clearToken, api, ApiError } from "./api.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";
import { renderRuns } from "./runs.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
};
```

- [ ] **Step 3: Add the nav link in `web/dashboard/index.html`**

Change:

```html
      <a href="#jobs">Jobs</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
```

to:

```html
      <a href="#jobs">Jobs</a>
      <a href="#runs">Runs</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
```

- [ ] **Step 4: Manual verification, including a seeded output row**

```bash
ROSTERBOT_API_TOKEN=test go run . serve
```

Open `http://localhost:8080/`, log in, click "Runs". Expected with an empty `.lineup/` dir:
- "No runs yet." and "No activity yet." both render without errors.

Now seed one fake run + output to exercise the detail/output path end-to-end:

```bash
mkdir -p .lineup/runs .lineup/output
cat > .lineup/runs/run-0000000001-test123.json <<'EOF'
{"id":"test123","command":"waivers","status":"SUCCESS","started_at":"2026-07-16T12:00:00Z","ended_at":"2026-07-16T12:00:05Z","trigger":"manual","log_tail":"waivers: 3 picks found"}
EOF
cat > .lineup/output/test123.json <<'EOF'
{"type":"waivers","data":{"total":1,"picks":[{"name":"Test Player","team":"NYY","pos":"OF","is_pitcher":false,"projected_pts_per_game":4.2,"rank":1}]}}
EOF
```

Reload the Runs view. Expected:
- One row appears: command `waivers`, a green `SUCCESS` badge, the started timestamp, trigger `manual`.
- Clicking the row shows the detail card (command, status, trigger, started/ended, the `log_tail` in a `<pre>`) plus an "Output (waivers)" card rendering a table with columns `name, team, pos, is_pitcher, projected_pts_per_game, rank` and the one seeded row.
- Clean up: `rm -rf .lineup/runs .lineup/output`.

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/runs.js web/dashboard/app.js web/dashboard/index.html
git commit -m "feat(dashboard): add run history, output viewer, and activity feed"
```

---

### Task 5: CDK infra — dashboard bucket + CloudFront path routing

**Files:**
- Modify: `infra/infra.go`

**Interfaces:**
- Consumes: `apiFn.AddFunctionUrl(...)`'s return value (`apiURL`, already defined at `infra.go:247` as of this plan) as the origin for the `/v1/*` behavior.
- Produces: `dashboardBucket` (`awss3.Bucket`) and `dashboardDist` (`awscloudfront.Distribution`) — Task 6 references both by name for the CodeBuild env vars and IAM grants.

- [ ] **Step 1: Add the dashboard bucket**

In `infra/infra.go`, right after the `reportBucket` block (ends at line 73) and before the `logGroup` block, insert:

```go

	// Dashboard bucket (static web UI; private, served via its own CDN below).
	dashboardBucket := awss3.NewBucket(stack, jsii.String("DashboardBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
	})
```

Then add its name to the existing `CfnOutput` block (lines 81-84):

```go
	awscdk.NewCfnOutput(stack, jsii.String("RepoUri"), &awscdk.CfnOutputProps{Value: repo.RepositoryUri()})
	awscdk.NewCfnOutput(stack, jsii.String("StateBucketName"), &awscdk.CfnOutputProps{Value: stateBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("SiteBucketName"), &awscdk.CfnOutputProps{Value: siteBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("ReportBucketName"), &awscdk.CfnOutputProps{Value: reportBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("DashboardBucketName"), &awscdk.CfnOutputProps{Value: dashboardBucket.BucketName()})
```

- [ ] **Step 2: Add the dashboard distribution, path-routed to the Lambda API**

The `apiURL` variable is defined right before the `--- Phase 2: CodeBuild ---` comment. Insert the new distribution immediately after the existing `awscdk.NewCfnOutput(stack, jsii.String("LineupApiUrl"), ...)` line and before the `--- Phase 2: CodeBuild ---` comment:

```go
	awscdk.NewCfnOutput(stack, jsii.String("LineupApiUrl"), &awscdk.CfnOutputProps{Value: apiURL.Url()})

	// --- Dashboard: static UI + the same Lambda API, one distribution ---
	// "/v1/*" proxies straight to the Function URL so the browser sees a single
	// same-origin app — no CORS handling needed anywhere. CachePolicy is
	// disabled and OriginRequestPolicy forwards everything (including the
	// Authorization header) since every /v1/* response is a dynamic,
	// per-request authenticated call.
	dashboardDist := awscloudfront.NewDistribution(stack, jsii.String("DashboardCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(dashboardBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
		AdditionalBehaviors: &map[string]*awscloudfront.BehaviorOptions{
			"/v1/*": {
				Origin:               awscloudfrontorigins.NewFunctionUrlOrigin(apiURL, nil),
				ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				AllowedMethods:       awscloudfront.AllowedMethods_ALLOW_ALL(),
				CachePolicy:          awscloudfront.CachePolicy_CACHING_DISABLED(),
				OriginRequestPolicy:  awscloudfront.OriginRequestPolicy_ALL_VIEWER(),
			},
		},
	})
	awscdk.NewCfnOutput(stack, jsii.String("DashboardUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dashboardDist.DistributionDomainName()}),
	})

	// --- Phase 2: CodeBuild (build + push image to ECR on push to main) ---
```

(The final line above already exists in the file — it's shown only to make the insertion point unambiguous; don't duplicate it.)

- [ ] **Step 3: Verify the stack still synthesizes**

Run: `cd infra && go build ./... && cdk synth >/dev/null`
Expected: no compile errors, synth completes without throwing (CDK validates construct wiring, e.g. that `AdditionalBehaviors`' origin is valid, at synth time).

- [ ] **Step 4: Preview the diff against the deployed stack**

Run: `cd infra && cdk diff`
Expected output includes: `[+] AWS::S3::Bucket DashboardBucket`, `[+] AWS::CloudFront::Distribution DashboardCdn`, plus the OAC/bucket-policy resources CDK generates for `S3BucketOrigin_WithOriginAccessControl` (mirrors what `cdk diff` showed when `SiteCdn`/`ReportCdn` were first added). No changes to `SiteCdn`, `ReportCdn`, `LineupApi`, or any EventBridge rule.

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go
git commit -m "feat(infra): add DashboardBucket + DashboardCdn, path-routed to the Lambda API"
```

---

### Task 6: CodeBuild auto-deploy wiring

**Files:**
- Modify: `infra/infra.go`
- Modify: `buildspec.yml`

**Interfaces:**
- Consumes: `dashboardBucket`, `dashboardDist`, `cfArn` (existing helper, `infra.go:120-124`) from Task 5.

- [ ] **Step 1: Add `DASHBOARD_BUCKET`/`DASHBOARD_CF_DIST_ID` to the CodeBuild project's env vars**

Inside the `if v, ok := stack.Node().TryGetContext(jsii.String("enableBuild"))...` block, find the `EnvironmentVariables` map on the `Project`:

```go
			EnvironmentVariables: &map[string]*awscodebuild.BuildEnvironmentVariable{
				"ECR_URI": {Value: repo.RepositoryUri()},
				// Launch coordinates for the post-build projection-site render so a
				// push to main re-renders the dashboard immediately instead of
				// waiting for the daily ProjectionSite schedule. Reuses the same
				// egress-only SG + public subnets the API uses to launch tasks.
				"CLUSTER":         {Value: cluster.ClusterArn()},
				"TASK_DEF":        {Value: taskDef.TaskDefinitionArn()},
				"SUBNETS":         {Value: awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds)},
				"SECURITY_GROUPS": {Value: taskSg.SecurityGroupId()},
			},
```

Change it to:

```go
			EnvironmentVariables: &map[string]*awscodebuild.BuildEnvironmentVariable{
				"ECR_URI": {Value: repo.RepositoryUri()},
				// Launch coordinates for the post-build projection-site render so a
				// push to main re-renders the dashboard immediately instead of
				// waiting for the daily ProjectionSite schedule. Reuses the same
				// egress-only SG + public subnets the API uses to launch tasks.
				"CLUSTER":         {Value: cluster.ClusterArn()},
				"TASK_DEF":        {Value: taskDef.TaskDefinitionArn()},
				"SUBNETS":         {Value: awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds)},
				"SECURITY_GROUPS": {Value: taskSg.SecurityGroupId()},
				// Where the static dashboard build step (buildspec.yml) publishes.
				"DASHBOARD_BUCKET":     {Value: dashboardBucket.BucketName()},
				"DASHBOARD_CF_DIST_ID": {Value: dashboardDist.DistributionId()},
			},
```

- [ ] **Step 2: Grant the build role write access to the dashboard bucket + invalidation rights**

Right after the existing `taskDef.GrantRun(project)` line, add:

```go
		repo.GrantPullPush(project)
		// Let the build launch the projection-site task (ecs:RunTask + the
		// iam:PassRole on the task's execution/task roles that RunTask requires).
		taskDef.GrantRun(project)
		// Let the build publish the static dashboard: write its bucket, then
		// invalidate its distribution so the new build is served immediately.
		dashboardBucket.GrantReadWrite(project, nil)
		project.Role().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
			Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
			Resources: &[]*string{cfArn(dashboardDist)},
		}))
```

(The `repo.GrantPullPush(project)` and `taskDef.GrantRun(project)` lines already exist — shown only for placement; add just the two new statements after them.)

- [ ] **Step 3: Add the sync step to `buildspec.yml`**

In `buildspec.yml`'s `post_build.commands`, insert a new step right after `docker push "$ECR_URI:$TAG"` and before the "Re-render the projection dashboard" step:

```yaml
  post_build:
    commands:
      - docker push "$ECR_URI:latest"
      - docker push "$ECR_URI:$TAG"
      # Publish the static dashboard on every build so a push to main updates
      # it immediately (same pattern as the projection-site re-render below).
      - |
        if [ -n "${DASHBOARD_BUCKET:-}" ]; then
          aws s3 sync web/dashboard "s3://$DASHBOARD_BUCKET" --delete
          aws cloudfront create-invalidation --distribution-id "$DASHBOARD_CF_DIST_ID" --paths '/*'
        else
          echo "DASHBOARD_BUCKET not set (project env not yet deployed via cdk); skipping dashboard sync"
        fi
      # Re-render the projection dashboard with the image just pushed, so a push
      # to main updates the site immediately instead of waiting for the daily
      # ProjectionSite schedule. Fargate pulls :latest fresh on each RunTask, so
      # this uses the new build. Reuses the API's egress-only SG + public subnets.
      - |
        if [ -n "${CLUSTER:-}" ]; then
```

(Only the new `if [ -n "${DASHBOARD_BUCKET:-}" ]; then ... fi` block is new; the surrounding lines already exist and are shown for placement only.)

- [ ] **Step 4: Verify the stack still synthesizes and the YAML is valid**

Run: `cd infra && go build ./... && cdk synth >/dev/null`
Expected: no compile errors.

Run: `python3 -c "import yaml, sys; yaml.safe_load(open('buildspec.yml'))" && echo OK`
Expected: `OK` (buildspec.yml parses as valid YAML — catches indentation mistakes in the new step before it reaches CodeBuild).

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go buildspec.yml
git commit -m "feat(infra): auto-deploy the dashboard from the existing CodeBuild pipeline"
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md`
- Modify: `docs/aws-deployment.md`

- [ ] **Step 1: Add a "Web Dashboard" section to `README.md`**

Add a new section after the existing "Lineup HTTP API (read-only)" section (find it via `grep -n "Lineup HTTP API" README.md`):

```markdown
### Web Dashboard

A private, single-user web UI for the API above: today's lineup, a form to
trigger any of the 9 allowlisted jobs, run history with live status, and a
generic viewer for each job's typed output — plus links out to the recap and
projection-accuracy sites. Static files live in `web/dashboard/` (no build
step — plain ES modules) and deploy via the existing CodeBuild pipeline to
its own CloudFront distribution (`DashboardUrl` in the CDK stack outputs).

Auth is the same `ROSTERBOT_API_TOKEN` as the API above: paste it into the
dashboard's login screen once; it's stored in the browser and sent as the
`Authorization` header on every call. There's no separate login system.

**Run it locally before deploying:**

```bash
go run . optimize --dry-run --publish-lineup   # writes .lineup/lineup-today.json
ROSTERBOT_API_TOKEN=test go run . serve
open http://localhost:8080/                    # log in with "test"
```

`rosterbot serve --web <dir>` serves the dashboard's static files at `/` and
the API at `/v1/*` from the same local server — the same split CloudFront
does in production — so the dashboard needs no environment-specific
configuration and no CORS handling anywhere. Job triggering returns `501`
locally (no ECS); everything else (lineup, run history, output, activity
feed) works against real local files under `.lineup/`.
```

- [ ] **Step 2: Add the dashboard to the architecture list in `docs/aws-deployment.md`**

Find the bullet describing `ReportCdn` (search for `ReportCdn` in the file) and add a new bullet immediately after it:

```markdown
- **S3 dashboard bucket** (`DashboardBucket`) + **CloudFront** (`DashboardCdn`, URL in `DashboardUrl` stack output) — the private control-panel web UI (`web/dashboard/`, static, no build step). One distribution serves both surfaces: its default behavior serves the static files from `DashboardBucket`; an additional `/v1/*` behavior proxies straight to the `LineupApi` Function URL (`CachePolicy.CACHING_DISABLED`, `OriginRequestPolicy.ALL_VIEWER` so the `Authorization` header passes through), making the browser's calls same-origin with zero CORS configuration anywhere. CodeBuild's buildspec syncs `web/dashboard/` to `DASHBOARD_BUCKET` and invalidates `DASHBOARD_CF_DIST_ID` on every push to `main`, alongside the existing image build and `projection-site` re-render.
```

- [ ] **Step 3: Commit**

```bash
git add README.md docs/aws-deployment.md
git commit -m "docs: document the web dashboard and its local dev flow"
```

---

## Deploy (manual — run yourself, not as an unattended task step)

This provisions new IAM policies, an S3 bucket, and a CloudFront distribution on shared AWS infra — per this repo's own safety conventions, run it yourself rather than having an agent execute it unattended.

```bash
cd infra
export JSII_SILENCE_WARNING_UNTESTED_NODE_VERSION=1
cdk diff                                        # review one more time
cdk deploy -c enableBuild=true --require-approval never
```

Grab `DashboardUrl` from the output. Since the dashboard's static files (`web/dashboard/`) only sync via the CodeBuild buildspec step added in Task 6, the bucket is empty right after this `cdk deploy` — push this branch's commits to `main` (or manually run `aws s3 sync web/dashboard s3://<DashboardBucketName> --delete && aws cloudfront create-invalidation --distribution-id <dist-id> --paths '/*'` using the `DashboardBucketName`/`DashboardCdn` outputs) to populate it.

Then smoke-test against the real deployment:
1. Open `DashboardUrl` in a browser; confirm the login screen appears.
2. `aws ssm get-parameter --name /rosterbot/ROSTERBOT_API_TOKEN --with-decryption --query Parameter.Value --output text --region us-west-1` to get the real token; paste it in.
3. Confirm the Lineup view loads real data (assuming `optimize` has run at least once with `--publish-lineup`/on a non-dry-run).
4. Trigger a non-mutating job (e.g. `backtest`) from the Jobs tab; confirm it appears in Runs as `RUNNING` then transitions to `SUCCESS` within a few minutes (the 5s poll should pick it up without a manual reload), and that its output renders.
5. Confirm the Recap/Projections nav links open the existing sites.
