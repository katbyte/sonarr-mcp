//go:build integration

// Package integration runs the generated SDK, lib/sonarr, against a real
// Sonarr in Docker. It tests one thing: that every request the generated
// methods build is one Sonarr accepts, answered in the status the
// definitions expect, and that every answer decodes into the generated types
// with the fields actually populated. Nothing here is about the tools;
// ../acceptance covers those.
//
// Every one of the SDK's operations is called by something in the suite, or
// named in notCalled with the reason it is not: a whole run that leaves an
// operation uncalled fails, so a method added by a refreshed document is
// tested or explained, never quietly untested.
//
// The bespoke tests prove what callers rely on field by field: each create
// answers 201 and each update 202, a round trip keeps what was written, the
// downloads move through the queue into the library or the blocklist. The
// read sweep (sweep_test.go) then calls every GET in the definitions and
// decodes its answer, so a GET a refreshed document adds is tested with
// nothing to write.
//
// The suite seeds its own library through the SDK - the root folder, four
// series imported from the folders scripts/testenv.sh lays out, one added
// with no files - and runs the fake indexer and download client
// (internal/fakes) that the searches, grabs, imports and failures go
// through. Sonarr's calls to SkyHook, services.sonarr.tv, TheXEM and the
// artwork CDN go through the record/replay proxy (lib/providerproxy), so a
// run needs no network; SONARR_TEST_RECORD=1 records them instead (make
// record). The tests put back what they change, so the suite can run again
// against a container that is still up.
//
//	make testacc-integration   # a container of its own, created, run, torn down
//
// or by hand, on the suite's own container, data and ports, so it can run
// beside the tool suite's:
//
//	export SONARR_TEST_CONTAINER=sonarr-mcp-integration SONARR_TEST_DATA=$HOME/.cache/sonarr-mcp/testenv/integration \
//	    SONARR_TEST_PORT=19089 SONARR_TEST_PROXY_PORT=18180 SONARR_TEST_INDEXER_PORT=18181 SONARR_TEST_SAB_PORT=18182
//	eval "$(scripts/testenv.sh up)"
//	go test -tags integration -count=1 ./integration/...
//	scripts/testenv.sh down
package integration

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }
