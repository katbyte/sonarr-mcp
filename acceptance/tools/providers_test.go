//go:build acceptance

package tools_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

func TestIndexers(t *testing.T) {
	out := call(t, "indexer_list", nil)

	fake := findRow(t, acctest.Rows(t, out["indexers"], "indexers"), "name", fakeIndexerName)
	settings := acctest.Object(t, fake["settings"], "settings")
	// RSS is off on the fake, so a scheduled RSS sync grabs nothing behind the
	// suite's back
	if acctest.Str(fake["implementation"]) != "Newznab" || acctest.Str(fake["protocol"]) != "usenet" || !acctest.IsBool(fake["rss"], false) || !acctest.BoolOf(fake["automatic_search"]) {
		t.Errorf("the fake indexer = %v", fake)
	}
	if acctest.Str(settings["baseUrl"]) != indexer.URL() {
		t.Errorf("baseUrl = %v, want %s", settings["baseUrl"], indexer.URL())
	}
	// the API key is a credential, and stays out
	if _, ok := settings["apiKey"]; ok {
		t.Errorf("the indexer settings include its apiKey: %v", settings)
	}

	one := call(t, "indexer_test", map[string]any{"name": "fake indexer"})
	if r := acctest.Rows(t, one["results"], "results"); len(r) != 1 || !acctest.BoolOf(r[0]["ok"]) || len(acctest.RowsOf(r[0]["problems"])) != 0 {
		t.Errorf("testing the fake = %v", one)
	}
	if msg := callErr(t, "indexer_test", map[string]any{"name": "Jackett"}); !strings.Contains(msg, fakeIndexerName) {
		t.Errorf("an unknown indexer = %s", msg)
	}

	// an indexer that answers nothing but errors fails its test, and Sonarr's
	// health, and the audit, say so
	broken := addBrokenIndexer(t)
	all := call(t, "indexer_test", nil)
	results := acctest.Rows(t, all["results"], "results")
	if ok := findRow(t, results, "name", fakeIndexerName); !acctest.BoolOf(ok["ok"]) {
		t.Errorf("the fake indexer failed beside the broken one: %v", ok)
	}
	bad := findRow(t, results, "name", brokenIndexerName)
	if !acctest.IsBool(bad["ok"], false) || len(acctest.Strs(t, bad["problems"], "problems")) == 0 {
		t.Errorf("the broken indexer = %v", bad)
	}
	single := call(t, "indexer_test", map[string]any{"name": brokenIndexerName})
	if r := acctest.Rows(t, single["results"], "results"); !acctest.IsBool(r[0]["ok"], false) || len(acctest.Strs(t, r[0]["problems"], "problems")) == 0 {
		t.Errorf("testing the broken one alone = %v", single)
	}

	// Sonarr says where it failed, the address it asked with the key it
	// keeps for the indexer on it: the address is shown, and the key is not,
	// in a test's problems and in a save Sonarr refuses for the same reason
	refused := callErr(t, "indexer_edit", map[string]any{"which": brokenIndexerName, "set": map[string]any{"priority": 40}})
	for what, said := range map[string]string{"every indexer tested": fmt.Sprint(all), "the broken one tested": fmt.Sprint(single), "the save refused": refused} {
		if strings.Contains(said, testenv.FakeKey) || !strings.Contains(said, "apikey=REDACTED") || !strings.Contains(said, "/broken/api") {
			t.Errorf("%s = %s, want the address Sonarr asked without the key", what, said)
		}
	}

	// an RSS sync fails against it, Sonarr backs off it, and its health
	// check and the audit report the indexer unavailable
	call(t, "task_run", map[string]any{"task": "RssSync"})
	if !slices.ContainsFunc(broken.Requests(), func(r newznab.Request) bool { return r.Function == "tvsearch" || r.Function == "search" }) {
		t.Error("the RSS sync never reached the broken indexer")
	}
	eventually(t, "audit_health", nil, "the indexer failure", func(out map[string]any) bool {
		for _, f := range acctest.RowsOf(out["findings"]) {
			if acctest.Str(f["subject"]) == "IndexerStatusCheck" && strings.Contains(acctest.Str(f["detail"]), brokenIndexerName) {
				return true
			}
		}
		return false
	})
	if f := findRow(t, acctest.Rows(t, call(t, "server_health", nil)["checks"], "checks"), "source", "IndexerStatusCheck"); !strings.Contains(acctest.Str(f["message"]), brokenIndexerName) {
		t.Errorf("server_health = %v", f)
	}
}

// addBrokenIndexer adds a second indexer and then breaks it: Sonarr tests an
// enabled indexer before saving it, even with forceSave (which only skips
// warnings), so it is a fake that answers when added and fails from then on.
// Only its RSS is on, so an RSS sync reaches it and nothing else. Sonarr's
// record of it is removed afterwards, and the fake is mended.
func addBrokenIndexer(t *testing.T) *newznab.Indexer {
	t.Helper()

	srv, err := fakes.AddIndexer("broken", catalogue()[:1]...)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetFailure(newznab.Failure{})
	t.Cleanup(func() { srv.SetFailure(newznab.Failure{}) })
	schemas, err := api.GetIndexerSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idx := schemas.Model[slices.IndexFunc(schemas.Model, func(s sonarr.IndexerResource) bool { return s.Implementation == "Newznab" })]
	idx.Name, idx.EnableRss, idx.EnableAutomaticSearch, idx.EnableInteractiveSearch = brokenIndexerName, new(true), new(false), new(false)
	idx.Fields = providerFields(idx.Fields, map[string]any{
		"baseUrl": srv.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []int{5030, 5040},
	})
	made, err := api.PostIndexer(ctx, idx, sonarr.PostIndexerOperationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = api.DeleteIndexerById(ctx, made.Model.Id) })
	srv.SetFailure(newznab.Failure{Status: http.StatusInternalServerError})

	return srv
}

func TestDownloadClients(t *testing.T) {
	out := call(t, "downloadclient_list", nil)

	fake := findRow(t, acctest.Rows(t, out["download_clients"], "download_clients"), "name", fakeClientName)
	settings := acctest.Object(t, fake["settings"], "settings")
	if acctest.Str(fake["implementation"]) != "Sabnzbd" || !acctest.BoolOf(fake["enabled"]) || acctest.Str(settings["tvCategory"]) != sab.Category() {
		t.Errorf("the fake client = %v", fake)
	}
	if _, ok := settings["apiKey"]; ok {
		t.Errorf("the client settings include its apiKey: %v", settings)
	}

	all := call(t, "downloadclient_test", nil)
	if r := findRow(t, acctest.Rows(t, all["results"], "results"), "name", fakeClientName); !acctest.BoolOf(r["ok"]) {
		t.Errorf("testing every client = %v", all)
	}
	one := call(t, "downloadclient_test", map[string]any{"name": fakeClientName})
	if r := acctest.Rows(t, one["results"], "results"); len(r) != 1 || !acctest.BoolOf(r[0]["ok"]) {
		t.Errorf("testing the fake = %v", one)
	}
	if msg := callErr(t, "downloadclient_test", map[string]any{"name": "qBittorrent"}); !strings.Contains(msg, fakeClientName) {
		t.Errorf("an unknown client = %s", msg)
	}
}
