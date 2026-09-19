//go:build integration

package acceptance

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/fakes/newznab"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

func TestIndexers(t *testing.T) {
	out := call(t, "indexer_list", nil)

	fake := findRow(t, rows(t, out["indexers"], "indexers"), "name", fakeIndexerName)
	settings := object(t, fake["settings"], "settings")
	// RSS is off on the fake, so a scheduled RSS sync grabs nothing behind the
	// suite's back
	if str(fake["implementation"]) != "Newznab" || str(fake["protocol"]) != "usenet" || fake["rss"] != false || fake["automatic_search"] != true {
		t.Errorf("the fake indexer = %v", fake)
	}
	if str(settings["baseUrl"]) != indexer.URL() {
		t.Errorf("baseUrl = %v, want %s", settings["baseUrl"], indexer.URL())
	}
	// the API key is a credential, and stays out
	if _, ok := settings["apiKey"]; ok {
		t.Errorf("the indexer settings include its apiKey: %v", settings)
	}

	one := call(t, "indexer_test", map[string]any{"name": "fake indexer"})
	if r := rows(t, one["results"], "results"); len(r) != 1 || r[0]["ok"] != true || len(rowsOf(r[0]["problems"])) != 0 {
		t.Errorf("testing the fake = %v", one)
	}
	if msg := callErr(t, "indexer_test", map[string]any{"name": "Jackett"}); !strings.Contains(msg, fakeIndexerName) {
		t.Errorf("an unknown indexer = %s", msg)
	}

	// an indexer that answers nothing but errors fails its test, and Sonarr's
	// health, and the audit, say so
	broken := addBrokenIndexer(t)
	all := call(t, "indexer_test", nil)
	results := rows(t, all["results"], "results")
	if ok := findRow(t, results, "name", fakeIndexerName); ok["ok"] != true {
		t.Errorf("the fake indexer failed beside the broken one: %v", ok)
	}
	bad := findRow(t, results, "name", brokenIndexerName)
	if bad["ok"] != false || len(strs(t, bad["problems"], "problems")) == 0 {
		t.Errorf("the broken indexer = %v", bad)
	}
	single := call(t, "indexer_test", map[string]any{"name": brokenIndexerName})
	if r := rows(t, single["results"], "results"); r[0]["ok"] != false || len(strs(t, r[0]["problems"], "problems")) == 0 {
		t.Errorf("testing the broken one alone = %v", single)
	}

	// an RSS sync fails against it, Sonarr backs off it, and its health
	// check and the audit report the indexer unavailable
	call(t, "task_run", map[string]any{"task": "RssSync"})
	if len(broken.RequestsFor("tvsearch"))+len(broken.RequestsFor("search")) == 0 {
		t.Error("the RSS sync never reached the broken indexer")
	}
	eventually(t, "audit_health", nil, "the indexer failure", func(out map[string]any) bool {
		for _, f := range rowsOf(out["findings"]) {
			if str(f["subject"]) == "IndexerStatusCheck" && strings.Contains(str(f["detail"]), brokenIndexerName) {
				return true
			}
		}
		return false
	})
	if f := findRow(t, rows(t, call(t, "server_health", nil)["checks"], "checks"), "source", "IndexerStatusCheck"); !strings.Contains(str(f["message"]), brokenIndexerName) {
		t.Errorf("server_health = %v", f)
	}
}

// addBrokenIndexer adds a second indexer and then breaks it: Sonarr tests an
// enabled indexer before saving it, even with forceSave (which only skips
// warnings), so it is a fake that answers when added and fails from then on.
// Only its RSS is on, so an RSS sync reaches it and nothing else. It is
// removed afterwards.
func addBrokenIndexer(t *testing.T) *newznab.Server {
	t.Helper()

	srv, err := newznab.New(newznab.Options{Addr: ":0", APIKey: fakeKey, PublicHost: containerHost(), Releases: catalogue()[:1]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	schemas, err := api.GetIndexerSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idx := schemas.Model[slices.IndexFunc(schemas.Model, func(s sonarr.IndexerResource) bool { return s.Implementation == "Newznab" })]
	idx.Name, idx.EnableRss, idx.EnableAutomaticSearch, idx.EnableInteractiveSearch = brokenIndexerName, new(true), new(false), new(false)
	idx.Fields = providerFields(idx.Fields, map[string]any{
		"baseUrl": srv.URL(), "apiPath": "/api", "apiKey": fakeKey, "categories": []int{5030, 5040},
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

	fake := findRow(t, rows(t, out["download_clients"], "download_clients"), "name", fakeClientName)
	settings := object(t, fake["settings"], "settings")
	if str(fake["implementation"]) != "Sabnzbd" || fake["enabled"] != true || str(settings["tvCategory"]) != sab.Category() {
		t.Errorf("the fake client = %v", fake)
	}
	if _, ok := settings["apiKey"]; ok {
		t.Errorf("the client settings include its apiKey: %v", settings)
	}

	all := call(t, "downloadclient_test", nil)
	if r := findRow(t, rows(t, all["results"], "results"), "name", fakeClientName); r["ok"] != true {
		t.Errorf("testing every client = %v", all)
	}
	one := call(t, "downloadclient_test", map[string]any{"name": fakeClientName})
	if r := rows(t, one["results"], "results"); len(r) != 1 || r[0]["ok"] != true {
		t.Errorf("testing the fake = %v", one)
	}
	if msg := callErr(t, "downloadclient_test", map[string]any{"name": "qBittorrent"}); !strings.Contains(msg, fakeClientName) {
		t.Errorf("an unknown client = %s", msg)
	}
}
