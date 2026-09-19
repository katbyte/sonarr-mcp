// Package cli implements the sonarr-mcp command line interface: the cobra commands, flag and
// config handling, and the MCP server exposing Sonarr's library, download and audit tools to
// AI clients.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// humanBytes renders a byte count the way a person reads disk space.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// firstSentence trims a tool description to its opening sentence, so the list
// stays one line per tool.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}

	return s
}

func ValidateParams(params []string) func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, _ []string) error {
		for _, p := range params {
			if viper.GetString(p) != "" {
				continue
			}
			return errors.New(p + " parameter can't be empty")
		}

		return nil
	}
}

// connectionParams are the flags every command that talks to Sonarr needs.
var connectionParams = []string{"server", "token"}

// toolsetOrder is how `sonarr-mcp tools` lists the sets: the base first,
// then the job most sessions are for.
var toolsetOrder = []string{"core", "curation", "library", "admin"}

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "sonarr-mcp [command]",
		Short: "sonarr-mcp is an MCP server and CLI that audits a Sonarr library and fixes what it finds",
		Long: `An MCP server (and CLI) for Sonarr: find what is wrong with a TV library - episodes
missing or below cutoff, downloads stuck or failing, files and folders Sonarr has lost track
of, profiles and settings that do nothing - and fix it, from an AI client such as Claude Code.
Complete documentation is available at https://github.com/katbyte/sonarr-mcp`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \"sonarr-mcp help\" for more information about available sonarr-mcp commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of sonarr-mcp",
		Long:          `Print the version number of sonarr-mcp`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sonarr-mcp "+version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "tools",
		Short: "List the tools and toolsets, and what the current flags would register",
		Long: `Lists every tool sonarr-mcp would register with the current flags, grouped by toolset,
with its kind (read, write or delete) and what it does.

Needs no server: it reports what would be registered, not what a server accepts.

  sonarr-mcp tools                        # the default set (core)
  sonarr-mcp tools --toolsets all         # every tool
  sonarr-mcp tools --toolsets curation    # audits and the tools that fix what they find
  sonarr-mcp tools --read-only            # only the tools that never change state
  sonarr-mcp tools -q                     # names only`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()
			quiet, _ := cmd.Flags().GetBool("quiet")

			f := GetFlags()
			list, err := tools.Describe(f.ToolOptions())
			if err != nil {
				return err
			}

			if quiet {
				for _, t := range list {
					_, _ = fmt.Fprintln(out, t.Name)
				}
				return nil
			}

			byset := map[string][]tools.ToolInfo{}
			for _, t := range list {
				byset[t.Toolset] = append(byset[t.Toolset], t)
			}
			counts := map[string]int{}
			for _, t := range list {
				counts[t.Kind]++
			}

			for _, set := range toolsetOrder {
				in := byset[set]
				if len(in) == 0 {
					continue
				}
				_, _ = fmt.Fprintf(out, "\n%s (%d)\n", set, len(in))
				for _, t := range in {
					_, _ = fmt.Fprintf(out, "  %-32s %-6s %s\n", t.Name, t.Kind, firstSentence(t.Description))
				}
			}
			_, _ = fmt.Fprintf(out, "\n%d tools: %d read, %d write, %d delete\n",
				len(list), counts["read"], counts["write"], counts["delete"])
			if !f.EnableDelete {
				_, _ = fmt.Fprintln(out, "delete tools are hidden; --enable-delete registers them")
			}
			_, _ = fmt.Fprintf(out, "\ntoolsets: all, %s\n", strings.Join(tools.ToolsetNames(), ", "))
			_, _ = fmt.Fprintf(out, "families: %s\n", strings.Join(tools.FamilyNames(), ", "))
			_, _ = fmt.Fprintf(out, "select with --toolsets / SONARR_TOOLSETS; core is always included. "+
				"Default is %s - use --toolsets all for every tool.\n", strings.Join(DefaultToolsets, ","))

			return nil
		},
	})
	if c, _, err := root.Find([]string{"tools"}); err == nil {
		c.Flags().BoolP("quiet", "q", false, "print tool names only")
	}

	root.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Check connectivity and print Sonarr's version and root folders",
		Long:          `Connects to the configured Sonarr and prints its name, version and operating system, how many series it holds, and its root folders with their free space.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()

			client, err := GetFlags().NewClient()
			if err != nil {
				return err
			}

			status, err := client.GetSystemStatus(cmd.Context())
			if err != nil {
				return err
			}
			series, err := client.GetSeries(cmd.Context(), sonarr.GetSeriesOperationOptions{})
			if err != nil {
				return err
			}
			folders, err := client.GetRootFolder(cmd.Context())
			if err != nil {
				return err
			}

			s := status.Model
			_, _ = fmt.Fprintf(out, "%s %s on %s at %s - %d series\n", s.InstanceName, s.Version, s.OsName, client.Client.BaseURL, len(series.Model))
			for _, f := range folders.Model {
				_, _ = fmt.Fprintf(out, "  root folder %s - %s free, %d folders not in Sonarr\n", f.Path, humanBytes(f.FreeSpace), len(f.UnmappedFolders))
			}
			return nil
		},
	})

	root.AddCommand(serveCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}
