package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// requireBearer is the only thing between --listen and everyone who can reach
// the port.
const testToken = "s3cret"

func TestRequireBearer(t *testing.T) {
	t.Parallel()

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})

	for _, c := range []struct {
		name       string
		token      string
		header     string
		wantStatus int
		wantThru   bool
	}{
		{"no token configured lets everything through", "", "", http.StatusTeapot, true},
		{"no token configured ignores a header", "", "Bearer anything", http.StatusTeapot, true},
		{"the right token passes", testToken, "Bearer " + testToken, http.StatusTeapot, true},
		{"a missing header is refused", testToken, "", http.StatusUnauthorized, false},
		{"the wrong token is refused", testToken, "Bearer nope", http.StatusUnauthorized, false},
		{"the bare token without the scheme is refused", testToken, testToken, http.StatusUnauthorized, false},
		{"a prefix of the token is refused", testToken, "Bearer s3cre", http.StatusUnauthorized, false},
		{"the token with more after it is refused", testToken, "Bearer s3cretXX", http.StatusUnauthorized, false},
		{"the scheme is case sensitive", testToken, "bearer " + testToken, http.StatusUnauthorized, false},
	} {
		reached = false
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/mcp", http.NoBody)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		requireBearer(c.token, next).ServeHTTP(rec, req)

		if rec.Code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.wantStatus)
		}
		if reached != c.wantThru {
			t.Errorf("%s: handler reached = %v, want %v", c.name, reached, c.wantThru)
		}
		if c.wantStatus == http.StatusUnauthorized {
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Errorf("%s: WWW-Authenticate = %q, want a Bearer challenge", c.name, got)
			}
		}
	}
}

// The health probe has to stay outside the auth check, or a container with a
// token configured never becomes healthy.
func TestMuxRoutes(t *testing.T) {
	t.Parallel()

	server := mcp.NewServer(&mcp.Implementation{Name: "sonarr-mcp", Version: "test"}, nil)
	mux := newMux(server, testToken)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz with a token configured = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.TrimSpace(body) != "ok" {
		t.Errorf("/healthz body = %q", body)
	}

	// and the MCP endpoint is not
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, mcpPath, strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%s without a token = %d, want 401", mcpPath, rec.Code)
	}

	// healthz is a GET route, so another method must not reach it
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", http.NoBody))
	if rec.Code == http.StatusOK {
		t.Error("POST /healthz answered 200; the route is registered GET-only")
	}
}

// --listen with no bearer token is an open port, so it is refused unless the
// operator said so in as many words.
func TestServeNeedsAnAuthToken(t *testing.T) {
	t.Parallel()

	if err := checkAuth("", false); err == nil {
		t.Error("no token and no --allow-no-auth was not refused")
	} else if !strings.Contains(err.Error(), "SONARR_AUTH_TOKEN") || !strings.Contains(err.Error(), "allow-no-auth") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
	if err := checkAuth("", true); err != nil {
		t.Errorf("--allow-no-auth was refused: %v", err)
	}
	if err := checkAuth(testToken, false); err != nil {
		t.Errorf("a token was refused: %v", err)
	}
}
