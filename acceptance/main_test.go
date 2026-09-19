//go:build integration

// Package acceptance covers every tool against a real Sonarr running in
// Docker - the reads, the writes and the audits alike - so response shapes,
// query encoding, status codes and Sonarr's own behaviour are checked against
// the thing sonarr-mcp actually talks to rather than a stub.
//
// The library is built through the tools themselves (rootfolder_add,
// series_import, series_add, series_edit), so the setup is part of the
// coverage. Sonarr's calls out to SkyHook and services.sonarr.tv go through a
// record/replay proxy (lib/providerproxy), and the indexer and download client
// it searches and downloads with are fakes this suite runs (internal/fakes),
// so a run needs no network and every download is one the suite chose.
//
//	make testacc-acceptance                             # container started and torn down
//
//	eval "$(scripts/testenv.sh up)"                     # or drive it by hand
//	go test -tags integration ./acceptance/...
//	scripts/testenv.sh down
package acceptance

import "testing"

func TestMain(m *testing.M) { testMain(m) }
