package testenv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A suite runs only when the script's whole environment is there: a server
// and a key without the container to start is somebody's own Sonarr.
func TestConfigured(t *testing.T) {
	for _, name := range []string{"SONARR_SERVER", "SONARR_TOKEN", "SONARR_TEST_CONTAINER"} {
		t.Setenv(name, "")
	}
	if Configured() {
		t.Error("configured with nothing set")
	}

	t.Setenv("SONARR_SERVER", "http://127.0.0.1:18989")
	t.Setenv("SONARR_TOKEN", "k")
	if Configured() {
		t.Error("configured without a container to start")
	}

	t.Setenv("SONARR_TEST_CONTAINER", "sonarr-mcp-test")
	if !Configured() || Server() != "http://127.0.0.1:18989" || Token() != "k" {
		t.Errorf("configured = %v, server %q, token %q", Configured(), Server(), Token())
	}
}

// The container reaches this process by the name the script gave it, and the
// library is under the directory the script laid out.
func TestHostAndDataDir(t *testing.T) {
	t.Setenv("SONARR_TEST_HOST", "")
	t.Setenv("SONARR_TEST_DATA", "")
	if Host() != "host.docker.internal" || DataDir("tv") != "" {
		t.Errorf("with nothing set: host %q, data %q", Host(), DataDir("tv"))
	}

	t.Setenv("SONARR_TEST_HOST", "172.17.0.1")
	t.Setenv("SONARR_TEST_DATA", "/data")
	if Host() != "172.17.0.1" || DataDir("tv", "Firefly") != "/data/tv/Firefly" {
		t.Errorf("host %q, data %q", Host(), DataDir("tv", "Firefly"))
	}
}

// A fake listens where the script told the container to look, the suite's
// default when it named nothing, and a port that is no number is said.
func TestPortFrom(t *testing.T) {
	t.Setenv("SONARR_TEST_INDEXER_PORT", "")
	if n, err := portFrom("TEST_INDEXER_PORT", 18081); err != nil || n != 18081 {
		t.Errorf("unset = %d, %v", n, err)
	}

	t.Setenv("SONARR_TEST_INDEXER_PORT", "19000")
	if n, err := portFrom("TEST_INDEXER_PORT", 18081); err != nil || n != 19000 {
		t.Errorf("set = %d, %v", n, err)
	}

	t.Setenv("SONARR_TEST_INDEXER_PORT", "nope")
	if _, err := portFrom("TEST_INDEXER_PORT", 18081); err == nil || !strings.Contains(err.Error(), "SONARR_TEST_INDEXER_PORT") {
		t.Errorf("a port that is no number = %v", err)
	}
}

// A run's made-up release details can be had again by naming its seed, and a
// seed that is no number is said rather than taken for none.
func TestSeedFromEnv(t *testing.T) {
	t.Setenv("SONARR_TEST_SEED", "")
	if n, err := seedFromEnv(); err != nil || n != 0 {
		t.Errorf("unset = %d, %v", n, err)
	}

	t.Setenv("SONARR_TEST_SEED", "8675309")
	if n, err := seedFromEnv(); err != nil || n != 8675309 {
		t.Errorf("set = %d, %v", n, err)
	}

	t.Setenv("SONARR_TEST_SEED", "soon")
	if _, err := seedFromEnv(); err == nil || !strings.Contains(err.Error(), "SONARR_TEST_SEED") {
		t.Errorf("a seed that is no number = %v", err)
	}
}

// Stopping fakes that never started is harmless, so a suite's clean-up can
// run after any failure.
func TestStopNothing(t *testing.T) {
	t.Parallel()

	var none *Fakes
	none.Stop()
	(&Fakes{}).Stop()
}

func TestWaitHTTP(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()

	if err := WaitHTTP(srv.URL); err != nil {
		t.Errorf("a server that answers = %v", err)
	}
}

// A record that came back is handed over as it is, and an answer with none stops the run rather than go on empty.
func TestAnswered(t *testing.T) {
	t.Parallel()

	if got := Answered(new(7)); got != 7 {
		t.Errorf("a record that came back = %d", got)
	}

	defer func() {
		if recover() == nil {
			t.Error("an answer with no record went on")
		}
	}()
	Answered[int](nil)
}
