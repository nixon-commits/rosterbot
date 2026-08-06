package fantrax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

// VersionStatus is the outcome of probing Fantrax's server-side client-version
// gate with the version this binary is pinned to.
type VersionStatus int

const (
	// VersionOK means the pinned version passed Fantrax's gate.
	VersionOK VersionStatus = iota
	// VersionStale means Fantrax rejected the pinned version outright.
	VersionStale
	// VersionUnknown means the probe could not determine the answer: an
	// unrecognized pageError code, a non-200, or a transport failure.
	VersionUnknown
)

func (s VersionStatus) String() string {
	switch s {
	case VersionOK:
		return "ok"
	case VersionStale:
		return "stale"
	default:
		return "unknown"
	}
}

// versionProbeURL is the Fantrax API endpoint. Var for test overriding.
var versionProbeURL = "https://www.fantrax.com/fxpa/req"

// versionProbeTimeout bounds the single probe request.
const versionProbeTimeout = 20 * time.Second

// ControlVersion is a deliberately ancient client version used as a positive
// control for the probe itself. Fantrax's gate is a MINIMUM check, not an
// equality check — measured live on 2026-08-05, the floor sits between 181.0.0
// (STALE_CLIENT) and 182.0.0 (WARNING_NOT_LOGGED_IN), and 999.999.999 passes.
// So a version far below the floor must come back STALE_CLIENT for as long as
// the gate exists at all, which is what makes it a control: if it stops
// answering STALE_CLIENT, the probe has stopped discriminating and every
// subsequent green reading is meaningless.
//
// Far below the floor on purpose. A control just under the pin (184.x) would
// be a tighter test of where the threshold sits, and would false-alarm the
// moment Fantrax lowered it — the control must only fail when the MECHANISM
// breaks, never when the threshold moves. A moved threshold is already the
// real probe's job to report.
const ControlVersion = "1.0.0"

// CheckControlVersion probes with ControlVersion. A working gate answers
// VersionStale; anything else means the probe can no longer tell a good pin
// from a bad one. See rosterbot-0a1.
func CheckControlVersion(ctx context.Context) (VersionStatus, string, error) {
	return probeVersion(ctx, ControlVersion)
}

// CheckAPIVersion asks Fantrax whether the version this binary pins still
// passes its server-side gate. It deliberately does NOT authenticate: the
// version gate is checked ahead of auth, so an unauthenticated getUserInfo
// separates the two cases cleanly —
//
//	stale pin    -> pageError.code == "STALE_CLIENT"
//	current pin  -> pageError.code == "WARNING_NOT_LOGGED_IN" (gate passed)
//
// Verified in both directions against live Fantrax on 2026-08-04.
//
// This validates the local pin rather than re-deriving the upstream value.
// Reading the current version out of Fantrax's web bundle is possible (see the
// runbook on auth_client.APIVersion) but means parsing an unstable Angular
// chunk layout and risks confidently adopting a wrong value; asking whether our
// own constant still works has a yes/no answer and nothing to get wrong.
//
// The returned string is the observed pageError code, empty when there was
// none. An error is returned only for transport/protocol failures, never for
// VersionStale — a stale pin is a successful probe with a bad answer.
func CheckAPIVersion(ctx context.Context) (VersionStatus, string, error) {
	return probeVersion(ctx, auth_client.APIVersion)
}

// probeVersion runs the unauthenticated gate probe with an arbitrary client
// version. CheckAPIVersion passes the pinned constant; CheckControlVersion
// passes ControlVersion. The version is overwritten on the envelope rather than
// threaded through auth_client.BuildFullRequest, which always stamps the pinned
// constant — that constant staying the single source of truth for real calls is
// the whole point of rosterbot-7i3, so the override lives here, at the one
// caller that deliberately sends something else.
func probeVersion(ctx context.Context, version string) (VersionStatus, string, error) {
	payload := auth_client.BuildFullRequest(
		[]auth_client.FantraxMessage{{
			Method: "getUserInfo",
			Data:   map[string]string{},
		}},
		"https://www.fantrax.com/",
	)
	payload["v"] = version

	body, err := json.Marshal(payload)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("marshal version probe: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, versionProbeURL, bytes.NewReader(body))
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("create version probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("send version probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VersionUnknown, "", fmt.Errorf("version probe returned status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("read version probe response: %w", err)
	}

	// Parsed here rather than via auth_client.ReadBody, which collapses every
	// pageError code into an error — this is the one caller that must branch on
	// the code itself.
	var envelope struct {
		PageError struct {
			Code string `json:"code"`
		} `json:"pageError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return VersionUnknown, "", fmt.Errorf("unmarshal version probe response: %w", err)
	}

	switch code := envelope.PageError.Code; code {
	case "STALE_CLIENT":
		return VersionStale, code, nil
	case "", "WARNING_NOT_LOGGED_IN":
		return VersionOK, code, nil
	default:
		return VersionUnknown, code, nil
	}
}
