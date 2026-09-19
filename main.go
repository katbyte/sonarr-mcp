// Package main implements sonarr-mcp, an MCP server and CLI that audits a Sonarr library and fixes what it finds.
package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/sonarr-mcp/cli"
)

func main() {
	// the log level comes from SONARR_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("SONARR_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>sonarr-mcp: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>sonarr-mcp:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
