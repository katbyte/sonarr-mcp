//go:build integration

package acceptance

import (
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/arrserver"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
)

// The settings tools against a real Sonarr: each kind of record it keeps is
// listed, added, changed and deleted the one way, and a delete waits for
// confirm. Each test takes away what it added.

// record is the record a write answered.
func record(t *testing.T, out map[string]any) map[string]any {
	t.Helper()

	return acctest.Object(t, out["record"], "record")
}

// strsOf is a list of text in an answer, and nothing when it is not one.
func strsOf(v any) []string {
	list := acctest.RowsOfAny(v)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, acctest.Str(item))
	}

	return out
}

// changedWith reports whether a write says a change that starts as given.
func changedWith(out map[string]any, start string) bool {
	return slices.ContainsFunc(strsOf(out["changed"]), func(c string) bool { return strings.HasPrefix(c, start) })
}

// deletes runs a delete tool the two ways: without confirm it names what
// would go and leaves it, and with confirm it is gone.
func deletes(t *testing.T, tool, which, label string) {
	t.Helper()

	would := call(t, tool, map[string]any{"which": which})
	if acctest.Str(would["would_delete"]) != label || acctest.Str(would["deleted"]) != "" {
		t.Errorf("%s without confirm = %v, want it to name %q and delete nothing", tool, would, label)
	}
	done := call(t, tool, map[string]any{"which": which, "confirm": true})
	if acctest.Str(done["deleted"]) != label {
		t.Errorf("%s with confirm = %v", tool, done)
	}
	if msg := callErr(t, tool, map[string]any{"which": which, "confirm": true}); !strings.Contains(msg, "no ") {
		t.Errorf("%s of what is gone = %s", tool, msg)
	}
}

func TestIndexerAddEditDelete(t *testing.T) {
	kinds := acctest.Rows(t, call(t, "indexer_kinds", nil)["kinds"], "kinds")
	newznab := findRow(t, kinds, "kind", "Newznab")
	settings := acctest.Rows(t, newznab["settings"], "settings")
	if key := findRow(t, settings, "name", "apiKey"); key["credential"] != true || len(settings) < 3 {
		t.Errorf("what a Newznab indexer takes = %v", settings)
	}
	findRow(t, kinds, "kind", "Torznab")

	srv, err := fakes.AddIndexer("added", catalogue()[:1]...)
	if err != nil {
		t.Fatal(err)
	}
	const name = "Added Indexer"
	t.Cleanup(func() {
		_, _ = invoke("indexer_delete", map[string]any{"which": name, "confirm": true})
		_, _ = invoke("tag_delete", map[string]any{"label": "added-indexer", "confirm": true})
	})
	out := call(t, "indexer_add", map[string]any{
		"kind": "newznab", "name": name, "set": map[string]any{"enableRss": false, "priority": 30, "tags": []any{"added-indexer"}},
		"settings": map[string]any{"baseUrl": srv.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []any{5030, 5040}},
	})
	rec := record(t, out)
	own := acctest.Object(t, rec["settings"], "settings")
	// searching is on unless said otherwise, as Sonarr's own page adds one
	if acctest.Str(rec["name"]) != name || acctest.NumOr0(rec["priority"]) != 30 || rec["enableRss"] != false || rec["enableAutomaticSearch"] != true || rec["enableInteractiveSearch"] != true || acctest.Str(own["baseUrl"]) != srv.URL() || !slices.Equal(acctest.Strs(t, rec["tags"], "tags"), []string{"added-indexer"}) {
		t.Errorf("the indexer added = %v", rec)
	}
	if _, shown := own["apiKey"]; shown {
		t.Errorf("the indexer's API key is answered: %v", own)
	}
	// the tool that lists indexers sees it, and Sonarr's own test of it passes
	listed := findRow(t, acctest.Rows(t, call(t, "indexer_list", nil)["indexers"], "indexers"), "name", name)
	if acctest.NumOr0(listed["priority"]) != 30 {
		t.Errorf("indexer_list = %v", listed)
	}
	if r := acctest.Rows(t, call(t, "indexer_test", map[string]any{"name": name})["results"], "results"); len(r) != 1 || r[0]["ok"] != true {
		t.Errorf("testing the indexer added = %v", r)
	}

	// Sonarr tries an indexer again before it saves a change to one, and
	// refuses categories its feed has nothing in
	if msg := callErr(t, "indexer_edit", map[string]any{"which": name, "settings": map[string]any{"categories": []any{5030}}}); !strings.Contains(msg, "no results in the configured categories") || !strings.Contains(msg, "force") {
		t.Errorf("an edit to categories the indexer has nothing in = %s", msg)
	}
	edited := call(t, "indexer_edit", map[string]any{"which": name, "set": map[string]any{"priority": 10}, "settings": map[string]any{"categories": []any{5040, 5045}}})
	if !changedWith(edited, "priority: 30 -> 10") || !changedWith(edited, "categories:") || acctest.NumOr0(record(t, edited)["priority"]) != 10 {
		t.Errorf("indexer_edit = %v", edited)
	}
	if same := call(t, "indexer_edit", map[string]any{"which": name, "set": map[string]any{"priority": 10}}); len(strsOf(same["changed"])) != 0 {
		t.Errorf("an edit that changes nothing = %v", same)
	}
	if msg := callErr(t, "indexer_edit", map[string]any{"which": name, "settings": map[string]any{"url": "x"}}); !strings.Contains(msg, "baseUrl") {
		t.Errorf("a setting it does not have = %s", msg)
	}

	// Sonarr tries an indexer before it saves one: one that answers nothing
	// is refused, and saved all the same with force, switched on as it was
	// asked for. Sonarr's own force does not do that for one being added,
	// only for one being changed
	dead := map[string]any{"kind": "Newznab", "name": "Dead Indexer", "settings": map[string]any{"baseUrl": "http://127.0.0.1:9", "apiKey": "x"}}
	if msg := callErr(t, "indexer_add", dead); !strings.Contains(msg, "force") {
		t.Errorf("an indexer that cannot be reached = %s", msg)
	}
	t.Cleanup(func() { _, _ = invoke("indexer_delete", map[string]any{"which": "Dead Indexer", "confirm": true}) })
	if forced := record(t, call(t, "indexer_add", mergeArgs(dead, map[string]any{"force": true}))); forced["enableRss"] != true || forced["enableAutomaticSearch"] != true || forced["enableInteractiveSearch"] != true {
		t.Errorf("an indexer added with force = %v, want it switched on", forced)
	}
	deletes(t, "indexer_delete", "Dead Indexer", "Dead Indexer")

	deletes(t, "indexer_delete", name, name)
}

func TestDownloadClientAddEditDelete(t *testing.T) {
	kinds := acctest.Rows(t, call(t, "downloadclient_kinds", nil)["kinds"], "kinds")
	findRow(t, kinds, "kind", "Sabnzbd")
	findRow(t, kinds, "kind", "TorrentBlackhole")

	const name = "Second SABnzbd"
	t.Cleanup(func() { _, _ = invoke("downloadclient_delete", map[string]any{"which": name, "confirm": true}) })
	out := call(t, "downloadclient_add", map[string]any{
		"kind": "Sabnzbd", "name": name, "set": map[string]any{"priority": 5},
		"settings": map[string]any{"host": sab.Host(), "port": sab.Port(), "apiKey": testenv.FakeKey, "tvCategory": sab.Category(), "useSsl": false},
	})
	rec := record(t, out)
	if own := acctest.Object(t, rec["settings"], "settings"); acctest.Str(rec["name"]) != name || acctest.NumOr0(rec["priority"]) != 5 || acctest.Str(own["tvCategory"]) != sab.Category() || own["apiKey"] != nil {
		t.Errorf("the client added = %v", rec)
	}
	// it is on unless said otherwise, as Sonarr's own page adds one
	listed := findRow(t, acctest.Rows(t, call(t, "downloadclient_list", nil)["download_clients"], "download_clients"), "name", name)
	if listed["enabled"] != true {
		t.Errorf("downloadclient_list = %v", listed)
	}

	edited := call(t, "downloadclient_edit", map[string]any{"which": name, "set": map[string]any{"enable": false, "removeCompletedDownloads": false}})
	if !changedWith(edited, "enable: true -> false") || !changedWith(edited, "removeCompletedDownloads:") {
		t.Errorf("downloadclient_edit = %v", edited)
	}
	deletes(t, "downloadclient_delete", name, name)
}

// shownSettings are the settings of Sonarr's providers that the tools show
// and that are text, each one looked at and found to hold no credential.
// Sonarr marks some settings private and the tools hide more by name and by
// kind; what is left is this list, and a setting a later Sonarr adds is on
// it only once somebody has read what it holds.
var shownSettings = []string{
	// where a provider is, and where it puts things
	"apiPath", "apiUrl", "baseUrl", "clickUrl", "host", "port", "rpcPath", "server", "serverUrl", "urlBase",
	"category", "completedDirectory", "destination", "destinationDirectory", "directory", "downloadDirectory", "magnetFileExtension", "mapFrom", "mapTo", "nzbFolder", "path", "strmFolder", "torrentFolder", "tvCategory", "tvDirectory", "tvImportedCategory", "watchFolder",
	// who and what a message goes to, and how it looks
	"author", "avatar", "bcc", "cc", "channel", "channelTags", "chatId", "deviceIds", "deviceNames", "devices", "displayTime", "event", "expire", "from", "icon", "mention", "notificationName", "receiverId", "recipients", "retry", "senderDomain", "senderId", "sound", "to", "topicId",
	// what a list reads
	"genres", "limit", "listId", "listname", "rating", "years",
	// labels of the provider's own
	"postImportTags", "tags",
	// an id an application is known by, beside the token that is hidden
	"appId",
	// the account whose list is read, not the one signed in with: a sign-in's name Sonarr marks private, and that is hidden
	"username",
	// text a person may have put a credential in, though it is not for one: extra query parameters for an indexer or for Trakt, a script's arguments
	"additionalParameters", "traktAdditionalParameters", "arguments",
	// addresses whose path can be private: a generic webhook's, a Plex watchlist's feed. Shown without sign-in or parameter values; whether to hide them whole is the owner's call
	"url",
	// a topic on a public ntfy server is as good as a password to it; on one's own it is a name. The owner's call
	"topics",
}

// Every setting of every kind of indexer, download client, notification and
// import list this Sonarr has is a credential the tools hide, or a switch, a
// number or a choice, or text on the list above. A setting Sonarr adds in a
// later release fails here until it has been looked at.
func TestEveryProviderSettingIsHiddenOrLookedAt(t *testing.T) {
	seen, total := map[string]bool{}, 0
	for _, tool := range []string{"indexer_kinds", "downloadclient_kinds", "notification_kinds", "importlist_kinds"} {
		for _, kind := range acctest.Rows(t, call(t, tool, map[string]any{"limit": 1000})["kinds"], "kinds") {
			for _, setting := range acctest.RowsOf(kind["settings"]) {
				total++
				name := acctest.Str(setting["name"])
				switch acctest.Str(setting["type"]) {
				case "checkbox", "number", "select", "oAuth":
					// a switch, a number, a choice, and a button that holds nothing
					continue
				}
				switch {
				case setting["credential"] == true:
				case slices.Contains(shownSettings, name):
					seen[name] = true
				default:
					t.Errorf("%s %s shows its setting %s (%s) and nobody has looked at what it holds: hide it as a credential, or add it to shownSettings", tool, acctest.Str(kind["kind"]), name, acctest.Str(setting["type"]))
				}
			}
		}
	}
	if total < 400 {
		t.Errorf("%d settings were read, want the several hundred Sonarr has", total)
	}
	for _, name := range shownSettings {
		if !seen[name] {
			t.Errorf("shownSettings has %s, which no kind has as text it shows: take it out", name)
		}
	}
}

// A notification sends a real message when it is tested and before it is
// saved: here to a webhook the suite answers itself, which counts them.
func TestNotifications(t *testing.T) {
	findRow(t, acctest.Rows(t, call(t, "notification_kinds", nil)["kinds"], "kinds"), "kind", "Webhook")

	var sent atomic.Int32
	var refuse atomic.Bool
	const hook = "hooks.sonarr-mcp.test"
	var authorised atomic.Pointer[string]
	stop := proxy.Serve(hook, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		authorised.Store(new(r.Header.Get("Authorization")))
		if refuse.Load() {
			http.Error(w, "the hook is closed", http.StatusForbidden)
		}
	}))
	t.Cleanup(stop)

	const name = "Suite Hook"
	t.Cleanup(func() { _, _ = invoke("notification_delete", map[string]any{"which": name, "confirm": true}) })
	// the hook has a header of its own with a token in it, which is where a
	// webhook's credential goes and which Sonarr does not count private: it
	// is sent with every message, and shown in no answer
	headers := []any{map[string]any{"key": "Authorization", "value": "Bearer " + secret("suite-hook-token-7f3a91c2d4e85b60")}}
	out := call(t, "notification_add", map[string]any{"kind": "Webhook", "name": name, "set": map[string]any{"onGrab": true, "onDownload": true}, "settings": map[string]any{"url": "http://" + hook + "/in", "method": 1, "headers": headers}})
	rec := record(t, out)
	if own := acctest.Object(t, rec["settings"], "settings"); rec["onGrab"] != true || rec["onRename"] == true || acctest.Str(own["url"]) != "http://"+hook+"/in" || own["headers"] != nil {
		t.Errorf("the notification added = %v", rec)
	}
	if got := authorised.Load(); got == nil || *got != "Bearer suite-hook-token-7f3a91c2d4e85b60" {
		t.Errorf("the hook was sent Authorization %v, want the header it was given", got)
	}
	if sent.Load() == 0 {
		t.Error("Sonarr saved the notification without sending a test through it")
	}
	listed := findRow(t, acctest.Rows(t, call(t, "notification_list", nil)["items"], "items"), "name", name)
	if listed["onDownload"] != true || acctest.Str(listed["implementation"]) != "Webhook" {
		t.Errorf("notification_list = %v", listed)
	}

	before := sent.Load()
	if r := acctest.Rows(t, call(t, "notification_test", map[string]any{"which": name})["results"], "results"); len(r) != 1 || r[0]["ok"] != true || sent.Load() == before {
		t.Errorf("testing the notification = %v, and %d messages were sent", r, sent.Load()-before)
	}
	all := acctest.Rows(t, call(t, "notification_test", nil)["results"], "results")
	if r := findRow(t, all, "name", name); r["ok"] != true {
		t.Errorf("testing every notification = %v", all)
	}

	// a hook that refuses is a test that fails, which is an answer and not an
	// error; and an edit Sonarr would refuse for it is saved with force
	refuse.Store(true)
	if r := acctest.Rows(t, call(t, "notification_test", map[string]any{"which": name})["results"], "results"); r[0]["ok"] != false || len(acctest.Strs(t, r[0]["problems"], "problems")) == 0 {
		t.Errorf("testing a notification that is refused = %v", r)
	}
	if msg := callErr(t, "notification_edit", map[string]any{"which": name, "set": map[string]any{"onRename": true}}); !strings.Contains(msg, "force") {
		t.Errorf("an edit Sonarr's test fails = %s", msg)
	}
	if edited := call(t, "notification_edit", map[string]any{"which": name, "set": map[string]any{"onRename": true}, "force": true}); !changedWith(edited, "onRename:") {
		t.Errorf("notification_edit with force = %v", edited)
	}

	// a hook nothing answers is saved with force too, sent for what was
	// asked. Sonarr's test of every notification answers for none of them
	// while one cannot be reached at all, and the tool still says which
	const dead = "Dead Hook"
	gone := map[string]any{"kind": "Webhook", "name": dead, "set": map[string]any{"onGrab": true}, "settings": map[string]any{"url": "http://127.0.0.1:9/in"}}
	if msg := callErr(t, "notification_add", gone); !strings.Contains(msg, "force") {
		t.Errorf("a notification that cannot be reached = %s", msg)
	}
	t.Cleanup(func() { _, _ = invoke("notification_delete", map[string]any{"which": dead, "confirm": true}) })
	if forced := record(t, call(t, "notification_add", mergeArgs(gone, map[string]any{"force": true}))); forced["onGrab"] != true || forced["onRename"] == true {
		t.Errorf("a notification added with force = %v, want it sent for a grab", forced)
	}
	every := acctest.Rows(t, call(t, "notification_test", nil)["results"], "results")
	if r := findRow(t, every, "name", dead); r["ok"] != false || len(acctest.Strs(t, r["problems"], "problems")) == 0 {
		t.Errorf("testing every notification, one of them out of reach = %v", every)
	}
	if r := findRow(t, every, "name", name); r["ok"] != false {
		t.Errorf("testing every notification, the hook that refuses = %v", every)
	}
	deletes(t, "notification_delete", dead, dead)
	deletes(t, "notification_delete", name, name)
}

// An import list of another Sonarr, which here is this one: nothing is added
// from it, but Sonarr reaches it, signs in and reads its series.
func TestImportListsAndExclusions(t *testing.T) {
	findRow(t, acctest.Rows(t, call(t, "importlist_kinds", nil)["kinds"], "kinds"), "kind", "SonarrImport")

	const name = "This Sonarr"
	t.Cleanup(func() { _, _ = invoke("importlist_delete", map[string]any{"which": name, "confirm": true}) })
	out := call(t, "importlist_add", map[string]any{
		"kind": "SonarrImport", "name": name,
		"set":      map[string]any{"enableAutomaticAdd": false, "searchForMissingEpisodes": false, "rootFolderPath": "/tv", "qualityProfileId": profile, "shouldMonitor": "none", "monitorNewItems": "none", "seriesType": "standard", "seasonFolder": true},
		"settings": map[string]any{"baseUrl": "http://localhost:8989", "apiKey": testenv.Token()},
	})
	rec := record(t, out)
	if acctest.Str(rec["name"]) != name || acctest.Str(rec["rootFolderPath"]) != "/tv" || acctest.NumOr0(rec["qualityProfileId"]) == 0 || rec["enableAutomaticAdd"] != false {
		t.Errorf("the import list added = %v", rec)
	}
	// the key it reads the other Sonarr with is a setting Sonarr does not
	// mark private, and it is not shown all the same
	if own := acctest.Object(t, rec["settings"], "settings"); own["apiKey"] != nil || acctest.Str(own["baseUrl"]) != "http://localhost:8989" {
		t.Errorf("the import list's settings = %v, want its address and not its key", own)
	}
	findRow(t, acctest.Rows(t, call(t, "importlist_list", nil)["items"], "items"), "name", name)
	if r := acctest.Rows(t, call(t, "importlist_test", map[string]any{"which": name})["results"], "results"); len(r) != 1 || r[0]["ok"] != true {
		t.Errorf("testing the import list = %v", r)
	}
	// asked to test every one, Sonarr tests the ones that add automatically,
	// which this one does not: there is nothing to report, and nothing fails
	if all := call(t, "importlist_test", nil); len(acctest.RowsOf(all["results"])) != 0 {
		t.Errorf("testing every import list = %v", all)
	}
	if edited := call(t, "importlist_edit", map[string]any{"which": name, "set": map[string]any{"seasonFolder": false}}); !changedWith(edited, "seasonFolder: true -> false") {
		t.Errorf("importlist_edit = %v", edited)
	}
	if msg := callErr(t, "importlist_edit", map[string]any{"which": name, "set": map[string]any{"qualityProfileId": "No Such Profile"}}); !strings.Contains(msg, profile) {
		t.Errorf("a profile there is not = %s", msg)
	}
	deletes(t, "importlist_delete", name, name)

	// what the lists may not add
	t.Cleanup(func() {
		_, _ = invoke("exclusion_remove", map[string]any{"exclusion": "tvdb:" + itoa(chernobyl.TvdbID)})
	})
	added := call(t, "exclusion_add", map[string]any{"series": "tvdb:" + itoa(chernobyl.TvdbID)})
	if acctest.NumOr0(added["tvdb_id"]) != chernobyl.TvdbID || acctest.Str(added["title"]) != chernobyl.Title {
		t.Errorf("exclusion_add = %v", added)
	}
	findRow(t, acctest.Rows(t, call(t, "exclusion_list", nil)["exclusions"], "exclusions"), "title", chernobyl.Title)
	if lifted := call(t, "exclusion_remove", map[string]any{"exclusion": chernobyl.Title}); acctest.NumOr0(acctest.Object(t, lifted["removed"], "removed")["tvdb_id"]) != chernobyl.TvdbID {
		t.Errorf("exclusion_remove = %v", lifted)
	}
	if msg := callErr(t, "exclusion_remove", map[string]any{"exclusion": chernobyl.Title}); !strings.Contains(msg, "no exclusion") {
		t.Errorf("lifting it again = %s", msg)
	}
}

func TestMetadata(t *testing.T) {
	consumers := acctest.Rows(t, call(t, "metadata_list", nil)["items"], "items")
	if len(consumers) < 3 {
		t.Fatalf("metadata_list = %v", consumers)
	}
	kodi := consumers[slices.IndexFunc(consumers, func(c map[string]any) bool { return strings.Contains(acctest.Str(c["name"]), "Kodi") })]
	name := acctest.Str(kodi["name"])
	if kodi["enable"] == true {
		t.Fatalf("%s is on in a seeded Sonarr: %v", name, kodi)
	}

	// one of what it writes is changed and put back; it is left off, so
	// nothing is written beside the fixtures' files
	own := acctest.Object(t, kodi["settings"], "settings")
	was := own["seasonImages"]
	t.Cleanup(func() {
		_, _ = invoke("metadata_edit", map[string]any{"which": name, "settings": map[string]any{"seasonImages": was}})
	})
	edited := call(t, "metadata_edit", map[string]any{"which": name, "settings": map[string]any{"seasonImages": was != true}})
	if !changedWith(edited, "seasonImages:") || acctest.Object(t, record(t, edited)["settings"], "settings")["seasonImages"] == was {
		t.Errorf("metadata_edit = %v", edited)
	}
	if msg := callErr(t, "metadata_edit", map[string]any{"which": "Plex Pass", "set": map[string]any{"enable": true}}); !strings.Contains(msg, name) {
		t.Errorf("a consumer there is not = %s", msg)
	}
}

func TestDelayProfiles(t *testing.T) {
	list := acctest.Rows(t, call(t, "delayprofile_list", nil)["items"], "items")
	if len(list) == 0 || len(strsOf(list[0]["tags"])) != 0 {
		t.Fatalf("delayprofile_list = %v, want the default profile, which has no tags", list)
	}

	t.Cleanup(func() { _, _ = invoke("tag_delete", map[string]any{"label": "slow", "confirm": true}) })
	out := call(t, "delayprofile_add", map[string]any{"set": map[string]any{"usenetDelay": 30, "preferredProtocol": "torrent", "tags": []any{"slow"}}})
	rec := record(t, out)
	id := itoa(acctest.Num(t, rec["id"], "id"))
	t.Cleanup(func() { _, _ = invoke("delayprofile_delete", map[string]any{"which": id, "confirm": true}) })
	if acctest.NumOr0(rec["usenetDelay"]) != 30 || acctest.Str(rec["preferredProtocol"]) != "torrent" || !slices.Equal(acctest.Strs(t, rec["tags"], "tags"), []string{"slow"}) {
		t.Errorf("the delay profile added = %v", rec)
	}
	if edited := call(t, "delayprofile_edit", map[string]any{"which": id, "set": map[string]any{"torrentDelay": 60}}); !changedWith(edited, "torrentDelay: 0 -> 60") {
		t.Errorf("delayprofile_edit = %v", edited)
	}
	if msg := callErr(t, "delayprofile_edit", map[string]any{"which": id, "set": map[string]any{"delay": 5}}); !strings.Contains(msg, "usenetDelay") {
		t.Errorf("a property it does not have = %s", msg)
	}
	deletes(t, "delayprofile_delete", id, "a delay profile "+id)
}

func TestReleaseProfiles(t *testing.T) {
	const name = "No Cams"
	t.Cleanup(func() { _, _ = invoke("releaseprofile_delete", map[string]any{"which": name, "confirm": true}) })
	out := call(t, "releaseprofile_add", map[string]any{"name": name, "set": map[string]any{"ignored": []any{"CAM", "/\\bTS\\b/"}}})
	if rec := record(t, out); acctest.Str(rec["name"]) != name || rec["enabled"] != true || len(strsOf(rec["ignored"])) != 2 {
		t.Errorf("the release profile added = %v", rec)
	}
	findRow(t, acctest.Rows(t, call(t, "releaseprofile_list", nil)["items"], "items"), "name", name)
	if edited := call(t, "releaseprofile_edit", map[string]any{"which": name, "set": map[string]any{"required": []any{"WEB"}, "enabled": false}}); !changedWith(edited, "required:") || !changedWith(edited, "enabled: true -> false") {
		t.Errorf("releaseprofile_edit = %v", edited)
	}
	deletes(t, "releaseprofile_delete", name, name)
}

func TestRemotePathMappings(t *testing.T) {
	out := call(t, "remotepathmapping_add", map[string]any{"set": map[string]any{"host": "seedbox.example", "remotePath": "/home/user/done/", "localPath": "/downloads/"}})
	rec := record(t, out)
	id := itoa(acctest.Num(t, rec["id"], "id"))
	t.Cleanup(func() { _, _ = invoke("remotepathmapping_delete", map[string]any{"which": id, "confirm": true}) })
	if acctest.Str(rec["host"]) != "seedbox.example" || acctest.Str(rec["localPath"]) != "/downloads/" {
		t.Errorf("the mapping added = %v", rec)
	}
	list := acctest.Rows(t, call(t, "remotepathmapping_list", nil)["items"], "items")
	findRow(t, list, "host", "seedbox.example")
	if edited := call(t, "remotepathmapping_edit", map[string]any{"which": id, "set": map[string]any{"remotePath": "/home/user/complete/"}}); !changedWith(edited, "remotePath:") {
		t.Errorf("remotepathmapping_edit = %v", edited)
	}
	deletes(t, "remotepathmapping_delete", id, "a remote path mapping "+id)
}

// A custom format made, scored by a profile, changed and deleted; and the
// profile tools on a copy of the fixtures' profile.
func TestCustomFormatsAndProfiles(t *testing.T) {
	const format, copied = "Suite HEVC", "Suite Copy"
	t.Cleanup(func() {
		_, _ = invoke("profile_delete", map[string]any{"profile": copied, "confirm": true})
		_, _ = invoke("customformat_delete", map[string]any{"which": format, "confirm": true})
	})

	out := call(t, "customformat_add", map[string]any{"name": format, "set": map[string]any{"specifications": []any{
		map[string]any{"implementation": "ReleaseTitleSpecification", "name": "hevc", "required": true, "settings": map[string]any{"value": `\b(hevc|x265)\b`}},
	}}})
	rec := record(t, out)
	specs := acctest.Rows(t, rec["specifications"], "specifications")
	if acctest.Str(rec["name"]) != format || len(specs) != 1 || specs[0]["required"] != true || acctest.Str(acctest.Object(t, specs[0]["settings"], "settings")["value"]) != `\b(hevc|x265)\b` {
		t.Errorf("the format added = %v", rec)
	}
	if msg := callErr(t, "customformat_add", map[string]any{"name": "x", "set": map[string]any{"specifications": []any{map[string]any{"implementation": "Codec"}}}}); !strings.Contains(msg, "ReleaseTitleSpecification") {
		t.Errorf("a kind of condition there is not = %s", msg)
	}

	created := call(t, "profile_create", map[string]any{"name": copied, "copy_of": profile})
	made := acctest.Object(t, created["profile"], "profile")
	if acctest.Str(made["name"]) != copied || len(acctest.Strs(t, made["allowed_qualities"], "allowed_qualities")) == 0 {
		t.Errorf("profile_create = %v", created)
	}
	edited := call(t, "profile_edit", map[string]any{"profile": copied, "upgrade_allowed": true, "allowed_qualities": []any{"HDTV-720p", "WEBDL-1080p"}, "cutoff": "WEBDL-1080p", "min_custom_format_score": 10, "custom_format_scores": map[string]any{format: 250}})
	now := acctest.Object(t, edited["profile"], "profile")
	// the profile groups WEBDL-1080p with WEBRip-1080p, so the two are allowed
	// together and cut off at as the group
	if acctest.Str(now["cutoff"]) != "WEB 1080p" || !slices.Equal(acctest.Strs(t, now["allowed_qualities"], "allowed_qualities"), []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-720p"}) || acctest.NumOr0(now["min_custom_format_score"]) != 10 || acctest.NumOr0(acctest.Object(t, now["custom_format_scores"], "scores")[format]) != 250 || len(strsOf(edited["changed"])) < 3 {
		t.Errorf("profile_edit = %v", edited)
	}
	// the format list shows the profile that scores it
	listed := findRow(t, acctest.Rows(t, call(t, "customformat_list", nil)["custom_formats"], "custom_formats"), "name", format)
	if acctest.NumOr0(acctest.Object(t, listed["scores"], "scores")[copied]) != 250 {
		t.Errorf("customformat_list = %v", listed)
	}
	if msg := callErr(t, "profile_edit", map[string]any{"profile": copied, "allow": []any{"Betamax"}}); !strings.Contains(msg, "WEBDL-1080p") {
		t.Errorf("a quality there is not = %s", msg)
	}
	if msg := callErr(t, "profile_edit", map[string]any{"profile": copied, "custom_format_scores": map[string]any{"Dolby Vision": 5}}); !strings.Contains(msg, format) {
		t.Errorf("a format there is not = %s", msg)
	}

	if changed := call(t, "customformat_edit", map[string]any{"which": format, "set": map[string]any{"includeCustomFormatWhenRenaming": true}}); !changedWith(changed, "includeCustomFormatWhenRenaming:") {
		t.Errorf("customformat_edit = %v", changed)
	}

	// a profile is deleted once confirmed, and one a series is on is refused
	would := call(t, "profile_delete", map[string]any{"profile": copied})
	if acctest.Str(would["would_delete"]) != copied || acctest.Str(would["deleted"]) != "" {
		t.Errorf("profile_delete without confirm = %v", would)
	}
	if done := call(t, "profile_delete", map[string]any{"profile": copied, "confirm": true}); acctest.Str(done["deleted"]) != copied {
		t.Errorf("profile_delete = %v", done)
	}
	if msg := callErr(t, "profile_delete", map[string]any{"profile": profile, "confirm": true}); !strings.Contains(strings.ToLower(msg), "in use") {
		t.Errorf("deleting a profile series are on = %s", msg)
	}
	deletes(t, "customformat_delete", format, format)
}

func TestAutoTagsAndFilters(t *testing.T) {
	const rule, filter = "Suite Anime", "Suite Monitored"
	t.Cleanup(func() {
		_, _ = invoke("autotag_delete", map[string]any{"which": rule, "confirm": true})
		_, _ = invoke("filter_delete", map[string]any{"which": filter, "confirm": true})
		_, _ = invoke("tag_delete", map[string]any{"label": "auto-anime", "confirm": true})
	})

	out := call(t, "autotag_add", map[string]any{"name": rule, "set": map[string]any{"tags": []any{"auto-anime"}, "specifications": []any{
		map[string]any{"implementation": "GenreSpecification", "name": "Anime", "settings": map[string]any{"value": []any{"Anime"}}},
	}}})
	if rec := record(t, out); acctest.Str(rec["name"]) != rule || !slices.Equal(acctest.Strs(t, rec["tags"], "tags"), []string{"auto-anime"}) || len(acctest.Rows(t, rec["specifications"], "specifications")) != 1 {
		t.Errorf("the auto tag added = %v", rec)
	}
	findRow(t, acctest.Rows(t, call(t, "autotag_list", nil)["items"], "items"), "name", rule)
	if edited := call(t, "autotag_edit", map[string]any{"which": rule, "set": map[string]any{"removeTagsAutomatically": true}}); !changedWith(edited, "removeTagsAutomatically:") {
		t.Errorf("autotag_edit = %v", edited)
	}
	deletes(t, "autotag_delete", rule, rule)

	saved := call(t, "filter_add", map[string]any{"name": filter, "set": map[string]any{"type": "series", "filters": []any{map[string]any{"key": "monitored", "value": []any{true}, "type": "equal"}}}})
	if rec := record(t, saved); acctest.Str(rec["label"]) != filter || acctest.Str(rec["type"]) != "series" {
		t.Errorf("the filter saved = %v", rec)
	}
	findRow(t, acctest.Rows(t, call(t, "filter_list", nil)["items"], "items"), "label", filter)
	if edited := call(t, "filter_edit", map[string]any{"which": filter, "set": map[string]any{"type": "calendar"}}); !changedWith(edited, `type: "series" -> "calendar"`) {
		t.Errorf("filter_edit = %v", edited)
	}
	deletes(t, "filter_delete", filter, filter)
}

func TestQualityDefinitionsAndLanguages(t *testing.T) {
	list := acctest.Rows(t, call(t, "qualitydefinition_list", nil)["definitions"], "definitions")
	web := findRow(t, list, "quality", "WEBDL-1080p")
	was := web["max_size"]
	t.Cleanup(func() {
		_, _ = invoke("qualitydefinition_edit", map[string]any{"quality": "WEBDL-1080p", "max_size": was})
	})

	edited := call(t, "qualitydefinition_edit", map[string]any{"quality": "webdl-1080p", "max_size": 123.5})
	if acctest.Object(t, edited["definition"], "definition")["max_size"] != 123.5 || len(strsOf(edited["changed"])) != 1 {
		t.Errorf("qualitydefinition_edit = %v", edited)
	}
	if msg := callErr(t, "qualitydefinition_edit", map[string]any{"quality": "Betamax", "max_size": 1}); !strings.Contains(msg, "WEBDL-1080p") {
		t.Errorf("a quality there is not = %s", msg)
	}

	findRow(t, acctest.Rows(t, call(t, "language_list", nil)["languages"], "languages"), "name", "English")
}

// Settings changed and put back: one that holds, the host's waiting for
// confirm, and the names a section has when one is not among them.
func TestConfigEdit(t *testing.T) {
	media := acctest.Object(t, call(t, "config_get", map[string]any{"section": "mediamanagement"})["settings"], "settings")
	was := acctest.NumOr0(media["recycleBinCleanupDays"])
	t.Cleanup(func() {
		_, _ = invoke("config_edit", map[string]any{"section": "mediamanagement", "set": map[string]any{"recycleBinCleanupDays": was}})
	})
	out := call(t, "config_edit", map[string]any{"section": "media_management", "set": map[string]any{"recycle_bin_cleanup_days": was + 3}})
	if !changedWith(out, "recycleBinCleanupDays: "+itoa(was)+" -> "+itoa(was+3)) || len(strsOf(out["not_kept"])) != 0 || acctest.NumOr0(acctest.Object(t, out["settings"], "settings")["recycleBinCleanupDays"]) != was+3 {
		t.Errorf("config_edit = %v", out)
	}
	if now := acctest.Object(t, call(t, "config_get", map[string]any{"section": "mediamanagement"})["settings"], "settings"); acctest.NumOr0(now["recycleBinCleanupDays"]) != was+3 {
		t.Errorf("the setting read back = %v", now["recycleBinCleanupDays"])
	}

	// a naming format is tried before it is set: the examples follow the
	// change, and the settings do not
	preview := call(t, "config_naming_preview", map[string]any{"set": map[string]any{"seriesFolderFormat": "{Series TitleYear} [preview]"}})
	examples := acctest.Object(t, preview["examples"], "examples")
	if folder := acctest.Str(examples["seriesFolderExample"]); !strings.HasSuffix(folder, "[preview]") || acctest.Str(examples["singleEpisodeExample"]) == "" {
		t.Errorf("config_naming_preview = %v", preview)
	}
	if naming := acctest.Object(t, call(t, "config_get", map[string]any{"section": "naming"})["settings"], "settings"); strings.Contains(acctest.Str(naming["seriesFolderFormat"]), "[preview]") {
		t.Errorf("the preview was kept: %v", naming["seriesFolderFormat"])
	}
	if plain := acctest.Object(t, call(t, "config_naming_preview", nil)["examples"], "examples"); strings.Contains(acctest.Str(plain["seriesFolderExample"]), "[preview]") {
		t.Errorf("the current settings' examples = %v", plain)
	}
	if msg := callErr(t, "config_edit", map[string]any{"section": "mediamanagement", "set": map[string]any{"recycleDays": 3}}); !strings.Contains(msg, "recycleBinCleanupDays") {
		t.Errorf("a setting it does not have = %s", msg)
	}
	if msg := callErr(t, "config_edit", map[string]any{"section": "mediamanagement", "set": map[string]any{"recycleBinCleanupDays": "soon"}}); !strings.Contains(msg, "not a string") {
		t.Errorf("a value of the wrong kind = %s", msg)
	}

	// the host's settings wait for confirm, and say what they would cut
	host := acctest.Object(t, call(t, "config_get", map[string]any{"section": "host"})["settings"], "settings")
	instance := acctest.Str(host["instanceName"])
	would := call(t, "config_edit", map[string]any{"section": "host", "set": map[string]any{"instanceName": "Suite Sonarr", "port": 9999}})
	if len(strsOf(would["would_change"])) != 2 || len(strsOf(would["changed"])) != 0 || !strings.Contains(acctest.Str(would["warning"]), "port") {
		t.Errorf("the host without confirm = %v", would)
	}
	if still := acctest.Object(t, call(t, "config_get", map[string]any{"section": "host"})["settings"], "settings"); acctest.Str(still["instanceName"]) != instance {
		t.Fatalf("the host changed without confirm: %v", still["instanceName"])
	}
	t.Cleanup(func() {
		_, _ = invoke("config_edit", map[string]any{"section": "host", "set": map[string]any{"instanceName": instance}, "confirm": true})
	})
	// Sonarr 4.0.20 refuses to save the host's settings while Allowed Hosts
	// is empty and signing in is not required, which is how a new one stands:
	// the refusal names the setting, and the hosts the suite reaches Sonarr by
	// and Sonarr reaches itself by keep everything allowed. There is no
	// putting it back to empty, which Sonarr refuses.
	if acctest.Str(host["allowedHosts"]) == "" {
		if msg := callErr(t, "config_edit", map[string]any{"section": "host", "set": map[string]any{"instanceName": "Suite Sonarr"}, "confirm": true}); !strings.Contains(msg, "Allowed Hosts") {
			t.Errorf("the host saved with no hosts allowed = %s", msg)
		}
		call(t, "config_edit", map[string]any{"section": "host", "set": map[string]any{"allowedHosts": "127.0.0.1,localhost," + testenv.Host()}, "confirm": true})
	}
	done := call(t, "config_edit", map[string]any{"section": "host", "set": map[string]any{"instanceName": "Suite Sonarr"}, "confirm": true})
	settings := acctest.Object(t, done["settings"], "settings")
	if !changedWith(done, "instanceName:") || acctest.Str(settings["instanceName"]) != "Suite Sonarr" {
		t.Errorf("the host with confirm = %v", done)
	}
	if _, shown := settings["apiKey"]; shown {
		t.Errorf("the host's API key is answered: %v", settings)
	}
}

func TestTagRename(t *testing.T) {
	t.Cleanup(func() {
		_, _ = invoke("tag_delete", map[string]any{"label": "suite-old", "confirm": true})
		_, _ = invoke("tag_delete", map[string]any{"label": "suite-new", "confirm": true})
	})
	call(t, "tag_create", map[string]any{"label": "suite-old"})
	if out := call(t, "tag_rename", map[string]any{"label": "Suite-Old", "new_label": "Suite-New"}); acctest.Str(out["label"]) != "suite-new" || acctest.Str(out["was"]) != "suite-old" {
		t.Errorf("tag_rename = %v", out)
	}
	if msg := callErr(t, "tag_rename", map[string]any{"label": "suite-old", "new_label": "x"}); !strings.Contains(msg, "no tag") {
		t.Errorf("renaming a tag that is gone = %s", msg)
	}

	// a delete names the tag and leaves it without confirm
	would := call(t, "tag_delete", map[string]any{"label": "suite-new"})
	if acctest.Str(would["would_delete"]) != "suite-new" || acctest.Str(would["deleted"]) != "" {
		t.Errorf("tag_delete without confirm = %v", would)
	}
	findRow(t, acctest.Rows(t, call(t, "tag_list", nil)["tags"], "tags"), "label", "suite-new")
	if done := call(t, "tag_delete", map[string]any{"label": "suite-new", "confirm": true}); acctest.Str(done["deleted"]) != "suite-new" {
		t.Errorf("tag_delete = %v", done)
	}
}

// One change across two series, and back.
func TestSeriesBulkEdit(t *testing.T) {
	both := []any{chernobyl.Title, "tvdb:" + itoa(firefly.TvdbID)}
	t.Cleanup(func() {
		_, _ = invoke("series_bulk_edit", map[string]any{"series": both, "remove_tags": []any{"bulk"}, "season_folder": true})
		_, _ = invoke("tag_delete", map[string]any{"label": "bulk", "confirm": true})
	})

	out := call(t, "series_bulk_edit", map[string]any{"series": both, "add_tags": []any{"bulk"}, "season_folder": false})
	rows := acctest.Rows(t, out["series"], "series")
	if len(rows) != 2 || len(acctest.Strs(t, out["changed"], "changed")) != 2 {
		t.Fatalf("series_bulk_edit = %v", out)
	}
	for _, title := range []string{chernobyl.Title, firefly.Title} {
		got := call(t, "series_get", map[string]any{"series": title})
		if !slices.Contains(acctest.Strs(t, got["tags"], "tags"), "bulk") || got["season_folder"] != false {
			t.Errorf("%s after the bulk edit: tags %v, season folders %v", title, got["tags"], got["season_folder"])
		}
	}

	back := call(t, "series_bulk_edit", map[string]any{"series": both, "remove_tags": []any{"bulk"}, "season_folder": true})
	if len(acctest.Rows(t, back["series"], "series")) != 2 {
		t.Errorf("the bulk edit back = %v", back)
	}
	if got := call(t, "series_get", map[string]any{"series": chernobyl.Title}); slices.Contains(strsOf(got["tags"]), "bulk") || got["season_folder"] != true {
		t.Errorf("%s after the bulk edit back: tags %v, season folders %v", chernobyl.Title, got["tags"], got["season_folder"])
	}
	if msg := callErr(t, "series_bulk_edit", map[string]any{"series": both}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("a bulk edit of nothing = %s", msg)
	}
	if msg := callErr(t, "series_bulk_edit", map[string]any{"series": []any{"No Such Show"}, "monitored": true}); !strings.Contains(msg, "no series") {
		t.Errorf("a bulk edit of a series there is not = %s", msg)
	}
}

// Sonarr itself: this one has never been backed up, the suite's broken
// indexer has it logging errors, and a release is offered to it.
func TestAuditServer(t *testing.T) {
	// an indexer nothing listens on: Sonarr logs a refused connection as an
	// error, where one that answers 500 gets only a warning
	t.Cleanup(func() { _, _ = invoke("indexer_delete", map[string]any{"which": "Silent Indexer", "confirm": true}) })
	call(t, "indexer_add", map[string]any{"kind": "Newznab", "name": "Silent Indexer", "settings": map[string]any{"baseUrl": "http://127.0.0.1:9", "apiKey": "x"}, "force": true})
	call(t, "task_run", map[string]any{"task": "RssSync"})

	out := audit(t, "audit_server", nil)
	if f := only(t, out, "problem", "no recent backup"); !strings.Contains(acctest.Str(f["detail"]), "no backup") || !strings.Contains(acctest.Str(f["fix"]), "backup_create") {
		t.Errorf("no backup = %v", f)
	}
	if f := only(t, out, "subject", "Newznab"); acctest.Str(f["problem"]) != "errors in the log" || !strings.Contains(acctest.Str(f["detail"]), "in the last day") || !strings.Contains(acctest.Str(f["detail"]), "refused") {
		t.Errorf("the silent indexer's errors = %v", f)
	}
	none(t, out, "problem", "update available")

	testenv.OfferUpdates(arrserver.Update{Version: "99.0.0.1", New: []string{"everything"}})
	t.Cleanup(func() { testenv.OfferUpdates() })
	offered := audit(t, "audit_server", nil)
	if f := only(t, offered, "problem", "update available"); acctest.Str(f["subject"]) != "99.0.0.1" || !strings.Contains(acctest.Str(f["detail"]), testenv.SonarrVersion()+" is running") || !strings.Contains(acctest.Str(f["fix"]), "server_updates") {
		t.Errorf("the release offered = %v", f)
	}
}

func TestBackups(t *testing.T) {
	made := call(t, "backup_create", nil)
	backup := acctest.Object(t, made["backup"], "backup")
	name := acctest.Str(backup["name"])
	if name == "" || acctest.Str(backup["kind"]) != "manual" {
		t.Fatalf("backup_create = %v", made)
	}
	t.Cleanup(func() { _, _ = invoke("backup_delete", map[string]any{"backup": name, "confirm": true}) })
	findRow(t, acctest.Rows(t, call(t, "backup_list", nil)["backups"], "backups"), "name", name)

	// a restore says what it would put back, and is not confirmed here: it
	// would take the library back under the rest of the suite
	would := call(t, "backup_restore", map[string]any{"backup": name})
	if acctest.Str(would["would_restore"]) != name || acctest.Str(would["restored"]) != "" || !strings.Contains(acctest.Str(would["loses"]), "restarted") {
		t.Errorf("backup_restore without confirm = %v", would)
	}
	if msg := callErr(t, "backup_restore", map[string]any{"backup": "no-such-backup.zip", "confirm": true}); !strings.Contains(msg, "no backup") {
		t.Errorf("restoring a backup there is not = %s", msg)
	}

	gone := call(t, "backup_delete", map[string]any{"backup": name})
	if acctest.Str(gone["would_delete"]) != name || acctest.Str(gone["deleted"]) != "" {
		t.Errorf("backup_delete without confirm = %v", gone)
	}
	if done := call(t, "backup_delete", map[string]any{"backup": name, "confirm": true}); acctest.Str(done["deleted"]) != name {
		t.Errorf("backup_delete = %v", done)
	}
	if left := acctest.Rows(t, call(t, "backup_list", nil)["backups"], "backups"); slices.ContainsFunc(left, func(b map[string]any) bool { return acctest.Str(b["name"]) == name }) {
		t.Errorf("the backup is still listed: %v", left)
	}
}

func TestSystemTools(t *testing.T) {
	disks := acctest.Rows(t, call(t, "server_disk_space", nil)["disks"], "disks")
	if len(disks) == 0 || acctest.Str(disks[0]["path"]) == "" || acctest.Str(disks[0]["total"]) == "" {
		t.Errorf("server_disk_space = %v", disks)
	}

	// the releases the stand-in update server lists, the one running among them
	updates := acctest.Rows(t, call(t, "server_updates", nil)["updates"], "updates")
	at := slices.IndexFunc(updates, func(u map[string]any) bool { return u["installed"] == true })
	if at < 0 {
		t.Fatalf("no release is said to be the one running: %v", updates)
	}
	if running := updates[at]; acctest.Str(running["version"]) == "" || acctest.Str(running["released"]) == "" || len(acctest.Strs(t, running["fixed"], "fixed")) == 0 {
		t.Errorf("the release running = %v", running)
	}

	files := acctest.Rows(t, call(t, "logfile_list", nil)["files"], "files")
	findRow(t, files, "filename", "sonarr.txt")
	read := call(t, "logfile_read", map[string]any{"lines": 5})
	if lines := acctest.Strs(t, read["lines"], "lines"); len(lines) == 0 || len(lines) > 5 || acctest.Num(t, read["matched"], "matched") < len(lines) {
		t.Errorf("logfile_read = %v", read)
	}
	only := call(t, "logfile_read", map[string]any{"filename": "sonarr.txt", "contains": "|info|", "lines": 3})
	for _, line := range acctest.Strs(t, only["lines"], "lines") {
		if !strings.Contains(strings.ToLower(line), "|info|") {
			t.Errorf("a line without the text asked for: %q", line)
		}
	}
	if msg := callErr(t, "logfile_read", map[string]any{"filename": "nothing.txt"}); !strings.Contains(msg, "no log file") {
		t.Errorf("a log file there is not = %s", msg)
	}

	top := call(t, "filesystem_list", map[string]any{"path": "/tv"})
	findRow(t, acctest.Rows(t, top["entries"], "entries"), "name", firefly.Folder)
	inside := call(t, "filesystem_list", map[string]any{"path": "/tv/" + firefly.Folder, "files": true, "limit": 2})
	if acctest.Num(t, inside["total"], "total") == 0 || len(acctest.Rows(t, inside["entries"], "entries")) > 2 {
		t.Errorf("filesystem_list inside a series = %v", inside)
	}

	// a command waiting its turn is cancelled. Sonarr cannot stop one that has
	// started, and starts one the moment it has a hand free, so its hands are
	// filled first: the indexer is held back, and a search is asked for and
	// then cancelled, one episode after another. A search that had already
	// started keeps a hand busy waiting on the indexer, until one has to wait
	// its turn, and that one is cancelled. What the indexer offers them is a
	// quality the profile does not take, so a search finds its answer at the
	// first asking and grabs nothing
	const searches = 9
	offered := indexer.Releases()
	unwanted := slices.Clone(offered)
	for n := 1; n <= searches; n++ {
		unwanted = append(unwanted, release("Severance.S02E0"+itoa(n)+".DVDRip.x264-FAKE", severance.TvdbID, 2, n, 0.3, newznab.CategoryTVSD))
	}
	if err := indexer.SetReleases(unwanted...); err != nil {
		t.Fatal(err)
	}
	indexer.SetDelay(3 * time.Second)
	t.Cleanup(func() {
		indexer.SetDelay(0)
		if err := indexer.SetReleases(offered...); err != nil {
			t.Error(err)
		}
	})

	cancelled, last := false, ""
	for n := 1; n <= searches && !cancelled; n++ {
		waiting := acctest.Object(t, call(t, "episode_search", map[string]any{"series": severance.Title, "episodes": []any{"S02E0" + itoa(n)}, "wait_seconds": -1})["command"], "command")

		out, err := invoke("command_cancel", map[string]any{"command_id": acctest.Num(t, waiting["command_id"], "command_id")})
		if err != nil {
			last = err.Error()

			continue
		}
		if got := acctest.Object(t, out["cancelled"], "cancelled"); acctest.NumOr0(got["command_id"]) != acctest.NumOr0(waiting["command_id"]) || acctest.Str(got["status"]) != "queued" {
			t.Errorf("command_cancel = %v", out)
		}
		cancelled = true
	}
	indexer.SetDelay(0)

	if !cancelled {
		t.Errorf("of %d searches asked for with the indexer held back, none had to wait its turn; the last was refused with: %s", searches, last)
	}
	// let what was started finish before the next test reads the library
	eventually(t, "command_list", nil, "the refresh finished", func(out map[string]any) bool {
		return !slices.ContainsFunc(acctest.RowsOf(out["commands"]), func(c map[string]any) bool {
			return acctest.Str(c["status"]) == "started" || acctest.Str(c["status"]) == "queued"
		})
	})

	// a command that has finished cannot be cancelled, and one there is not
	// is said so
	ran := call(t, "task_run", map[string]any{"task": "CheckHealth"})
	if msg := callErr(t, "command_cancel", map[string]any{"command_id": acctest.Num(t, ran["command_id"], "command_id")}); !strings.Contains(msg, "already") {
		t.Errorf("cancelling a finished command = %s", msg)
	}
	if msg := callErr(t, "command_cancel", map[string]any{"command_id": 987654321}); !strings.Contains(msg, "no command") {
		t.Errorf("cancelling a command there is not = %s", msg)
	}

	// restarting and shutting down say what they would do, and do nothing
	// without confirm
	for _, tool := range []string{"server_restart", "server_shutdown"} {
		if out := call(t, tool, nil); acctest.Str(out["would"]) == "" || acctest.Str(out["done"]) != "" {
			t.Errorf("%s without confirm = %v", tool, out)
		}
	}
	if info := call(t, "server_info", nil); acctest.Str(info["sonarr_version"]) == "" {
		t.Errorf("Sonarr after the two that were not confirmed = %v", info)
	}
}
