package cli

import (
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/mcp/server"
	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (stdio by default, HTTP with --listen)",
		Long: `Runs the MCP server for AI clients such as Claude Code.

Without --listen it speaks MCP over stdio: register it in .mcp.json with the SONARR_*
environment variables set. With --listen (SONARR_LISTEN, e.g. :8080) it serves the
Streamable HTTP transport at /mcp instead, for an always-on deployment such as the
docker-compose.yml in this repo. --auth-token (SONARR_AUTH_TOKEN) is then required, and
clients must send "Authorization: Bearer <token>"; to serve with no token at all, which
lets anyone who can reach the port use every tool, say so with --allow-no-auth
(SONARR_ALLOW_NO_AUTH=true).`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			client, err := f.NewClient()
			if err != nil {
				return err
			}

			// what a client is told when it connects: go-kt's words for every
			// server, then this one's rules for the tools it is given
			own, err := tools.Instructions(f.ToolOptions())
			if err != nil {
				return err
			}

			srv := mcp.NewServer(&mcp.Implementation{
				Name:    "sonarr",
				Title:   "Sonarr Library Auditor",
				Version: version.Version,
			}, &mcp.ServerOptions{Instructions: server.Instructions(own...)})

			registered, err := tools.RegisterAll(srv, client, f.ToolOptions())
			if err != nil {
				return err
			}
			clog.Log.Infof("registered %d tools", len(registered))

			return server.Run(cmd.Context(), srv, f.serveOptions())
		},
	}
}

// serveOptions is how this tool serves: stdio, or HTTP at --listen behind
// --auth-token, with the health probe a container asks outside the check.
func (f *FlagData) serveOptions() server.Options {
	return server.Options{
		Listen:      f.Listen,
		AuthToken:   f.AuthToken,
		AllowNoAuth: f.AllowNoAuth,
		Name:        "sonarr-mcp",
		EnvPrefix:   "SONARR",
	}
}
