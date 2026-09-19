//go:build integration

package integration

// The providers - indexers, download clients, notifications, metadata and
// import lists - each created from Sonarr's own schema, read, updated,
// tested one at a time and all together, asked for one of its actions,
// updated and deleted in bulk where Sonarr has that. Every one points at
// something this suite runs or at Sonarr itself, so a create's validation
// (Sonarr tests a provider before it saves an enabled one) passes on its own
// merits and nothing reaches the network.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/fakes/newznab"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// bodyOf reads an answer the SDK leaves undecoded (an action's free-form
// JSON), which the client has buffered.
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()

	if resp == nil {
		t.Fatal("no response")
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// allValid checks a test-all answer: one result a provider, every one valid.
func allValid(t *testing.T, what string, results []sonarr.ProviderTestAllResult, ids ...int) {
	t.Helper()

	for _, id := range ids {
		i := slices.IndexFunc(results, func(r sonarr.ProviderTestAllResult) bool { return r.Id == id })
		switch {
		case i < 0:
			t.Errorf("%s has no result for %d: %+v", what, id, results)
		case !boolValue(results[i].IsValid):
			t.Errorf("%s says %d failed: %+v", what, id, results[i].ValidationFailures)
		}
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestIndexers(t *testing.T) {
	ctx := skipUnlessUp(t)

	create := func(name string) sonarr.IndexerResource {
		res := must(sc.PostIndexer(ctx, newIndexer(ctx, name), sonarr.PostIndexerOperationOptions{}))
		status(t, res.HttpResponse, http.StatusCreated)
		id := res.Model.Id
		t.Cleanup(func() { _, _ = sc.DeleteIndexerById(context.WithoutCancel(ctx), id) })
		return *res.Model
	}
	idx := create("SDK Indexer Two")
	if idx.Id == 0 || idx.Implementation != "Newznab" || idx.Protocol != sonarr.DownloadProtocolUsenet || fieldValue(idx.Fields, "baseUrl") != indexer.URL() {
		t.Fatalf("PostIndexer = %+v", idx)
	}
	// Sonarr never answers a stored key: the SDK has to send back what it
	// read, and Sonarr keeps the key it has
	if key := fieldValue(idx.Fields, "apiKey"); key == fakeKey {
		t.Error("the indexer's API key came back in the clear")
	}
	if got := must(sc.GetIndexerById(ctx, idx.Id)).Model; got.Name != "SDK Indexer Two" {
		t.Errorf("GetIndexerById = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetIndexer(ctx)).Model, func(x sonarr.IndexerResource) bool { return x.Id == idx.Id }) {
		t.Error("GetIndexer does not list the new indexer")
	}

	idx.Priority = 40
	upd := must(sc.PutIndexerById(ctx, idx.Id, idx, sonarr.PutIndexerByIdOperationOptions{}))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if upd.Model.Priority != 40 {
		t.Errorf("PutIndexerById = %+v", upd.Model)
	}
	// the key sent back masked still works: the test fetches the feed with it
	status(t, must(sc.PostIndexerTest(ctx, *upd.Model, sonarr.PostIndexerTestOperationOptions{})).HttpResponse, http.StatusOK)
	all := must(sc.PostIndexerTestAll(ctx))
	status(t, all.HttpResponse, http.StatusOK)
	allValid(t, "PostIndexerTestAll", all.Model, idx.Id, indexerID)

	// an action the indexer answers from its caps: the categories to pick from
	action := must(sc.PostIndexerActionByName(ctx, "newznabCategories", *upd.Model))
	if body := bodyOf(t, action.HttpResponse); !strings.Contains(body, `"options"`) || !strings.Contains(body, "5040") {
		t.Errorf("PostIndexerActionByName(newznabCategories) = %s", body)
	}

	// an indexer that fails its test fails the whole test-all: a 400 with
	// every result, which the SDK decodes as an answer, not an error
	indexer.SetFailure(newznab.Failure{Status: http.StatusServiceUnavailable})
	failing := must(sc.PostIndexerTestAll(ctx))
	indexer.SetFailure(newznab.Failure{})
	status(t, failing.HttpResponse, http.StatusBadRequest)
	i := slices.IndexFunc(failing.Model, func(r sonarr.ProviderTestAllResult) bool { return r.Id == idx.Id })
	if i < 0 || boolValue(failing.Model[i].IsValid) || len(failing.Model[i].ValidationFailures) == 0 || failing.Model[i].ValidationFailures[0].ErrorMessage == "" {
		t.Errorf("PostIndexerTestAll against a failing indexer = %+v", failing.Model)
	}
	// the failed test also held every indexer back from searches, until a
	// test passes again: IndexerFactory.Test records either way
	allValid(t, "PostIndexerTestAll once the indexer answers again", must(sc.PostIndexerTestAll(ctx)).Model, idx.Id, indexerID)

	// the bulk update answers every indexer it changed
	other := create("SDK Indexer Three")
	bulk := must(sc.PutIndexerBulk(ctx, sonarr.IndexerBulkResource{Ids: []int{idx.Id, other.Id}, Priority: 45, EnableRss: new(false)}))
	status(t, bulk.HttpResponse, http.StatusAccepted)
	if len(bulk.Model) != 2 || slices.ContainsFunc(bulk.Model, func(x sonarr.IndexerResource) bool { return x.Priority != 45 || boolValue(x.EnableRss) }) {
		t.Errorf("PutIndexerBulk = %+v", bulk.Model)
	}

	status(t, must(sc.DeleteIndexerById(ctx, idx.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetIndexerById(ctx, idx.Id)
	gone(t, "GetIndexerById", err)
	status(t, must(sc.DeleteIndexerBulk(ctx, sonarr.IndexerBulkResource{Ids: []int{other.Id}})).HttpResponse, http.StatusOK)
	_, err = sc.GetIndexerById(ctx, other.Id)
	gone(t, "GetIndexerById after the bulk delete", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestDownloadClients(t *testing.T) {
	ctx := skipUnlessUp(t)

	create := func(name string) sonarr.DownloadClientResource {
		res := must(sc.PostDownloadClient(ctx, newDownloadClient(ctx, name), sonarr.PostDownloadClientOperationOptions{}))
		status(t, res.HttpResponse, http.StatusCreated)
		id := res.Model.Id
		t.Cleanup(func() { _, _ = sc.DeleteDownloadClientById(context.WithoutCancel(ctx), id) })
		return *res.Model
	}
	dc := create("SDK SABnzbd Two")
	if dc.Id == 0 || dc.Implementation != "Sabnzbd" || dc.Protocol != sonarr.DownloadProtocolUsenet || fieldValue(dc.Fields, "tvCategory") != "tv" {
		t.Fatalf("PostDownloadClient = %+v", dc)
	}
	if got := must(sc.GetDownloadClientById(ctx, dc.Id)).Model; got.Name != "SDK SABnzbd Two" {
		t.Errorf("GetDownloadClientById = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetDownloadClient(ctx)).Model, func(x sonarr.DownloadClientResource) bool { return x.Id == dc.Id }) {
		t.Error("GetDownloadClient does not list the new client")
	}

	dc.Priority = 10
	upd := must(sc.PutDownloadClientById(ctx, dc.Id, dc, sonarr.PutDownloadClientByIdOperationOptions{}))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if upd.Model.Priority != 10 {
		t.Errorf("PutDownloadClientById = %+v", upd.Model)
	}
	status(t, must(sc.PostDownloadClientTest(ctx, *upd.Model, sonarr.PostDownloadClientTestOperationOptions{})).HttpResponse, http.StatusOK)
	all := must(sc.PostDownloadClientTestAll(ctx))
	status(t, all.HttpResponse, http.StatusOK)
	allValid(t, "PostDownloadClientTestAll", all.Model, dc.Id, downloadClient)
	// SABnzbd has no actions of its own; an unknown one answers null
	action := must(sc.PostDownloadClientActionByName(ctx, "sdkNoSuchAction", *upd.Model))
	if body := strings.TrimSpace(bodyOf(t, action.HttpResponse)); body != "null" && body != "{}" {
		t.Errorf("PostDownloadClientActionByName = %s", body)
	}

	other := create("SDK SABnzbd Three")
	bulk := must(sc.PutDownloadClientBulk(ctx, sonarr.DownloadClientBulkResource{Ids: []int{dc.Id, other.Id}, Priority: 20}))
	status(t, bulk.HttpResponse, http.StatusAccepted)
	if len(bulk.Model) != 2 || slices.ContainsFunc(bulk.Model, func(x sonarr.DownloadClientResource) bool { return x.Priority != 20 }) {
		t.Errorf("PutDownloadClientBulk = %+v", bulk.Model)
	}

	status(t, must(sc.DeleteDownloadClientById(ctx, dc.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetDownloadClientById(ctx, dc.Id)
	gone(t, "GetDownloadClientById", err)
	status(t, must(sc.DeleteDownloadClientBulk(ctx, sonarr.DownloadClientBulkResource{Ids: []int{other.Id}})).HttpResponse, http.StatusOK)
	_, err = sc.GetDownloadClientById(ctx, other.Id)
	gone(t, "GetDownloadClientById after the bulk delete", err)
}

// webhooks is a receiver for Sonarr's webhook notifications, on this machine
// where the container can reach it.
type webhooks struct {
	url string

	mu     sync.Mutex
	events []string
}

func newWebhooks(t *testing.T) *webhooks {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listening on %s, not TCP", ln.Addr())
	}
	w := &webhooks{url: "http://" + containerHost() + ":" + strconv.Itoa(addr.Port) + "/hook"}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			EventType string `json:"eventType"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.mu.Lock()
		w.events = append(w.events, body.EventType)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return w
}

// count is how many events of a type arrived.
func (w *webhooks) count(eventType string) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := 0
	for _, e := range w.events {
		if e == eventType {
			n++
		}
	}

	return n
}

//nolint:paralleltest // the tests share one Sonarr
func TestNotifications(t *testing.T) {
	ctx := skipUnlessUp(t)

	hooks := newWebhooks(t)
	schemas := must(sc.GetNotificationSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.NotificationResource) bool { return s.Implementation == "Webhook" })
	if i < 0 {
		t.Fatal("sonarr has no Webhook notification schema")
	}
	n := schemas[i]
	n.Name, n.OnGrab, n.OnDownload, n.OnHealthIssue = "SDK Webhook", new(true), new(true), new(false)
	n.Fields = fields(n.Fields, map[string]any{"url": hooks.url, "method": 1})

	// an enabled notification is tested before it is saved: Sonarr sends it
	// a test event
	res := must(sc.PostNotification(ctx, n, sonarr.PostNotificationOperationOptions{}))
	status(t, res.HttpResponse, http.StatusCreated)
	created := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteNotificationById(context.WithoutCancel(ctx), created.Id) })
	if created.Id == 0 || fieldValue(created.Fields, "url") != hooks.url || !boolValue(created.SupportsOnGrab) {
		t.Fatalf("PostNotification = %+v", created)
	}
	if !poll(10*time.Second, func() bool { return hooks.count("Test") >= 1 }) {
		t.Error("the webhook never received the test Sonarr sends a notification before saving it")
	}
	if got := must(sc.GetNotificationById(ctx, created.Id)).Model; got.Name != "SDK Webhook" {
		t.Errorf("GetNotificationById = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetNotification(ctx)).Model, func(x sonarr.NotificationResource) bool { return x.Id == created.Id }) {
		t.Error("GetNotification does not list the new notification")
	}

	created.OnDownload = new(false)
	upd := must(sc.PutNotificationById(ctx, created.Id, created, sonarr.PutNotificationByIdOperationOptions{}))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if boolValue(upd.Model.OnDownload) {
		t.Errorf("PutNotificationById = %+v", upd.Model)
	}

	before := hooks.count("Test")
	status(t, must(sc.PostNotificationTest(ctx, *upd.Model, sonarr.PostNotificationTestOperationOptions{})).HttpResponse, http.StatusOK)
	if !poll(10*time.Second, func() bool { return hooks.count("Test") > before }) {
		t.Error("PostNotificationTest sent the webhook nothing")
	}
	all := must(sc.PostNotificationTestAll(ctx))
	status(t, all.HttpResponse, http.StatusOK)
	allValid(t, "PostNotificationTestAll", all.Model, created.Id)
	// a webhook has no actions; an unknown one answers null
	action := must(sc.PostNotificationActionByName(ctx, "sdkNoSuchAction", *upd.Model))
	if body := strings.TrimSpace(bodyOf(t, action.HttpResponse)); body != "null" && body != "{}" {
		t.Errorf("PostNotificationActionByName = %s", body)
	}

	status(t, must(sc.DeleteNotificationById(ctx, created.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetNotificationById(ctx, created.Id)
	gone(t, "GetNotificationById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestMetadata(t *testing.T) {
	ctx := skipUnlessUp(t)

	// Sonarr has one of each metadata writer from the start, all off
	existing := must(sc.GetMetadata(ctx)).Model
	if len(existing) == 0 {
		t.Fatal("GetMetadata listed nothing")
	}
	schemas := must(sc.GetMetadataSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.MetadataResource) bool { return s.Implementation == "XbmcMetadata" })
	if i < 0 {
		t.Fatalf("GetMetadataSchema has no Kodi writer: %+v", schemas)
	}
	// a second writer, left off, so it writes nothing into the library
	md := schemas[i]
	md.Name, md.Enable = "SDK Kodi", new(false)
	res := must(sc.PostMetadata(ctx, md, sonarr.PostMetadataOperationOptions{}))
	status(t, res.HttpResponse, http.StatusCreated)
	created := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteMetadataById(context.WithoutCancel(ctx), created.Id) })
	if created.Id == 0 || created.Name != "SDK Kodi" || boolValue(created.Enable) {
		t.Fatalf("PostMetadata = %+v", created)
	}
	if got := must(sc.GetMetadataById(ctx, created.Id)).Model; got.Name != "SDK Kodi" {
		t.Errorf("GetMetadataById = %+v", got)
	}

	created.Fields = fields(created.Fields, map[string]any{"seriesImages": false})
	upd := must(sc.PutMetadataById(ctx, created.Id, created, sonarr.PutMetadataByIdOperationOptions{}))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if on, ok := fieldValue(upd.Model.Fields, "seriesImages").(bool); !ok || on {
		t.Errorf("PutMetadataById = %+v", upd.Model.Fields)
	}
	status(t, must(sc.PostMetadataTest(ctx, *upd.Model, sonarr.PostMetadataTestOperationOptions{})).HttpResponse, http.StatusOK)
	// testing all tests the enabled ones, which is none of these
	status(t, must(sc.PostMetadataTestAll(ctx)).HttpResponse, http.StatusOK)
	action := must(sc.PostMetadataActionByName(ctx, "sdkNoSuchAction", *upd.Model))
	if body := strings.TrimSpace(bodyOf(t, action.HttpResponse)); body != "null" && body != "{}" {
		t.Errorf("PostMetadataActionByName = %s", body)
	}

	status(t, must(sc.DeleteMetadataById(ctx, created.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetMetadataById(ctx, created.Id)
	gone(t, "GetMetadataById", err)
}

// newImportList adds a Sonarr list pointed at this same Sonarr, over
// localhost inside the container: the one kind that lists series without
// reaching a service on the internet. It adds nothing automatically, so it
// never adds this Sonarr's series to itself, and is removed when the test
// ends.
func newImportList(ctx context.Context, t *testing.T, name string) sonarr.ImportListResource {
	t.Helper()

	schemas := must(sc.GetImportListSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.ImportListResource) bool { return s.Implementation == "SonarrImport" })
	if i < 0 {
		t.Fatal("sonarr has no Sonarr import list schema")
	}
	l := schemas[i]
	l.Name, l.EnableAutomaticAdd, l.SearchForMissingEpisodes = name, new(false), new(false)
	l.QualityProfileId, l.RootFolderPath, l.SeasonFolder = profileID, "/tv", new(true)
	l.ShouldMonitor, l.MonitorNewItems, l.SeriesType = sonarr.MonitorTypesNone, sonarr.NewItemMonitorTypesNone, sonarr.SeriesTypesStandard
	l.Fields = fields(l.Fields, map[string]any{"baseUrl": "http://localhost:8989", "apiKey": os.Getenv("SONARR_TOKEN")})
	res := must(sc.PostImportList(ctx, l, sonarr.PostImportListOperationOptions{}))
	status(t, res.HttpResponse, http.StatusCreated)
	id := res.Model.Id
	t.Cleanup(func() { _, _ = sc.DeleteImportListById(context.WithoutCancel(ctx), id) })

	return *res.Model
}

//nolint:paralleltest // the tests share one Sonarr
func TestImportLists(t *testing.T) {
	ctx := skipUnlessUp(t)

	list := newImportList(ctx, t, "SDK Sonarr List")
	if list.Id == 0 || list.Implementation != "SonarrImport" || list.ListType == "" {
		t.Fatalf("PostImportList = %+v", list)
	}
	if got := must(sc.GetImportListById(ctx, list.Id)).Model; got.Name != "SDK Sonarr List" {
		t.Errorf("GetImportListById = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetImportList(ctx)).Model, func(x sonarr.ImportListResource) bool { return x.Id == list.Id }) {
		t.Error("GetImportList does not list the new list")
	}

	list.ListOrder = 3
	upd := must(sc.PutImportListById(ctx, list.Id, list, sonarr.PutImportListByIdOperationOptions{}))
	status(t, upd.HttpResponse, http.StatusAccepted)
	status(t, must(sc.PostImportListTest(ctx, *upd.Model, sonarr.PostImportListTestOperationOptions{})).HttpResponse, http.StatusOK)
	// testing all tests the lists that add series automatically, which this
	// one (left off, so it never adds this Sonarr's series to itself) is not
	all := must(sc.PostImportListTestAll(ctx))
	status(t, all.HttpResponse, http.StatusOK)
	if slices.ContainsFunc(all.Model, func(r sonarr.ProviderTestAllResult) bool { return r.Id == list.Id }) {
		t.Errorf("PostImportListTestAll tested a list that adds nothing: %+v", all.Model)
	}

	// the list's own action: the quality profiles of the Sonarr it reads,
	// which is this one
	action := must(sc.PostImportListActionByName(ctx, "getProfiles", *upd.Model))
	if body := bodyOf(t, action.HttpResponse); !strings.Contains(body, profileName) {
		t.Errorf("PostImportListActionByName(getProfiles) = %s", body)
	}

	other := newImportList(ctx, t, "SDK Sonarr List Two")
	bulk := must(sc.PutImportListBulk(ctx, sonarr.ImportListBulkResource{Ids: []int{list.Id, other.Id}, EnableAutomaticAdd: new(false), RootFolderPath: "/tv"}))
	status(t, bulk.HttpResponse, http.StatusAccepted)
	if len(bulk.Model) != 2 {
		t.Errorf("PutImportListBulk = %+v", bulk.Model)
	}

	status(t, must(sc.DeleteImportListById(ctx, list.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetImportListById(ctx, list.Id)
	gone(t, "GetImportListById", err)
	status(t, must(sc.DeleteImportListBulk(ctx, sonarr.ImportListBulkResource{Ids: []int{other.Id}})).HttpResponse, http.StatusOK)
	_, err = sc.GetImportListById(ctx, other.Id)
	gone(t, "GetImportListById after the bulk delete", err)
}
