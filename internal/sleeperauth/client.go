// Package sleeperauth is the AUTHENTICATED Sleeper client: the undocumented
// GraphQL API behind the Sleeper app, driven with the operator's own session
// token.
//
// It is a separate package from internal/sleeper (the public, token-free REST
// client) so the token's blast radius is visible in the import graph:
// TestOnlyTheOffersCommandImportsThisPackage pins that no internal/* package
// and no cmd/ file other than football_offers.go imports it. There is
// deliberately no on-disk cache here — the reads exist for freshness, the
// volume is a dozen calls an hour, and a cache would be one more place a
// token-derived key could end up.
//
// v1 is READ-ONLY. There is no Mutate. When writes arrive they take a
// WriteAuthorization value obtainable only through an explicit opt-in (an
// --apply-style flag plus an env switch), mirroring internal/lineuprun's
// applyAuthorization, so a write cannot happen by statement order.
//
// The API is unsupported by Sleeper: the schema can change without notice and
// the token grants full account access for about a year. See
// docs/dynasty-football.md for the operating notes and the reference the
// query shapes were checked against.
package sleeperauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Endpoint is the app's own GraphQL host. A var so tests can point it at an
// httptest server; production never changes it.
var Endpoint = "https://sleeper.app/graphql"

// EnvToken is the env var FromEnv reads. The value is the `token` entry the
// Sleeper web app keeps in localStorage; the operator places it in .env
// locally and in SSM for the deployment. Nothing in this package prints it.
const EnvToken = "SLEEPER_TOKEN"

// userAgent identifies the client honestly. Sleeper answers 403 to a request
// with no User-Agent at all.
const userAgent = "rosterbot (+https://github.com/nixon-commits/rosterbot)"

var (
	// ErrNoToken is returned by FromEnv when SLEEPER_TOKEN is unset, so the
	// one command that needs it fails fast with a plain message.
	ErrNoToken = errors.New("sleeperauth: SLEEPER_TOKEN is not set")
	// ErrUnauthorized covers both HTTP 401 and a GraphQL error carrying
	// code "unauthorized": the token has expired, been revoked, or is wrong.
	// Callers fail the whole run on it — every league would fail the same way.
	ErrUnauthorized = errors.New("sleeperauth: unauthorized (token expired, revoked, or wrong)")
)

// Client posts GraphQL documents with the operator's token.
type Client struct {
	http  *http.Client
	token string
}

// FromEnv builds a client from SLEEPER_TOKEN.
func FromEnv() (*Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvToken))
	if tok == "" {
		return nil, ErrNoToken
	}
	return New(tok), nil
}

// New builds a client for a token the caller already holds (tests).
func New(token string) *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}, token: token}
}

// String returns a redacted representation of the client.
func (c *Client) String() string {
	return "sleeperauth.Client{token: [redacted]}"
}

// GoString returns a redacted representation of the client for %#v formatting.
func (c *Client) GoString() string {
	return c.String()
}

// redact replaces the token with [redacted] in the given string.
// Defence in depth: the server controls error message content and could echo
// the Authorization header. If it does, this scrubs the token before the
// message reaches the caller's logs or error reporting.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// gqlError is one entry of a GraphQL errors[] array. Sleeper always sets
// code, message and path; data is ignored.
type gqlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e gqlError) String() string {
	parts := make([]string, 0, len(e.Path))
	for _, p := range e.Path {
		parts = append(parts, fmt.Sprint(p))
	}
	if len(parts) == 0 {
		return e.Message
	}
	return strings.Join(parts, ".") + ": " + e.Message
}

// Query posts doc (with vars, which may be nil) and decodes the response's
// data into out.
//
// Error mapping: HTTP 401 and any errors[] entry with code "unauthorized"
// become ErrUnauthorized; any other non-2xx status becomes an error naming
// the status only (the body is dropped — Sleeper sometimes answers with an
// HTML page, and a body is the one place a token could be echoed back);
// other GraphQL errors are joined into one error naming each path and
// message. The token is never formatted into anything.
func (c *Client) Query(ctx context.Context, doc string, vars map[string]any, out any) error {
	payload := map[string]any{"query": doc}
	if vars != nil {
		payload["variables"] = vars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sleeperauth: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sleeperauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sleeperauth: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("sleeperauth: http %d", resp.StatusCode)
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("sleeperauth: decode response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			if e.Code == "unauthorized" {
				return fmt.Errorf("%w: %s", ErrUnauthorized, c.redact(e.String()))
			}
			msgs = append(msgs, c.redact(e.String()))
		}
		return fmt.Errorf("sleeperauth: graphql: %s", strings.Join(msgs, "; "))
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("sleeperauth: decode data: %w", err)
	}
	return nil
}
