package workarounds

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/config"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/openapi"
)

// loadSpec reads a service's vendored document.
func loadSpec(t *testing.T, service string) *openapi.Spec {
	t.Helper()

	cfg, ok := config.Find(service)
	if !ok {
		t.Fatalf("no service %q", service)
	}
	// tests run in the package directory, four below the repository root
	spec, err := openapi.Load(filepath.Join("..", "..", "..", "..", cfg.Spec))
	if err != nil {
		t.Fatal(err)
	}

	return spec
}

// Every workaround fixes a bug that is in its vendored document, and fails
// once the bug is gone. Applying a workaround to the document it already
// fixed is the cheapest stand-in for a fixed upstream spec.
func TestWorkaroundsApplyOnceThenFail(t *testing.T) {
	t.Parallel()

	for _, w := range All {
		t.Run(w.Name(), func(t *testing.T) {
			t.Parallel()

			if _, ok := config.Find(w.Service()); !ok {
				t.Fatalf("service %q is not configured", w.Service())
			}
			if w.Bug() == "" || !strings.HasPrefix(w.Name(), w.Service()+"-") {
				t.Errorf("name %q or bug %q does not describe the workaround", w.Name(), w.Bug())
			}
			spec := loadSpec(t, w.Service())
			if err := w.Apply(spec); err != nil {
				t.Fatalf("the bug is not in the vendored document: %v", err)
			}
			if err := w.Apply(spec); err == nil {
				t.Error("applying it again succeeded, so it would not notice the bug being fixed")
			}
		})
	}
}

func TestApply(t *testing.T) {
	t.Parallel()

	spec := loadSpec(t, "sonarr")
	var logged []string
	applied, err := Apply("sonarr", spec, func(s string) { logged = append(logged, s) })
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, w := range All {
		if w.Service() == "sonarr" {
			want = append(want, w.Name())
		}
	}
	if !slices.Equal(applied, want) || len(logged) != len(want) {
		t.Errorf("applied %v (logged %v), want %v", applied, logged, want)
	}

	// a second pass over the patched document fails and names the first workaround
	_, err = Apply("sonarr", spec, nil)
	if err == nil || !strings.Contains(err.Error(), "workaround "+want[0]+" no longer applies, so remove it") {
		t.Errorf("second Apply = %v", err)
	}

	if applied, err := Apply("nothing", spec, nil); err != nil || len(applied) != 0 {
		t.Errorf("Apply for a service without workarounds = %v, %v", applied, err)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, w := range All {
		if seen[w.Name()] {
			t.Errorf("two workarounds are named %s", w.Name())
		}
		seen[w.Name()] = true
	}
}
