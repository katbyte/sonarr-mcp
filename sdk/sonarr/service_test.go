package sonarr

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/pandorest/client"
)

// sonarrAt is a client for a canned Sonarr, which records what it was asked.
func sonarrAt(t *testing.T, base string, h http.HandlerFunc) (*Client, *[]*http.Request) {
	t.Helper()

	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL+base, "the-key")
	if err != nil {
		t.Fatal(err)
	}

	return c, &seen
}

// What Sonarr says went wrong is read out of its two error shapes, each
// thing it said on its own, and anything else is left for the client to
// show the start of.
func TestErrorMessages(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, body string
		want       []string
	}{
		{"a failed validation, field by field", `[{"propertyName":"Path","errorMessage":"Path is already configured","severity":"error"},{"propertyName":"RootFolderPath","errorMessage":"Folder is not writable","severity":"warning"},{"propertyName":"Skipped","errorMessage":""}]`, []string{"Path: Path is already configured", "RootFolderPath: Folder is not writable (warning)"}},
		{"a failure with no field", `[{"errorMessage":"Unable to connect"}]`, []string{"Unable to connect"}},
		{"any other failure", ` {"message":"Series already exists","description":"a stack trace"} `, []string{"Series already exists"}},
		{"an object with no message", `{"error":"nope"}`, nil},
		{"not JSON", "bad gateway", nil},
		{"a list of something else", `["a","b"]`, nil},
		{"nothing", "", nil},
	} {
		if got := errorMessages([]byte(tt.body)); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A refusal carries Sonarr's own words, joined in the error's text and one by
// one for a tool that lists them, and a 401 says where the key is found.
func TestRefusals(t *testing.T) {
	t.Parallel()

	c, _ := sonarrAt(t, "", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/rootfolder") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `[{"propertyName":"Path","errorMessage":"Path is already configured","severity":"error"},{"propertyName":"Path","errorMessage":"Folder is not writable","severity":"warning"}]`)

			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := c.PostRootFolder(t.Context(), RootFolderResource{Path: "/tv"})
	if client.StatusCode(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "Path: Path is already configured; Path: Folder is not writable (warning)") {
		t.Errorf("a failed validation = %v", err)
	}
	if got := client.Messages(err); len(got) != 2 || got[0] != "Path: Path is already configured" {
		t.Errorf("its messages = %q, want the two failures apart", got)
	}

	_, err = c.GetSystemStatus(t.Context())
	if client.StatusCode(err) != http.StatusUnauthorized || !strings.Contains(err.Error(), "API key rejected; check the key under Settings → General → Security") {
		t.Errorf("a key Sonarr does not take = %v", err)
	}
}

// A Sonarr behind a reverse proxy is reached at a URL base - http://nas/sonarr
// - which every request has to keep, or it lands on the proxy's own 404. Each
// request carries the key in the header Sonarr reads, and says who is asking.
func TestURLBaseKeyAndUserAgent(t *testing.T) {
	t.Parallel()

	c, seen := sonarrAt(t, "/sonarr/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"4.0.20.3014"}`)
	})

	res, err := c.GetSystemStatus(t.Context())
	if err != nil || res.Model == nil || res.Model.Version != "4.0.20.3014" {
		t.Fatalf("a request under a URL base = %+v, %v", res.Model, err)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d requests, want one", len(*seen))
	}
	got := (*seen)[0]
	if got.URL.Path != "/sonarr/api/v3/system/status" {
		t.Errorf("the request went to %s", got.URL.Path)
	}
	if got.Header.Get("X-Api-Key") != "the-key" || got.URL.RawQuery != "" {
		t.Errorf("the key: header %q, query %q; want it in the header alone", got.Header.Get("X-Api-Key"), got.URL.RawQuery)
	}
	if ua := got.Header.Get("User-Agent"); !strings.HasPrefix(ua, "sonarr-mcp/") {
		t.Errorf("User-Agent = %q, want sonarr-mcp and its version", ua)
	}
}

// An address that is no address is refused before anything is sent, with
// what a Sonarr's looks like.
func TestNewRefusesWhatIsNoAddress(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "nas:8989", "http://user:pass@nas:8989", "http://nas:8989/?apikey=x"} {
		if _, err := New(bad, "the-key"); err == nil {
			t.Errorf("New(%q) made a client", bad)
		}
	}
	if _, err := New("nas", "the-key"); err == nil || !strings.Contains(err.Error(), "http://nas:8989") || !strings.Contains(err.Error(), "Sonarr") {
		t.Errorf("an address with no scheme = %v, want Sonarr named and an example given", err)
	}
	if _, err := New("http://nas:8989", ""); err == nil {
		t.Error("New without an API key made a client")
	}
}
