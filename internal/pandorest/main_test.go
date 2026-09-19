package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/config"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/generator"
)

// repoRoot is where the config's paths resolve from.
const repoRoot = "../.."

func runCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, log bytes.Buffer
	err = run(args, &out, &log)

	return out.String(), log.String(), err
}

func TestUsage(t *testing.T) {
	t.Parallel()

	if _, _, err := runCmd(t); err == nil || !strings.Contains(err.Error(), "usage: pandorest") {
		t.Errorf("no command = %v", err)
	}
	if _, _, err := runCmd(t, "frobnicate", "-root", repoRoot); err == nil || !strings.Contains(err.Error(), `unknown command "frobnicate"`) {
		t.Errorf("unknown command = %v", err)
	}
	if _, _, err := runCmd(t, "check", "-service", "plex"); err == nil || !strings.Contains(err.Error(), `unknown service "plex"`) {
		t.Errorf("unknown service = %v", err)
	}
	if _, _, err := runCmd(t, "diff", "-old", "x"); err == nil || !strings.Contains(err.Error(), "pass both -old and -new") {
		t.Errorf("diff with only -old = %v", err)
	}
}

// The checked-in definitions match the vendored specs and the generated
// packages have every method: make apicheck, as a test.
func TestCheckRepository(t *testing.T) {
	t.Parallel()

	stdout, _, err := runCmd(t, "check", "-root", repoRoot, "-quiet")
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range config.Services {
		if !strings.Contains(stdout, svc.Name+": ") || !strings.Contains(stdout, "every one has a method in "+filepath.Join(repoRoot, svc.Output)) {
			t.Errorf("check output lacks %s:\n%s", svc.Name, stdout)
		}
	}
	stdout, _, err = runCmd(t, "diff", "-root", repoRoot, "-quiet", "-exit-code")
	if err != nil || strings.Count(stdout, ": no changes") != len(config.Services) {
		t.Errorf("diff against the specs = %v:\n%s", err, stdout)
	}
}

// import then generate in a scratch copy of the repository reproduces the
// checked-in definitions and packages byte for byte: make gencheck, as a test.
func TestImportGenerateReproduces(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, svc := range config.Services {
		src, err := os.ReadFile(filepath.Join(repoRoot, svc.Spec))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, svc.Spec)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, svc.Spec), src, 0o600); err != nil { //nolint:gosec // the config's own spec path under the test's temp dir
			t.Fatal(err)
		}
	}

	if _, stderr, err := runCmd(t, "import", "-root", root); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(stderr, "applied workaround sonarr-ui-routes") {
		t.Errorf("import did not log its workarounds:\n%s", stderr)
	}
	if _, _, err := runCmd(t, "generate", "-root", root); err != nil {
		t.Fatal(err)
	}

	for _, svc := range config.Services {
		for _, dir := range []string{svc.Definitions, svc.Output} {
			// (hand-written tests beside the generated code are not regenerated)
			want := listFiles(t, filepath.Join(repoRoot, dir), dir == svc.Output)
			got := listFiles(t, filepath.Join(root, dir), false)
			if len(got) != len(want) {
				t.Errorf("%s: %d files regenerated, %d checked in", dir, len(got), len(want))
			}
			for name, content := range want {
				if got[name] != content {
					t.Errorf("%s/%s differs from a fresh generation; run make generate", dir, name)
				}
			}
		}
	}

	// the diff between two copies of the same definitions is empty
	stdout, _, err := runCmd(t, "diff", "-old", filepath.Join(repoRoot, config.Services[0].Definitions), "-new", filepath.Join(root, config.Services[0].Definitions), "-exit-code")
	if err != nil || !strings.Contains(stdout, "no changes") {
		t.Errorf("diff -old -new = %v:\n%s", err, stdout)
	}
	if err := os.Remove(filepath.Join(root, config.Services[0].Definitions, "Tag.json")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runCmd(t, "diff", "-old", filepath.Join(repoRoot, config.Services[0].Definitions), "-new", filepath.Join(root, config.Services[0].Definitions), "-exit-code")
	if !errors.Is(err, errChanges) || !strings.Contains(stdout, "- operation PostTag (POST /api/v3/tag) [breaking]") {
		t.Errorf("diff with a group gone = %v:\n%s", err, stdout)
	}
}

// listFiles reads a directory's files; generatedOnly skips the hand-written
// ones (no generated header) that live beside generated code.
func listFiles(t *testing.T, dir string, generatedOnly bool) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a directory under test
		if err != nil {
			t.Fatal(err)
		}
		if generatedOnly && !strings.HasPrefix(string(b), generator.Header) {
			continue
		}
		out[e.Name()] = string(b)
	}

	return out
}
