// Command pandorest generates the Emby and Jellyfin SDKs (lib/emby, lib/jf)
// from their vendored OpenAPI documents. It is inspired by Pandora, the
// go-azure-sdk generator (https://github.com/hashicorp/pandora):
//
//	import    spec -> workarounds -> api-definitions/<service>/*.json
//	generate  api-definitions/<service> -> lib/<package>
//	diff      what a refreshed spec changes, against the checked-in definitions
//	check     the definitions match the spec and the package has every method
//
// Run from the repository root (the make targets do):
//
//	go run ./internal/pandorest import
//	go run ./internal/pandorest generate -service jellyfin
//	go run ./internal/pandorest diff
//	go run ./internal/pandorest diff -old /tmp/old-definitions/emby -new api-definitions/emby
//
// See internal/pandorest/README.md for the design and how to refresh a spec.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/config"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/differ"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/generator"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/importer"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pandorest:", err)
		os.Exit(1)
	}
}

const usage = `usage: pandorest <import|generate|diff|check> [flags]

  import    read the specs into api-definitions/, applying the workarounds
  generate  write the SDK packages from api-definitions/
  diff      report what the specs change against the checked-in definitions
  check     verify the definitions match the specs and the packages have every method

run "pandorest <command> -h" for a command's flags`

// errChanges is returned by diff -exit-code when there are changes.
var errChanges = errors.New("the definitions differ")

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]

	fs := flag.NewFlagSet("pandorest "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	services := fs.String("service", "", "comma-separated services to process (default all: emby, jellyfin)")
	root := fs.String("root", ".", "repository root the config paths are relative to")
	quiet := fs.Bool("quiet", false, "do not log each workaround and warning")
	var oldDir, newDir *string
	var exitCode *bool
	if cmd == "diff" {
		oldDir = fs.String("old", "", "definitions directory to compare from (default: the checked-in definitions)")
		newDir = fs.String("new", "", "definitions directory to compare to (default: a fresh import of the spec)")
		exitCode = fs.Bool("exit-code", false, "exit 1 when there are changes")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	log := func(msg string) { _, _ = fmt.Fprintln(stderr, "pandorest: "+msg) }
	if *quiet {
		log = func(string) {}
	}

	if cmd == "diff" && (*oldDir != "") != (*newDir != "") {
		return errors.New("diff: pass both -old and -new, or neither")
	}
	if cmd == "diff" && *oldDir != "" {
		return diffDirs(*oldDir, *newDir, *exitCode, stdout)
	}

	selected, err := config.Select(*services)
	if err != nil {
		return err
	}
	changed := false
	for _, svc := range selected {
		svc = svc.In(*root)
		switch cmd {
		case "import":
			err = importService(svc, log, stdout)
		case "generate":
			err = generateService(svc, stdout)
		case "diff":
			var differs bool
			differs, err = diffService(svc, log, stdout)
			changed = changed || differs
		case "check":
			err = checkService(svc, log, stdout)
		default:
			return fmt.Errorf("unknown command %q\n%s", cmd, usage)
		}
		if err != nil {
			return err
		}
	}
	if cmd == "diff" && changed && *exitCode {
		return errChanges
	}

	return nil
}

func importService(svc config.Service, log func(string), stdout io.Writer) error {
	defs, err := importer.Import(svc, log)
	if err != nil {
		return err
	}
	if err := definitions.Save(defs, svc.Path(svc.Definitions)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s: %d operations, %d models, %d constants, %d workarounds -> %s\n",
		svc.Name, len(defs.Operations()), len(defs.Models()), len(defs.Constants()), len(defs.Workarounds), svc.Path(svc.Definitions))

	return nil
}

func generateService(svc config.Service, stdout io.Writer) error {
	defs, err := definitions.Load(svc.Path(svc.Definitions))
	if err != nil {
		return err
	}
	n, err := generator.Generate(defs, generator.Options{Dir: svc.Path(svc.Output), Definitions: svc.Definitions})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s: %d files -> %s\n", svc.Name, n, svc.Path(svc.Output))

	return nil
}

func diffService(svc config.Service, log func(string), stdout io.Writer) (bool, error) {
	checkedIn, err := definitions.Load(svc.Path(svc.Definitions))
	if err != nil {
		return false, err
	}
	fresh, err := importer.Import(svc, log)
	if err != nil {
		return false, err
	}
	report := differ.Diff(checkedIn, fresh)
	_, _ = fmt.Fprint(stdout, report.String())

	return !report.Empty(), nil
}

func diffDirs(oldDir, newDir string, exitCode bool, stdout io.Writer) error {
	older, err := definitions.Load(oldDir)
	if err != nil {
		return err
	}
	newer, err := definitions.Load(newDir)
	if err != nil {
		return err
	}
	report := differ.Diff(older, newer)
	_, _ = fmt.Fprint(stdout, report.String())
	if exitCode && !report.Empty() {
		return errChanges
	}

	return nil
}

func checkService(svc config.Service, log func(string), stdout io.Writer) error {
	checkedIn, err := definitions.Load(svc.Path(svc.Definitions))
	if err != nil {
		return err
	}
	fresh, err := importer.Import(svc, log)
	if err != nil {
		return err
	}
	if report := differ.Diff(checkedIn, fresh); !report.Empty() {
		return fmt.Errorf("%s: %s no longer matches %s; run make generate:\n%s", svc.Name, svc.Definitions, svc.Spec, report.String())
	}
	n, err := generator.Check(checkedIn, svc.Path(svc.Output))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%s: %d operations, every one has a method in %s\n", svc.Name, n, svc.Path(svc.Output))

	return nil
}
