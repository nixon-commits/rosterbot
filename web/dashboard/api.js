// api.js — thin fetch wrapper around the rosterbot control API. Every request
// is a same-origin relative path (CloudFront path-routes /v1/* to the Lambda
// in production; `rosterbot serve --web` does the same locally), so no CORS
// handling is needed anywhere in this app. Auth rides a same-origin,
// httpOnly session cookie set by the passkey login ceremony — there is no
// client-readable token to store.

// ApiError carries the HTTP status so callers can special-case 401
// (unauthenticated), 404 (nothing published yet / no passkeys registered),
// 409 (job already running), and 501 (job triggering unavailable locally)
// without parsing message strings.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function request(method, path, body, bootstrapToken) {
  const headers = body ? { "Content-Type": "application/json" } : {};
  if (bootstrapToken) headers["Authorization"] = "Bearer " + bootstrapToken;
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers,
    body: body ? JSON.stringify(body) : undefined,
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

  authLoginBegin: () => request("POST", "/v1/auth/login/begin"),
  authLoginFinish: (assertion) => request("POST", "/v1/auth/login/finish", assertion),
  authRegisterBegin: (bootstrapToken) => request("POST", "/v1/auth/register/begin", undefined, bootstrapToken),
  authRegisterFinish: (attestation, bootstrapToken) =>
    request("POST", "/v1/auth/register/finish", attestation, bootstrapToken),
  authPasskeys: () => request("GET", "/v1/auth/passkeys"),
  authRevokePasskey: (id) => request("DELETE", `/v1/auth/passkeys/${encodeURIComponent(id)}`),
  authLogout: () => request("POST", "/v1/auth/logout"),

  // Report JSON is served from the CloudFront root, not /v1.
  reportModel: () => request("GET", "/report/model.json"),
  reportValue: () => request("GET", "/report/value.json"),
  runProgress: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}/progress`),
};
