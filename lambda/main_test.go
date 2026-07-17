package main

import (
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// TestAdapt_RequestCookiesReachHandler guards against the Lambda Function URL
// bug where incoming cookies (delivered in the dedicated evt.Cookies field
// under payload format 2.0, not evt.Headers) never reached the wrapped
// http.Handler, so r.Cookie(...) always came up empty in production even
// though the browser sent a valid session/ceremony cookie.
func TestAdapt_RequestCookiesReachHandler(t *testing.T) {
	var sawCookie bool
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("foo"); err == nil && c.Value == "bar" {
			sawCookie = true
		}
		w.WriteHeader(http.StatusOK)
	})

	evt := events.LambdaFunctionURLRequest{
		RawPath: "/v1/whatever",
		Cookies: []string{"foo=bar"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	}

	if _, err := adapt(fake)(nil, evt); err != nil {
		t.Fatalf("adapt returned error: %v", err)
	}
	if !sawCookie {
		t.Fatal("handler did not see the request cookie from evt.Cookies")
	}
}

// TestAdapt_RequestCookiesJoinedWithMultiple checks that multiple incoming
// cookies are joined into one Cookie header (the standard wire format), not
// just the first one surviving.
func TestAdapt_RequestCookiesJoinedWithMultiple(t *testing.T) {
	var gotFoo, gotBaz bool
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("foo"); err == nil && c.Value == "bar" {
			gotFoo = true
		}
		if c, err := r.Cookie("baz"); err == nil && c.Value == "qux" {
			gotBaz = true
		}
		w.WriteHeader(http.StatusOK)
	})

	evt := events.LambdaFunctionURLRequest{
		RawPath: "/v1/whatever",
		Cookies: []string{"foo=bar", "baz=qux"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	}

	if _, err := adapt(fake)(nil, evt); err != nil {
		t.Fatalf("adapt returned error: %v", err)
	}
	if !gotFoo || !gotBaz {
		t.Fatalf("handler did not see both cookies: gotFoo=%v gotBaz=%v", gotFoo, gotBaz)
	}
}

// TestAdapt_ResponseCookiesReturned guards against the Lambda Function URL
// bug where every Set-Cookie header the handler wrote (ceremony cookie,
// session cookie, logout clears) was silently discarded because the response
// Cookies field was never populated from the recorder's Set-Cookie headers.
func TestAdapt_ResponseCookiesReturned(t *testing.T) {
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "baz", Value: "qux"})
		w.WriteHeader(http.StatusOK)
	})

	evt := events.LambdaFunctionURLRequest{
		RawPath: "/v1/whatever",
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	}

	resp, err := adapt(fake)(nil, evt)
	if err != nil {
		t.Fatalf("adapt returned error: %v", err)
	}

	var found bool
	for _, c := range resp.Cookies {
		if c == "baz=qux" {
			found = true
		}
	}
	if !found {
		t.Fatalf("response.Cookies = %v, want to contain \"baz=qux\"", resp.Cookies)
	}
}

// TestAdapt_ResponseMultipleCookiesReturned covers a response that sets more
// than one Set-Cookie header in the same response (e.g. clearing the
// ceremony cookie AND setting the session cookie on login/finish) — the
// response Cookies field must capture all of them, not just one.
func TestAdapt_ResponseMultipleCookiesReturned(t *testing.T) {
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1", MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
		w.WriteHeader(http.StatusOK)
	})

	evt := events.LambdaFunctionURLRequest{
		RawPath: "/v1/whatever",
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	}

	resp, err := adapt(fake)(nil, evt)
	if err != nil {
		t.Fatalf("adapt returned error: %v", err)
	}
	if len(resp.Cookies) != 2 {
		t.Fatalf("response.Cookies = %v, want 2 entries", resp.Cookies)
	}
}
