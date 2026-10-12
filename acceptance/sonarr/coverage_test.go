//go:build acceptance

package sonarr_test

// Operation coverage: the client records every request the suite makes, and a whole run checks each of the SDK's operations was made at least once.
// The generated tests prove each method builds its request; only a call against Sonarr proves the request is one Sonarr accepts and the answer one
// the method decodes, so an operation nothing calls is an operation nothing has proved.

import (
	"encoding/json"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/sweep"
	"github.com/katbyte/sonarr-mcp/sdk/pandorest/workarounds"
)

// notCalled are the operations the suite leaves alone, and why. A name here
// that the suite calls after all fails the run, so the list cannot go stale.
var notCalled = map[string]string{
	"PostSystemRestart":             "restarts Sonarr under the rest of the suite",
	"PostSystemShutdown":            "stops Sonarr, and the container with it",
	"PostSystemBackupRestoreById":   "restores a backup over the running Sonarr, which then restarts",
	"PostSystemBackupRestoreUpload": "the same restore, from an uploaded zip",
}

// calls is every request the suite's client sent, for the check at the end of a whole run.
var calls client.Calls

// loadDefinitions reads the definitions the SDK was generated from: the ones
// beside the highest-versioned document under api-defs.
func loadDefinitions() (*definitions.Service, error) {
	svc, _ := config.Find(workarounds.Services, workarounds.Service)
	svc, err := svc.In("../..").Resolve()
	if err != nil {
		return nil, err
	}

	return definitions.Load(svc.Path(svc.Definitions))
}

// uncovered is what a whole run left wrong, and how many operations there are: each operation nothing called that notCalled does not excuse, and
// each it excuses that was called after all.
func uncovered() (problems []string, total int, err error) {
	defs, err := loadDefinitions()
	if err != nil {
		return nil, 0, err
	}

	return sweep.NeverCalled(defs, calls.Seen(), notCalled), len(defs.Operations()), nil
}

// jsonRaw renders a value as the raw JSON an untyped body takes.
func jsonRaw(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	return b, nil
}
