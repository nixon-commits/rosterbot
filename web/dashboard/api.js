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

async function request(method, path, body) {
  const headers = body ? { "Content-Type": "application/json" } : {};
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
  infra: () => request("GET", "/v1/infra"),
  triggerJob: (name, params) => request("POST", `/v1/jobs/${encodeURIComponent(name)}`, { params }),

  authLoginBegin: () => request("POST", "/v1/auth/login/begin"),
  authLoginFinish: (assertion) => request("POST", "/v1/auth/login/finish", assertion),
  // An ENROLLMENT token, carried in the JSON body — not the Authorization
  // header. The global API token used to authorize registration and no longer
  // does: it proved you were the operator, and with several users that makes it
  // a god token whose holder could add a passkey to anybody's account
  // (rosterbot-crq.9). The server reads ?token= or a body "token" field and
  // resolves it to one specific person.
  //
  // Omit it entirely when adding a device while already logged in; the session
  // cookie authorizes that on its own.
  me: () => request("GET", "/v1/me"),
  setPreferences: (prefs) => request("POST", "/v1/me/preferences", prefs),
  tenants: () => request("GET", "/v1/tenants"),
  // Tenant management (admin only; the /v1/tenants prefix is allowlisted
  // server-side, these calls just 403 for a member).
  tenantInvite: (body) => request("POST", "/v1/tenants/invite", body),
  tenantSetStatus: (id, status) =>
    request("POST", `/v1/tenants/${encodeURIComponent(id)}/status`, { status }),
  tenantSetAutoApply: (id, autoApply) =>
    request("POST", `/v1/tenants/${encodeURIComponent(id)}/auto-apply`, { auto_apply: autoApply }),
  tenantRecovery: (id) =>
    request("POST", `/v1/tenants/${encodeURIComponent(id)}/recovery`, {}),
  tenantDelete: (id) => request("DELETE", `/v1/tenants/${encodeURIComponent(id)}`),
  connect: (username, password) => request("POST", "/v1/connect", { username, password }),

  authRegisterBegin: (enrollToken) =>
    request("POST", "/v1/auth/register/begin", enrollToken ? { token: enrollToken } : undefined),
  authRegisterFinish: (attestation, enrollToken) =>
    request("POST", "/v1/auth/register/finish",
      enrollToken ? { ...attestation, token: enrollToken } : attestation),
  authPasskeys: () => request("GET", "/v1/auth/passkeys"),
  authRenamePasskey: (id, name) =>
    request("POST", `/v1/auth/passkeys/${encodeURIComponent(id)}/name`, { name }),
  authRevokePasskey: (id) => request("DELETE", `/v1/auth/passkeys/${encodeURIComponent(id)}`),
  authLogout: () => request("POST", "/v1/auth/logout"),

  // Pending offers go through /v1 rather than the report/ prefix: report JSON
  // is served from the CloudFront root with no auth, and an open offer is not
  // something to publish to the world before deciding on it.
  trades: () => request("GET", "/v1/trades"),
  tradeValues: () => request("GET", "/v1/trades/values"),

  // The private reports go through /v1 for the same reason pending offers do:
  // the report/ prefix is served from the CloudFront root with no auth, and
  // these three are the manager's own performance — bench points left on the
  // table, projection grades, who reads their site. In a multi-tenant league
  // they are per-manager data, and the tenants are competitors.
  reportModel: () => request("GET", "/v1/reports/model"),
  reportViews: () => request("GET", "/v1/reports/views"),
  reportGap: () => request("GET", "/v1/reports/gap"),

  // League-wide standings stay on the public report/ prefix — every manager can
  // already read these numbers off Fantrax and Sleeper.
  reportValue: () => request("GET", "/report/value.json"),
  reportFootball: () => request("GET", "/report/football.json"),
  runProgress: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}/progress`),
};
