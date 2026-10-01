package sleeperauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The token in every test is a made-up string; the point of several of these
// tests is that it never appears anywhere but the authorization header.
const testToken = "tok-abc123-NEVER-LOG-ME"

// serve runs a handler on an httptest server and points Endpoint at it for the
// test's lifetime.
func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := Endpoint
	Endpoint = srv.URL
	t.Cleanup(func() { Endpoint = old })
}

func TestFromEnv_MissingTokenIsErrNoToken(t *testing.T) {
	t.Setenv(EnvToken, "")
	if _, err := FromEnv(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	t.Setenv(EnvToken, testToken)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestQuery_SendsTokenUserAgentAndBody(t *testing.T) {
	var gotAuth, gotUA, gotCT string
	var gotBody map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotCT = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":"42"}}}`))
	})
	var out struct {
		Me struct {
			UserID string `json:"user_id"`
		} `json:"me"`
	}
	err := New(testToken).Query(context.Background(), `{ me { user_id } }`, map[string]any{"x": 1}, &out)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotAuth != testToken {
		t.Errorf("authorization = %q, want the raw token (no Bearer prefix)", gotAuth)
	}
	if !strings.Contains(gotUA, "rosterbot") {
		t.Errorf("user-agent = %q, want it to name rosterbot", gotUA)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["query"] != `{ me { user_id } }` || gotBody["variables"].(map[string]any)["x"] != float64(1) {
		t.Errorf("body = %v", gotBody)
	}
	if out.Me.UserID != "42" {
		t.Errorf("decoded user_id = %q", out.Me.UserID)
	}
}

func TestQuery_HTTP401IsUnauthorizedAndNeverLeaksTheToken(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token ` + testToken + `"}`)) // a hostile body echoing the token
	})
	err := New(testToken).Query(context.Background(), `{ me { user_id } }`, nil, &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text contains the token: %q", err.Error())
	}
}

func TestQuery_GraphQLUnauthorizedCodeIsUnauthorized(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":null},"errors":[{"code":"unauthorized","data":{},"message":"Unauthorized","path":["league_transactions"]}]}`))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text contains the token: %q", err.Error())
	}
}

func TestQuery_OtherGraphQLErrorsAreJoinedAndNamePaths(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"code":"x","message":"first","path":["a"]},{"code":"y","message":"second","path":["b","c"]}]}`))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"first", "second", "a", "b.c"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}

func TestQuery_NonJSONBodyIsAnErrorNotAPanic(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>oops</html>`)) // the server does this for some malformed requests
	})
	if err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{}); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestQuery_Non2xxStatusIsAnErrorWithoutTheBody(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(testToken))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuery_ServerEchoedTokenInGraphQLMessageIsRedacted(t *testing.T) {
	// Test that a regular GraphQL error echoing the token is redacted
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"code":"x","message":"bad header ` + testToken + `","path":["q"]}]}`))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error contains token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error should contain [redacted]: %q", err.Error())
	}

	// Test that an unauthorized GraphQL error echoing the token is redacted
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"code":"unauthorized","message":"bad token ` + testToken + `","path":["league"]}]}`))
	})
	err = New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error contains token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error should contain [redacted]: %q", err.Error())
	}
}

func TestClient_FormattingNeverPrintsTheToken(t *testing.T) {
	c := New(testToken)
	// Both the pointer and the dereferenced value: the stringers have value
	// receivers, so a copied Client cannot print the unexported token either.
	for _, s := range []string{
		fmt.Sprint(c),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%+v", c),
		fmt.Sprintf("%#v", c),
		fmt.Sprint(*c),
		fmt.Sprintf("%v", *c),
		fmt.Sprintf("%+v", *c),
		fmt.Sprintf("%#v", *c),
	} {
		if strings.Contains(s, testToken) {
			t.Fatalf("formatted client contains token: %q", s)
		}
		if !strings.Contains(s, "[redacted]") {
			t.Fatalf("formatted client should contain [redacted]: %q", s)
		}
	}
}
