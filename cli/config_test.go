package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const (
	testServer  = "http://nas:8989"
	testTool    = "series_get"
	denyDeletes = "*_delete"
	testBearer  = "bearer"
)

// load drives the real flag wiring against a temporary home and working
// directory.
func load(t *testing.T, home, wd string) *FlagData {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", home)
	t.Chdir(wd)

	if err := configureFlags(&cobra.Command{Use: "sonarr-mcp"}); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}

	return GetFlags()
}

// configure drives the flag wiring as load does, and returns what it said of the config file.
func configure(t *testing.T, home, wd string) error {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", home)
	t.Chdir(wd)

	return configureFlags(&cobra.Command{Use: "sonarr-mcp"})
}

func write(t *testing.T, dir, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, ".sonarr-mcp"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// on is how a boolean is switched on from the environment.
const on = "true"

// A .sonarr-mcp in the working directory is documented as per-project
// settings, which it only is if it is found before the one in $HOME. viper
// reads the first file it finds.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestConfigFilePrecedence(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()

	t.Run("project wins over home", func(t *testing.T) {
		write(t, home, "SERVER=http://from-home\n")
		write(t, project, "SERVER=http://from-project\n")
		if got := load(t, home, project).Server; got != "http://from-project" {
			t.Errorf("server = %q, want the project config", got)
		}
	})

	t.Run("home is used when there is no project config", func(t *testing.T) {
		empty := t.TempDir()
		if got := load(t, home, empty).Server; got != "http://from-home" {
			t.Errorf("server = %q, want the home config", got)
		}
	})

	t.Run("no config at all is not an error", func(t *testing.T) {
		if got := load(t, t.TempDir(), t.TempDir()).Server; got != "" {
			t.Errorf("server = %q, want empty", got)
		}
	})
}

// The environment beats a config file, so a container can override what is
// baked into an image.
func TestEnvironmentOverridesConfigFile(t *testing.T) {
	home := t.TempDir()
	write(t, home, "SERVER=http://from-home\nTOKEN=from-home\n")

	t.Setenv("SONARR_SERVER", "http://from-env")
	f := load(t, home, t.TempDir())

	if f.Server != "http://from-env" {
		t.Errorf("server = %q, want the environment", f.Server)
	}
	// and a key the environment did not set still comes from the file
	if f.Token != "from-home" {
		t.Errorf("token = %q, want the config file", f.Token)
	}
}

// A flag with a - in its name is written with a _ in a config file, since an env file takes no -, and reaches its flag like any other.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestConfigFileTakesEveryFlag(t *testing.T) {
	home := t.TempDir()
	write(t, home, "SERVER=http://from-home\nREAD_ONLY=true\nENABLE_DELETE=true\nALLOW_TOOLS=series_get\nDENY_TOOLS=*_delete\nAUTH_TOKEN=bearer\nALLOW_NO_AUTH=true\n")

	f := load(t, home, t.TempDir())
	if f.Server != "http://from-home" || !f.ReadOnly || !f.EnableDelete || !slices.Equal(f.AllowTools, []string{testTool}) || !slices.Equal(f.DenyTools, []string{denyDeletes}) || f.AuthToken != testBearer || !f.AllowNoAuth {
		t.Errorf("flags from the file = %+v, want every one of them set", f)
	}
}

// A name a config file does not take did nothing and said nothing, so a file written with the environment's names (SONARR_SERVER=) left the
// server unset with no word why. Each such name is an error that says what to write instead.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestConfigFileNamesWhatItDoesNotTake(t *testing.T) {
	for body, want := range map[string]string{ //nolint:gosec // the names of settings in a file, not credentials
		"SONARR_SERVER=http://nas:8989\nSONARR_TOKEN=t\n": "SONARR_SERVER is the environment's name for --server; in this file it is SERVER; SONARR_TOKEN is the environment's name for --token; in this file it is TOKEN",
		"SONARR_URL=http://nas:8989\n":                    "SONARR_URL is the environment's name for --server; in this file it is SERVER",
		"SERVER=http://nas:8989\nSERVR=http://nas\n":      "SERVR is not a setting; this file takes ALLOW_NO_AUTH, ALLOW_TOOLS, AUTH_TOKEN, DENY_TOOLS, ENABLE_DELETE, LISTEN, READ_ONLY, SERVER, TOKEN, TOOLSETS",
	} {
		home := t.TempDir()
		write(t, home, body)
		if err := configure(t, home, t.TempDir()); err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
			t.Errorf("a file of %q = %v, want it to say %q", body, err, want)
		}
	}
}

// Every documented SONARR_* variable has to actually reach its field; a
// typo in the binding map is invisible until someone sets the variable and
// nothing happens.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestEnvironmentBindings(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []struct {
		key, value string
		check      func(*FlagData) bool
	}{
		{"SONARR_SERVER", testServer, func(f *FlagData) bool { return f.Server == testServer }},
		{"SONARR_TOKEN", "tok", func(f *FlagData) bool { return f.Token == "tok" }},
		{"SONARR_READ_ONLY", on, func(f *FlagData) bool { return f.ReadOnly }},
		{"SONARR_ENABLE_DELETE", on, func(f *FlagData) bool { return f.EnableDelete }},
		{"SONARR_LISTEN", ":8080", func(f *FlagData) bool { return f.Listen == ":8080" }},
		{"SONARR_AUTH_TOKEN", testBearer, func(f *FlagData) bool { return f.AuthToken == testBearer }},
		{"SONARR_ALLOW_NO_AUTH", on, func(f *FlagData) bool { return f.AllowNoAuth }},
		{"SONARR_TOOLSETS", "curation", func(f *FlagData) bool { return len(f.Toolsets) == 1 && f.Toolsets[0] == "curation" }},
		{"SONARR_ALLOW_TOOLS", testTool, func(f *FlagData) bool { return len(f.AllowTools) == 1 && f.AllowTools[0] == testTool }},
		{"SONARR_DENY_TOOLS", denyDeletes, func(f *FlagData) bool { return len(f.DenyTools) == 1 && f.DenyTools[0] == denyDeletes }},
		// the names the rest of the Sonarr tooling uses reach the same fields
		{"SONARR_URL", testServer, func(f *FlagData) bool { return f.Server == testServer }},
		{"SONARR_API_KEY", "key", func(f *FlagData) bool { return f.Token == "key" }},
	} {
		t.Setenv(env.key, env.value)
		if f := load(t, dir, dir); !env.check(f) {
			t.Errorf("%s=%s did not reach its field: %+v", env.key, env.value, f)
		}
		_ = os.Unsetenv(env.key)
	}
}

// With both set, this family's own name wins over the common one, so a shell
// exporting SONARR_URL for other tools cannot redirect a SONARR_SERVER that
// was set on purpose.
func TestOwnNamesWinOverAliases(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONARR_SERVER", "http://own")
	t.Setenv("SONARR_URL", "http://alias")
	t.Setenv("SONARR_TOKEN", "own")
	t.Setenv("SONARR_API_KEY", "alias")

	f := load(t, dir, dir)
	if f.Server != "http://own" || f.Token != "own" {
		t.Errorf("server %q token %q, want the SONARR_SERVER and SONARR_TOKEN values", f.Server, f.Token)
	}
}

// NewClient is where a missing server or API key is caught, before anything
// tries to talk to Sonarr.
func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	if _, err := (&FlagData{}).NewClient(); err == nil {
		t.Error("a client with no server should be refused")
	}
	if _, err := (&FlagData{Server: testServer}).NewClient(); err == nil {
		t.Error("a client with no API key should be refused")
	}
	if _, err := (&FlagData{Server: "nas:8989", Token: "t"}).NewClient(); err == nil {
		t.Error("a server with no scheme should be refused")
	}
	if _, err := (&FlagData{Server: testServer, Token: "t"}).NewClient(); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}
