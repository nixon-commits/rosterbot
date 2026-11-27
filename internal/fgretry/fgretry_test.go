package fgretry

import (
	"net/http"
	"testing"
)

// The 403 row is the whole reason this package exists. Both FanGraphs fetch
// paths have to read it the same way — "an unsolved challenge, try again" —
// and before this they disagreed.
func TestRetryable(t *testing.T) {
	cases := []struct {
		code int
		want bool
		why  string
	}{
		{http.StatusForbidden, true, "Cloudflare's unsolved-challenge status, which clears on a scale of hours"},
		{http.StatusTooManyRequests, true, "a rate limit lifts on its own"},
		{http.StatusInternalServerError, true, "the origin failed to answer"},
		{http.StatusBadGateway, true, "the edge could not reach the origin"},
		{http.StatusServiceUnavailable, true, "the origin is briefly out"},
		{524, true, "Cloudflare's own origin-timeout status"},
		{http.StatusBadRequest, false, "a malformed request is malformed on every attempt"},
		{http.StatusUnauthorized, false, "a credential answer is not a blip"},
		{http.StatusNotFound, false, "a route that is gone stays gone"},
		{http.StatusGone, false, "a route that is gone stays gone"},
		{http.StatusOK, false, "a success is not a failure to retry"},
	}
	for _, c := range cases {
		if got := Retryable(c.code); got != c.want {
			t.Errorf("Retryable(%d) = %v, want %v -- %s", c.code, got, c.want, c.why)
		}
	}
}
