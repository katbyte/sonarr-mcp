package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel.
const coreTool = "series_get"

// run executes the command tree the binary builds and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	// a developer's own ~/.sonarr-mcp must not leak into the run: with a
	// real server configured, info would connect to it
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	root, err := Make()
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err = root.Execute()

	return out.String(), err
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestMakeBuildsTheCommands(t *testing.T) {
	out, err := run(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"serve", "info", "version", "tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from --help:\n%s", want, out)
		}
	}

	out, err = run(t)
	if err != nil || !strings.Contains(out, "sonarr-mcp help") {
		t.Errorf("bare invocation = %q, %v", out, err)
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	if err != nil || !strings.HasPrefix(out, "sonarr-mcp ") {
		t.Errorf("version = %q, %v", out, err)
	}
}

// A command that talks to the server refuses to start without a server and
// token, before any connection is attempted.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestInfoNeedsConnectionParams(t *testing.T) {
	_, err := run(t, "info")
	if err == nil || !strings.Contains(err.Error(), "server") {
		t.Errorf("info with nothing configured = %v, want a complaint about server", err)
	}
	_, err = run(t, "serve", "--server", "http://nas:8989")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("serve with no token = %v, want a complaint about token", err)
	}
}

// `sonarr-mcp tools` needs no server, so it has to work with nothing
// configured - which is also what makes it the one command a test can drive
// end to end.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommand(t *testing.T) {
	out, err := run(t, "tools")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}

	// the default is core, so those are listed and nothing else is
	for _, want := range []string{"core", "server_info", "series_list", coreTool} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from `tools`:\n%s", want, out)
		}
	}
	if strings.Contains(out, "audit_missing_episodes") {
		t.Error("an audit tool is listed by default; the default is core")
	}
	// and it says how to get more
	for _, want := range []string{"toolsets:", "families:", "--toolsets all", "--enable-delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from the footer:\n%s", want, out)
		}
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommandToolsets(t *testing.T) {
	out, err := run(t, "tools", "--toolsets", "curation", "-q")
	if err != nil {
		t.Fatalf("tools --toolsets curation: %v", err)
	}
	lines := strings.Fields(out)
	if len(lines) < 20 {
		t.Errorf("curation listed %d tools, want the whole set", len(lines))
	}
	if !strings.Contains(out, "audit_missing_episodes") {
		t.Error("audit_missing_episodes missing from curation")
	}
	if !strings.Contains(out, coreTool) {
		t.Error("core was not included alongside curation")
	}

	// -q prints names only, so nothing should carry a description
	if strings.Contains(out, "read ") || strings.Contains(out, "Monitored ") {
		t.Errorf("-q printed more than names:\n%s", out)
	}

	out, err = run(t, "tools", "--toolsets", "all", "--enable-delete")
	if err != nil {
		t.Fatalf("tools --toolsets all: %v", err)
	}
	if !strings.Contains(out, "series_delete") || strings.Contains(out, "delete tools are hidden") {
		t.Errorf("--enable-delete did not list the delete tools:\n%s", out)
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestToolsCommandRejectsAnUnknownToolset(t *testing.T) {
	_, err := run(t, "tools", "--toolsets", "nope")
	if err == nil {
		t.Fatal("an unknown toolset should fail the command")
	}
	if !strings.Contains(err.Error(), "curation") {
		t.Errorf("the error should name the valid sets: %v", err)
	}
}

func TestFirstSentence(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ in, want string }{
		{"One thing. And then another.", "One thing."},
		{"No full stop at all", "No full stop at all"},
		{"Trailing full stop.", "Trailing full stop."},
		{"", ""},
	} {
		if got := firstSentence(c.in); got != c.want {
			t.Errorf("firstSentence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
