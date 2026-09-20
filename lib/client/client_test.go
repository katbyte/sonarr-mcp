package client

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "tok-123"

// options is a hand-written options object, the shape generated ones take.
type options struct {
	query  map[string][]string
	header map[string]string
}

func (o options) ToHeaders() *Headers {
	out := Headers{}
	for k, v := range o.header {
		out.Append(k, v)
	}
	return &out
}

func (o options) ToQuery() *QueryParams {
	out := QueryParams{}
	for k, vs := range o.query {
		for _, v := range vs {
			out.Append(k, v)
		}
	}
	return &out
}

// serve starts a canned server and a client for it, authenticating with
// testToken.
func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, APIKey(testToken))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// the server's own client, so closing another test's server cannot
	// close this one's connections
	c.HTTPClient = srv.Client()

	return c
}

// execute builds and sends one request the way a generated method does.
func execute(t *testing.T, c *Client, opts RequestOptions, body any) (*Response, error) {
	t.Helper()

	req, err := c.NewRequest(t.Context(), opts)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		if err := req.Marshal(body); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	}

	return req.Execute(t.Context())
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, url, wantErr, wantBase string
		auth                         Authorizer
	}{
		{name: "ok", url: "http://nas:8989", auth: APIKey("t"), wantBase: "http://nas:8989"},
		{name: "trailing slash trimmed", url: "http://nas:8989/sonarr/", auth: APIKey("t"), wantBase: "http://nas:8989/sonarr"},
		{name: "missing url", url: "", auth: APIKey("t"), wantErr: "server URL is required"},
		{name: "no scheme", url: "nas:8989", auth: APIKey("t"), wantErr: "must include a scheme and host"},
		{name: "no host", url: "http://", auth: APIKey("t"), wantErr: "must include a scheme and host"},
		{name: "credentials in url", url: "http://kt:pw@nas:8989", auth: APIKey("t"), wantErr: "must not contain credentials"}, //nolint:gosec // the point of the case
		{name: "no authorizer", url: "http://nas:8989", wantErr: "authorizer is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(tt.url, tt.auth)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("New(%q) err = %v, want containing %q", tt.url, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.BaseURL != tt.wantBase {
				t.Errorf("BaseURL = %q, want %q", c.BaseURL, tt.wantBase)
			}
		})
	}
}

func TestAPIKey(t *testing.T) {
	t.Parallel()

	var got http.Header
	var query string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})
	if _, err := execute(t, c, RequestOptions{HttpMethod: http.MethodGet, Path: "/api/v3/system/status", ExpectedStatusCodes: []int{http.StatusOK}}, nil); err != nil {
		t.Fatal(err)
	}

	if v := got.Get("X-Api-Key"); v != testToken {
		t.Errorf("X-Api-Key = %q, want %q", v, testToken)
	}
	// the key never goes in the query string, where access logs keep it
	if strings.Contains(query, testToken) {
		t.Errorf("the key is in the query string: %q", query)
	}
	if got.Get("Accept") != "application/json" || got.Get("User-Agent") != UserAgent {
		t.Errorf("Accept %q, User-Agent %q", got.Get("Accept"), got.Get("User-Agent"))
	}
}

func TestOptionsObject(t *testing.T) {
	t.Parallel()

	var got *http.Request
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	opts := options{
		query:  map[string][]string{"fields": {"Path", "Genres"}, "Limit": {"5"}},
		header: map[string]string{"X-Api-Key": "override"},
	}
	if _, err := execute(t, c, RequestOptions{HttpMethod: http.MethodGet, Path: "/api/v3/series", ExpectedStatusCodes: []int{http.StatusOK}, OptionsObject: opts}, nil); err != nil {
		t.Fatal(err)
	}

	if got.URL.Path != "/api/v3/series" || got.URL.Query().Get("Limit") != "5" {
		t.Errorf("request = %s", got.URL)
	}
	if fields := got.URL.Query()["fields"]; len(fields) != 2 || fields[0] != "Path" || fields[1] != "Genres" {
		t.Errorf("fields = %v, want one key per value", fields)
	}
	// a header option wins over the authorizer's
	if v := got.Header.Get("X-Api-Key"); v != "override" {
		t.Errorf("X-Api-Key = %q, want the option's", v)
	}
}

func TestStatusError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		body     string
		wantMsg  []string
		notFound bool
	}{
		{name: "401 hint", status: http.StatusUnauthorized, body: "bad token", wantMsg: []string{"GET /api/v3/system/status: HTTP 401 (expected 200): bad token", "API key rejected"}},
		// Sonarr answers a failed validation with the failures, one per field
		{
			name: "validation failures", status: http.StatusBadRequest,
			body:    `[{"propertyName":"Path","errorMessage":"Path is already configured","severity":"error"},{"propertyName":"RootFolderPath","errorMessage":"Folder is not writable","severity":"warning"}]`,
			wantMsg: []string{"HTTP 400 (expected 200): Path: Path is already configured; RootFolderPath: Folder is not writable (warning)"},
		},
		// and every other failure with a message
		{name: "error model", status: http.StatusConflict, body: `{"message":"Series already exists","description":"stack"}`, wantMsg: []string{"HTTP 409 (expected 200): Series already exists"}},
		{name: "404 is not found", status: http.StatusNotFound, body: "nope", wantMsg: []string{"HTTP 404 (expected 200): nope"}, notFound: true},
		// a 2xx the operation does not document is an error too
		{name: "undocumented 204", status: http.StatusNoContent, wantMsg: []string{"HTTP 204 (expected 200)"}},
		{name: "long body truncated", status: http.StatusBadRequest, body: strings.Repeat("x", 1000), wantMsg: []string{"HTTP 400", strings.Repeat("x", errBodyPreview) + "..."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			resp, err := execute(t, c, RequestOptions{HttpMethod: http.MethodGet, Path: "/api/v3/system/status", ExpectedStatusCodes: []int{http.StatusOK}}, nil)

			se, ok := errors.AsType[*StatusError](err)
			if !ok {
				t.Fatalf("err = %v, want *StatusError", err)
			}
			if se.StatusCode != tt.status || StatusCode(err) != tt.status || se.Method != http.MethodGet || se.Path != "/api/v3/system/status" {
				t.Errorf("StatusError = %+v", se)
			}
			for _, want := range tt.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Error() = %q, want containing %q", err.Error(), want)
				}
			}
			if IsNotFound(err) != tt.notFound || WasNotFound(resp.Response) != tt.notFound {
				t.Errorf("IsNotFound = %v, WasNotFound = %v, want %v", IsNotFound(err), WasNotFound(resp.Response), tt.notFound)
			}
			// the response comes back with the error, its whole body readable
			if b, _ := io.ReadAll(resp.Body); string(b) != tt.body {
				t.Errorf("body after the error = %d bytes, want %d", len(b), len(tt.body))
			}
		})
	}
	if IsNotFound(errors.New("other")) || StatusCode(nil) != 0 || WasNotFound(nil) {
		t.Error("a non-status error reads as a status")
	}
}

func TestBodies(t *testing.T) {
	t.Parallel()

	t.Run("json both ways, body readable again", func(t *testing.T) {
		t.Parallel()

		c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"Label":"kt"}` || r.ContentLength != int64(len(b)) {
				t.Errorf("body = %s (ContentLength %d)", b, r.ContentLength)
			}
			_, _ = io.WriteString(w, `{"id":7}`)
		})
		resp, err := execute(t, c, RequestOptions{ContentType: "application/json", HttpMethod: http.MethodPost, Path: "/api/v3/tag", ExpectedStatusCodes: []int{http.StatusOK}},
			struct{ Label string }{"kt"})
		if err != nil {
			t.Fatal(err)
		}
		var model struct{ ID int }
		if err := resp.Unmarshal(&model); err != nil || model.ID != 7 {
			t.Fatalf("Unmarshal = %+v, %v", model, err)
		}
		if b, _ := io.ReadAll(resp.Body); string(b) != `{"id":7}` {
			t.Errorf("body after Unmarshal = %q", b)
		}
	})

	t.Run("empty answer leaves the model alone", func(t *testing.T) {
		t.Parallel()

		c := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		resp, err := execute(t, c, RequestOptions{HttpMethod: http.MethodPost, Path: "/x", ExpectedStatusCodes: []int{http.StatusOK, http.StatusNoContent}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		model := map[string]string{"kept": "yes"}
		if err := resp.Unmarshal(&model); err != nil || model["kept"] != "yes" {
			t.Errorf("Unmarshal of nothing = %v, %v", model, err)
		}
	})

	t.Run("bad json is an error", func(t *testing.T) {
		t.Parallel()

		c := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") })
		resp, err := execute(t, c, RequestOptions{HttpMethod: http.MethodGet, Path: "/api/v3/system/status", ExpectedStatusCodes: []int{http.StatusOK}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var model struct{}
		if err := resp.Unmarshal(&model); err == nil || !strings.Contains(err.Error(), "GET /api/v3/system/status: decoding response") {
			t.Errorf("Unmarshal = %v, want a decoding error", err)
		}
	})

	t.Run("raw body needs a concrete type for a range", func(t *testing.T) {
		t.Parallel()

		var gotType string
		c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			gotType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusNoContent)
		})
		req, err := c.NewRequest(t.Context(), RequestOptions{ContentType: "image/*", HttpMethod: http.MethodPost, Path: "/api/v3/system/backup/restore/upload", ExpectedStatusCodes: []int{http.StatusNoContent}})
		if err != nil {
			t.Fatal(err)
		}
		if err := req.SetBody(strings.NewReader("png"), ""); err == nil || !strings.Contains(err.Error(), "image/*") {
			t.Errorf("SetBody with no type for image/* = %v, want an error", err)
		}
		if err := req.SetBody(bytes.NewReader([]byte("png")), "image/png"); err != nil {
			t.Fatal(err)
		}
		if _, err := req.Execute(t.Context()); err != nil {
			t.Fatal(err)
		}
		if gotType != "image/png" {
			t.Errorf("Content-Type = %q", gotType)
		}
	})

	t.Run("stream left unread", func(t *testing.T) {
		t.Parallel()

		c := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "log line") })
		resp, err := execute(t, c, RequestOptions{HttpMethod: http.MethodGet, Path: "/api/v3/log/file/sonarr.txt", ExpectedStatusCodes: []int{http.StatusOK}, StreamResponse: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if b, _ := io.ReadAll(resp.Body); string(b) != "log line" {
			t.Errorf("stream = %q", b)
		}
	})
}

func TestListHelpers(t *testing.T) {
	t.Parallel()

	type kind string
	if got := CSV([]kind{"Movie", "Series"}); got != "Movie,Series" {
		t.Errorf("CSV(kinds) = %q", got)
	}
	if got := CSV([]int{1979, 1986}); got != "1979,1986" {
		t.Errorf("CSV(ints) = %q", got)
	}
	if got := JSONObject(map[string]string{"a": "b"}); got != `{"a":"b"}` {
		t.Errorf("JSONObject = %q", got)
	}
}

// A Sonarr behind a reverse proxy is reached at a URL base - http://nas/sonarr
// - which every request has to keep, or it lands on the proxy's own 404.
func TestURLBase(t *testing.T) {
	t.Parallel()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/sonarr/") {
			http.NotFound(w, r) // the proxy serves something else here
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"4.0.20.3014"}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL+"/sonarr/", APIKey(testToken))
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = srv.Client()

	resp, err := execute(t, c, RequestOptions{
		HttpMethod: http.MethodGet, Path: "/api/v3/system/status", ExpectedStatusCodes: []int{http.StatusOK},
	}, nil)
	if err != nil {
		t.Fatalf("a request under a URL base: %v", err)
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := resp.Unmarshal(&status); err != nil || status.Version == "" {
		t.Fatalf("decoding = %v, %v", status, err)
	}
	if len(paths) != 1 || paths[0] != "/sonarr/api/v3/system/status" {
		t.Errorf("requests went to %v", paths)
	}
}

// A request that never reaches Sonarr says what went wrong in words its
// operator can act on, rather than Go's own.
func TestUnreachableServer(t *testing.T) {
	t.Parallel()

	// a port nothing listens on, found by closing a listener
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, url, want string
	}{
		{"nothing listening", "http://" + addr, "nothing is listening there"},
		{"a name that does not resolve", "http://sonarr.invalid:8989", "does not resolve"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(c.url, APIKey(testToken))
			if err != nil {
				t.Fatal(err)
			}
			client.HTTPClient = &http.Client{Timeout: 5 * time.Second}
			_, err = execute(t, client, RequestOptions{
				HttpMethod: http.MethodGet, Path: "/api/v3/system/status", ExpectedStatusCodes: []int{http.StatusOK},
			}, nil)
			if err == nil {
				t.Fatal("a request to nothing succeeded")
			}
			if !strings.Contains(err.Error(), "cannot reach Sonarr at "+c.url) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("= %v", err)
			}
		})
	}
}
