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
