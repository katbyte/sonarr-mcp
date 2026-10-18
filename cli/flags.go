package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type FlagData struct {
	Server       string   `mapstructure:"server"`
	Token        string   `mapstructure:"token"`
	ReadOnly     bool     `mapstructure:"read-only"`
	EnableDelete bool     `mapstructure:"enable-delete"`
	Toolsets     []string `mapstructure:"toolsets"`
	AllowTools   []string `mapstructure:"allow-tools"`
	DenyTools    []string `mapstructure:"deny-tools"`
	Listen       string   `mapstructure:"listen"`
	AuthToken    string   `mapstructure:"auth-token"`
	AllowNoAuth  bool     `mapstructure:"allow-no-auth"`
}

// envNames maps each flag to the environment variables that set it, first
// match winning. SONARR_SERVER and SONARR_TOKEN follow the other servers in
// this family (ABS_*, EMBYFIN_*); SONARR_URL and SONARR_API_KEY are what most
// other Sonarr tooling reads, so a shell that already exports them works.
var envNames = map[string][]string{
	"server":        {"SONARR_SERVER", "SONARR_URL"},
	"token":         {"SONARR_TOKEN", "SONARR_API_KEY"},
	"read-only":     {"SONARR_READ_ONLY"},
	"enable-delete": {"SONARR_ENABLE_DELETE"},
	"toolsets":      {"SONARR_TOOLSETS"},
	"allow-tools":   {"SONARR_ALLOW_TOOLS"},
	"deny-tools":    {"SONARR_DENY_TOOLS"},
	"listen":        {"SONARR_LISTEN"},
	"auth-token":    {"SONARR_AUTH_TOKEN"},
	"allow-no-auth": {"SONARR_ALLOW_NO_AUTH"},
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("server", "s", "", "Sonarr's url, with its URL base if it has one, e.g. http://nas:8989")
	pflags.StringP("token", "t", "", "the Sonarr API key, from Settings → General → Security (consider exporting to SONARR_TOKEN instead)")
	pflags.Bool("read-only", false, "register only tools that never change Sonarr's state")
	pflags.Bool("enable-delete", false, "register the 15 tools that delete: series and episode files from disk, tags, backups, and settings such as indexers and download clients. Each still changes nothing without confirm")
	pflags.StringSlice("toolsets", nil, "groups of tools to register: all, core (default), library, curation, admin, settings, or a resource family like series (core is always included)")
	pflags.StringSlice("allow-tools", nil, "register these tools by name: names, prefix globs like series_*, or the essential preset. On its own, only these; beside --toolsets, these as well as the sets")
	pflags.StringSlice("deny-tools", nil, "never register these tools, whatever asked for them: names or prefix globs like *_delete")
	pflags.String("listen", "", "serve MCP over HTTP on this address (e.g. :8080) instead of stdio")
	pflags.String("auth-token", "", "bearer token required on the HTTP endpoint (consider exporting to SONARR_AUTH_TOKEN instead)")
	pflags.Bool("allow-no-auth", false, "serve HTTP with no bearer token: anyone who can reach the port can use every tool")

	for name, envs := range envNames {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if err := viper.BindEnv(append([]string{name}, envs...)...); err != nil {
			return fmt.Errorf("error binding '%s' to env %v : %w", name, envs, err)
		}
	}

	viper.SetConfigName(".sonarr-mcp")
	viper.SetConfigType("env")
	// viper reads the first file it finds, so the working directory comes
	// first: a per-project .sonarr-mcp overrides the one in $HOME
	viper.AddConfigPath(".")
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home)
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			clog.Log.Errorf("Error reading config file: %v", err)
		}

		return nil
	}

	return readConfigNames(viper.ConfigFileUsed())
}

// readConfigNames holds a config file to the names it takes, the flags in capitals with _ for - (SERVER, ALLOW_TOOLS), and reads each name
// with a _ as its flag. A name it does not take would do nothing and say nothing, so each is an error saying what to write instead.
func readConfigNames(path string) error {
	file := viper.New()
	file.SetConfigFile(path)
	file.SetConfigType("env")
	if err := file.ReadInConfig(); err != nil {
		return err
	}

	var wrong []string
	for _, key := range slices.Sorted(slices.Values(file.AllKeys())) {
		flag := strings.ReplaceAll(key, "_", "-")
		if _, ok := envNames[flag]; !ok {
			wrong = append(wrong, misnamed(strings.ToUpper(key)))

			continue
		}
		if flag != key {
			viper.RegisterAlias(key, flag)
		}
	}
	if len(wrong) > 0 {
		return fmt.Errorf("%s: %s", path, strings.Join(wrong, "; "))
	}

	return nil
}

// misnamed says what is wrong with a name a config file does not take: an environment variable's name is told the file's name for it, and
// anything else what the file takes.
func misnamed(key string) string {
	flags := slices.Sorted(maps.Keys(envNames))
	for _, flag := range flags {
		if slices.Contains(envNames[flag], key) {
			return fmt.Sprintf("%s is the environment's name for --%s; in this file it is %s", key, flag, fileName(flag))
		}
	}
	names := make([]string, 0, len(flags))
	for _, flag := range flags {
		names = append(names, fileName(flag))
	}

	return fmt.Sprintf("%s is not a setting; this file takes %s", key, strings.Join(names, ", "))
}

// fileName is a flag's name in a config file: in capitals, with _ for -.
func fileName(flag string) string {
	return strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// GetFlags returns the fully populated FlagData.
// We must unmarshal from Viper instead of using globally bound pflags variables
// because pflags only parses command-line arguments. Viper merges environment
// variables (and config files) on top of the CLI flags.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// NewClient is the client for the configured Sonarr. What it sends and is
// sent goes to the log at trace (SONARR_LOG=trace), with the API key hidden.
func (f *FlagData) NewClient() (*sonarr.Client, error) {
	return sonarr.New(f.Server, f.Token, client.WithLog(clog.Log))
}

// DefaultToolsets is what the binary registers when neither --toolsets nor
// --allow-tools is given: enough to find things and read them, and nothing
// that writes. The whole surface is thousands of tokens of tool definitions
// before a question is asked, which is a poor thing to spend a client's
// context on by default. Ask for more with --toolsets, or --toolsets all for
// everything.
var DefaultToolsets = []string{"core"}

// ToolOptions maps the flags onto the tool registration options. An allow
// list with no --toolsets is the whole of what loads: it already says which
// tools, so the default set is not put beside it. Beside --toolsets it adds
// the tools it names to the sets.
func (f *FlagData) ToolOptions() tools.Options {
	sets := f.Toolsets
	if len(sets) == 0 && len(f.AllowTools) == 0 {
		sets = DefaultToolsets
	}

	return tools.Options{
		ReadOnly:     f.ReadOnly,
		EnableDelete: f.EnableDelete,
		Toolsets:     sets,
		Allow:        f.AllowTools,
		Deny:         f.DenyTools,
	}
}
