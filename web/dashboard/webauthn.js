// webauthn.js — browser-side WebAuthn ceremony helpers: binary<->base64url
// conversion (the WebAuthn API's credential fields are ArrayBuffers, which
// don't survive JSON.stringify/the server's JSON responses) and the
// register/login ceremony orchestration.
import { api } from "./api.js";

function bufToB64url(buf) {
  const bytes = new Uint8Array(buf);
  let str = "";
  for (const b of bytes) str += String.fromCharCode(b);
  return btoa(str).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function b64urlToBuf(b64url) {
  const pad = (4 - (b64url.length % 4)) % 4;
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat(pad);
  const str = atob(b64);
  const bytes = new Uint8Array(str.length);
  for (let i = 0; i < str.length; i++) bytes[i] = str.charCodeAt(i);
  return bytes.buffer;
}

// decodeCreationOptions/decodeRequestOptions convert the server's JSON (every
// binary field as a base64url string) into the ArrayBuffer shape
// navigator.credentials.create()/.get() require.
function decodeCreationOptions(opts) {
  const pk = opts.publicKey;
  return {
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      user: { ...pk.user, id: b64urlToBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

function decodeRequestOptions(opts) {
  const pk = opts.publicKey;
  return {
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      allowCredentials: (pk.allowCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

// encodeAttestation/encodeAssertion convert navigator.credentials' result
// back into the JSON shape the server's protocol.ParseCredentialCreationResponse
// / ParseCredentialRequestResponse expect. `id` is already a base64url string
// per the WebAuthn spec (the browser computes it, unlike rawId).
function encodeAttestation(cred) {
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: bufToB64url(cred.response.attestationObject),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
    },
  };
}

function encodeAssertion(cred) {
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bufToB64url(cred.response.authenticatorData),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
      signature: bufToB64url(cred.response.signature),
      userHandle: cred.response.userHandle ? bufToB64url(cred.response.userHandle) : null,
    },
  };
}

// registerPasskey runs a full registration ceremony. bootstrapToken is the
// ROSTERBOT_API_TOKEN, required only when setting up the very first passkey
// (no session cookie exists yet); omit it when adding an additional device
// while already logged in.
export async function registerPasskey(bootstrapToken) {
  const opts = await api.authRegisterBegin(bootstrapToken);
  const cred = await navigator.credentials.create(decodeCreationOptions(opts));
  await api.authRegisterFinish(encodeAttestation(cred), bootstrapToken);
}

// loginWithPasskey runs a full login ceremony against the one registered
// identity and, on success, leaves the browser holding a session cookie.
export async function loginWithPasskey() {
  const opts = await api.authLoginBegin();
  const cred = await navigator.credentials.get(decodeRequestOptions(opts));
  await api.authLoginFinish(encodeAssertion(cred));
}
