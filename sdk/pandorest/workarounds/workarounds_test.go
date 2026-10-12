package workarounds_test

import (
	"strings"
	"testing"

	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/importer"
	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
	"github.com/katbyte/sonarr-mcp/sdk/pandorest/workarounds"
)

// root is the repository root from this package, which is where the service's
// paths are relative to.
const root = "../../.."

// load reads a fresh copy of a service's vendored document, the highest
// version there is.
func load(service string) (*openapi.Spec, error) {
	svc, _ := config.Find(workarounds.Services, service)
	svc, err := svc.In(root).Resolve()
	if err != nil {
		return nil, err
	}

	return openapi.Load(svc.Path(svc.Spec))
}

// Every workaround fixes a bug the vendored document has, and would notice it
// gone: one a refreshed document no longer needs fails the import and names
// itself, rather than doing nothing for ever.
func TestWorkarounds(t *testing.T) {
	t.Parallel()

	if err := pandorest.Verify(workarounds.All, load); err != nil {
		t.Fatal(err)
	}
}

func TestServices(t *testing.T) {
	t.Parallel()

	svc, ok := config.Find(workarounds.Services, workarounds.Service)
	if !ok {
		t.Fatalf("no service named %s", workarounds.Service)
	}
	svc, err := svc.In(root).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if svc.Version == "" || svc.Spec == "" || svc.Definitions == "" {
		t.Errorf("the service resolved to %+v: a document and where its definitions go", svc)
	}
	// the document is named by the Sonarr release it was taken from, not by
	// the version it gives itself, which is 3.0.0 for every release
	if strings.Count(svc.Version, ".") != 3 {
		t.Errorf("the document's version is %q, want a Sonarr release such as 4.0.20.3014", svc.Version)
	}

	for _, w := range workarounds.All {
		if w.Service() != workarounds.Service {
			t.Errorf("%s is for %s, which is no service here", w.Name(), w.Service())
		}
	}
}

// Every kind of id reaches a place in the document, no two kinds claim one, and every exception named is still there.
func TestIDs(t *testing.T) {
	t.Parallel()

	svc, _ := config.Find(workarounds.Services, workarounds.Service)
	svc, err := svc.In(root).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := load(workarounds.Service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pandorest.Apply(workarounds.All, workarounds.Service, spec, nil); err != nil {
		t.Fatal(err)
	}
	if err := importer.VerifyIDs(svc, spec); err != nil {
		t.Fatal(err)
	}
}
