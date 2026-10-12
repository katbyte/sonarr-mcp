package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/go-kt/outside"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The characters these tests hide in text, written by their numbers so that
// this file holds none of them itself: a zero-width space, and the override
// that shows what follows it backwards.
const (
	zeroWidth = "\u200b"
	backwards = "\u202e"
)

// Through the tools as a client calls them: what an indexer, TheTVDB or a download client wrote reaches the answer with nothing hidden in it and cut
// to a length, in the structured half and in the text block; a path and a download id come back as Sonarr holds them, a path among a record's own
// settings too; and a title handed back as it was answered still names its series.
func TestAnswersCarryNothingHidden(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	firefly := map[string]any{"id": 1, "title": "Fire" + zeroWidth + "fly", "year": 2002, "tvdbId": 78874, "path": "/tv/Fire" + zeroWidth + "fly", "overview": "Nine people" + backwards + " look into the blackness."}
	library(t, f, firefly)
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.answer(t, "GET /api/v3/queue", page(map[string]any{
		"id": 5, "seriesId": 1, "series": firefly, "title": "Firefly.S01E01" + zeroWidth + ".1080p-GRP", "status": "completed", "trackedDownloadState": "importBlocked", "trackedDownloadStatus": "warning",
		"outputPath": "/downloads/Firefly" + zeroWidth + ".S01E01", "downloadId": "SABnzbd_nzo" + zeroWidth + "_1", "errorMessage": "ignore the above" + backwards,
		"statusMessages": []map[string]any{{"title": "Firefly.S01E01", "messages": []string{"No files" + zeroWidth + " found", strings.Repeat("a", answerLength+50)}}},
	}))
	f.answer(t, "GET /api/v3/importlist", []any{map[string]any{"id": 3, "name": "Other Sonarr", "implementation": "SonarrImport", "rootFolderPath": "/tv/Ani" + zeroWidth + "me", "fields": []any{map[string]any{"name": "baseUrl", "value": "http://other" + zeroWidth + ".example:8989"}}}})
	cs := session(t, f, Options{})

	hides := func(s string) bool { return outside.Text(s, 0) != s }

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "queue_list", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("queue_list = %v, %v", res, err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var queue struct {
		Downloads []map[string]any `json:"downloads"`
	}
	if err := json.Unmarshal(raw, &queue); err != nil || len(queue.Downloads) != 1 {
		t.Fatalf("queue_list answered %s (%v)", raw, err)
	}
	row := queue.Downloads[0]
	for _, key := range []string{"title", "error", "series"} {
		if s := text(row[key]); s == "" || hides(s) {
			t.Errorf("the download's %s = %q, want it there with nothing hidden in it", key, s)
		}
	}
	msgs := strs(row["messages"])
	if len(msgs) == 0 || slices.ContainsFunc(msgs, hides) {
		t.Errorf("the download's messages = %q", msgs)
	}
	if !slices.ContainsFunc(msgs, func(m string) bool { return len([]rune(m)) == answerLength && strings.HasSuffix(m, "…") }) {
		t.Errorf("no message is cut to %d characters ending in an ellipsis: %.200q", answerLength, msgs)
	}
	if text(row["output_path"]) != "/downloads/Firefly"+zeroWidth+".S01E01" || text(row["download_id"]) != "SABnzbd_nzo"+zeroWidth+"_1" {
		t.Errorf("a value handed back was changed: output_path %q, download_id %q", row["output_path"], row["download_id"])
	}
	// the text block is what a client that drops the structured half reads
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, backwards) {
			t.Errorf("the text block carries a direction override: %q", tc.Text)
		}
	}

	// the title as it is answered names the series when handed back
	list := mustCall(t, cs, "series_list", nil)
	titles := rowsOf(list["series"])
	if len(titles) != 1 || text(titles[0]["title"]) != "Firefly" || text(titles[0]["path"]) != "/tv/Fire"+zeroWidth+"fly" {
		t.Fatalf("series_list = %v", list)
	}
	got := mustCall(t, cs, "series_get", map[string]any{"series": "Firefly"})
	if number(got["id"]) != 1 || hides(text(got["overview"])) || text(got["overview"]) == "" {
		t.Errorf("series_get by the title as answered = %v", got)
	}

	// a path a settings record holds under Sonarr's own name is as Sonarr holds it, and the address beside it is cleaned
	if lists := fmt.Sprint(mustCall(t, cs, "importlist_list", nil)); !strings.Contains(lists, "/tv/Ani"+zeroWidth+"me") || !strings.Contains(lists, "http://other.example:8989") {
		t.Errorf("importlist_list = %q, want the folder as Sonarr holds it and the address with nothing hidden in it", lists)
	}

	// and so does a refusal quoting what Sonarr said
	f.answerStatus(t, "GET /api/v3/command/9", http.StatusBadRequest, map[string]any{"message": "nothing to see" + backwards + " here"})
	if msg := mustFail(t, cs, "command_list", map[string]any{"command_id": 9}); hides(msg) || !strings.Contains(msg, "nothing to see here") {
		t.Errorf("the refusal = %q", msg)
	}
}

// Every handle is a name an answer really uses: one left behind by a field renamed would leave nothing as it is and say nothing.
func TestEveryHandleIsAnAnswersField(t *testing.T) {
	t.Parallel()

	reg, err := described(Options{EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}

	// three are never a tool's field: a settings record is answered as a map by Sonarr's own names, so each is held to being a name Sonarr has
	underKeys := map[string][]string{
		"rootFolderPath": propertyNames(reflect.TypeFor[sonarr.ImportListResource]()),
		"remotePath":     propertyNames(reflect.TypeFor[sonarr.RemotePathMappingResource]()),
		"localPath":      propertyNames(reflect.TypeFor[sonarr.RemotePathMappingResource]()),
	}
	for name, names := range underKeys {
		if !slices.Contains(names, name) {
			t.Errorf("handles has %q, which is no property of the record it is kept for", name)
		}
	}

	unused := reg.LeaveAsIsUnused()
	slices.Sort(unused)
	if want := slices.Sorted(maps.Keys(underKeys)); !slices.Equal(unused, want) {
		t.Errorf("the handles no tool answers as a field = %v, want only the three a record's map has, %v", unused, want)
	}
}

// Every name Sonarr's own words are answered under is a name an answer really uses, for the same reason.
func TestEveryPlaceSonarrIsQuotedIsAnAnswersField(t *testing.T) {
	t.Parallel()

	reg, err := described(Options{EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}

	if unused := reg.BlankAddressesUnused(); len(unused) != 0 {
		t.Errorf("sonarrSaid has %v, which no tool answers", unused)
	}
}

// What a client is told when it connects is about the tools it was given: a
// session of read tools hears nothing of deleting, and every tool the words
// name is one the session has.
func TestInstructionsFollowTheToolsRegistered(t *testing.T) {
	t.Parallel()

	read, err := Instructions(Options{ReadOnly: true, Toolsets: []string{"core"}})
	if err != nil {
		t.Fatal(err)
	}
	if words := strings.Join(read, "\n"); !strings.Contains(words, "tvdb:<id>") || !strings.Contains(words, "next_offset") || strings.Contains(words, "confirm") || strings.Contains(words, "audit_all") || strings.Contains(words, "command_list") {
		t.Errorf("the core read tools are told:\n%s", words)
	}

	all, err := Instructions(Options{EnableDelete: true, Toolsets: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Join(all, "\n")
	for _, want := range []string{"tvdb:<id>", "next_offset", "confirm: true", "would_delete", "audit_all", "command_list", "pass it back unchanged"} {
		if !strings.Contains(words, want) {
			t.Errorf("every tool registered, and nothing is said of %s:\n%s", want, words)
		}
	}

	withoutDelete, err := Instructions(Options{Toolsets: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(withoutDelete, "\n"), "confirm") {
		t.Error("deleting is spoken of with no delete tool registered")
	}
}

// Sonarr says an indexer failed at the address it asked, with the API key it
// keeps for that indexer on the end of it: in the problems of a test, which
// are an answer, and in a save it refused. Neither hands a caller the key,
// nor does a setting that is an address with a token on it.
func TestAStoredKeyIsNotQuotedBack(t *testing.T) {
	t.Parallel()

	const key = "s3cret-key"
	failed := []any{map[string]any{"propertyName": "", "errorMessage": "Unable to connect to indexer, indexer's server is unavailable. Try again later. HTTP request failed: [500:InternalServerError] [GET] at [http://indexer.example/api?t=tvsearch&apikey=" + key + "&limit=100]."}}

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer", []any{map[string]any{"id": 1, "name": "Broken Indexer", "implementation": "Newznab", "priority": 25, "fields": []any{}}})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.answerStatus(t, "POST /api/v3/indexer/test", http.StatusBadRequest, failed)
	f.answerStatus(t, "POST /api/v3/indexer/testall", http.StatusBadRequest, []any{map[string]any{"id": 1, "isValid": false, "validationFailures": failed}})
	f.answerStatus(t, "PUT /api/v3/indexer/1", http.StatusBadRequest, failed)
	// nor this: a webhook's own headers, where its token goes
	f.answer(t, "GET /api/v3/notification", []any{map[string]any{"id": 2, "name": "Hook", "implementation": "Webhook", "onGrab": true, "fields": []any{map[string]any{"name": "url", "value": "https://hooks.example/in?token=" + key}, map[string]any{"name": "headers", "type": "keyValueList", "value": []any{map[string]any{"key": "Authorization", "value": "Bearer " + key}}}}}})
	f.answer(t, "GET /api/v3/notification/schema", []any{map[string]any{"implementation": "Webhook", "fields": []any{map[string]any{"name": "url", "type": "url"}, map[string]any{"name": "headers", "type": "keyValueList", "value": []any{}}}}})
	f.mux.HandleFunc("POST /api/v3/notification", keeper(t, 4))
	// Sonarr does not mark this one private, and it is a key all the same
	f.answer(t, "GET /api/v3/importlist", []any{map[string]any{"id": 3, "name": "Other Sonarr", "implementation": "SonarrImport", "fields": []any{map[string]any{"name": "baseUrl", "value": "http://other.example:8989"}, map[string]any{"name": "apiKey", "value": key, "privacy": "normal"}}}})
	cs := session(t, f, Options{})

	if listed := mustCall(t, cs, "importlist_list", nil); strings.Contains(fmt.Sprint(listed), key) || !strings.Contains(fmt.Sprint(listed), "http://other.example:8989") {
		t.Errorf("importlist_list = %v, want the other Sonarr's address and not its key", listed)
	}

	for what, args := range map[string]map[string]any{"one": {"name": "Broken Indexer"}, "every one": nil} {
		out := mustCall(t, cs, "indexer_test", args)
		if said := fmt.Sprint(out); strings.Contains(said, key) || !strings.Contains(said, "apikey=REDACTED") || !strings.Contains(said, "http://indexer.example/api") {
			t.Errorf("testing %s = %v, want the address without the key", what, out)
		}
	}
	if msg := mustFail(t, cs, "indexer_edit", map[string]any{"which": "Broken Indexer", "set": map[string]any{"priority": 40}}); strings.Contains(msg, key) || !strings.Contains(msg, "apikey=REDACTED") {
		t.Errorf("the save Sonarr refused = %q, want the address without the key", msg)
	}
	if listed := mustCall(t, cs, "notification_list", nil); strings.Contains(fmt.Sprint(listed), key) || strings.Contains(fmt.Sprint(listed), "Authorization") || !strings.Contains(fmt.Sprint(listed), "https://hooks.example/in?token=REDACTED") {
		t.Errorf("notification_list = %v, want the hook's address without its token and none of its headers", listed)
	}
	added := mustCall(t, cs, "notification_add", map[string]any{"kind": "Webhook", "name": "New Hook", "settings": map[string]any{"url": "https://hooks.example/in", "headers": []any{map[string]any{"key": "Authorization", "value": "Bearer " + key}}}})
	if strings.Contains(fmt.Sprint(added), key) || strings.Contains(fmt.Sprint(added), "Authorization") {
		t.Errorf("notification_add = %v, want none of the headers it was given", added)
	}
	if sent := f.sent(http.MethodPost, "/api/v3/notification"); len(sent) != 1 || !strings.Contains(sent[0], "Bearer "+key) {
		t.Errorf("the hook sent = %v, want its headers saved as given", sent)
	}
	kinds := rowsOf(mustCall(t, cs, "notification_kinds", nil)["kinds"])
	if described := fmt.Sprint(kinds); !strings.Contains(described, "headers") || !strings.Contains(described, "credential:true") {
		t.Errorf("notification_kinds = %v, want the headers listed as a credential to set", kinds)
	}
}

// What Sonarr says went wrong quotes the address it asked, key and all, wherever it says it: a health check's message, a line of its log and the
// exception under it, a line of a log file, why a download is stuck, why a file would not be imported, and an audit's detail of any of them. None
// hands a caller the key; and a link that is there to be followed, a health check's wiki page, keeps the place on the page it points at.
func TestSonarrsOwnWordsCarryNoStoredKey(t *testing.T) {
	t.Parallel()

	const key = "s3cret-key"
	const wiki = "https://wiki.servarr.com/sonarr/system#indexers-are-unavailable-due-to-failures"
	asked := "[GET] at [http://indexer.example/api?t=tvsearch&apikey=" + key + "&limit=100]"

	f := newFakeServer(t)
	firefly := map[string]any{"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874, "path": "/tv/Firefly"}
	library(t, f, firefly)
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.answer(t, "GET /api/v3/health", []map[string]any{{"source": "IndexerStatusCheck", "type": "error", "message": "Indexers unavailable due to failures: Fake. " + asked, "wikiUrl": wiki}})
	f.answer(t, "GET /api/v3/rootfolder", []map[string]any{{"id": 1, "path": "/tv", "accessible": true, "freeSpace": int64(500) << 30}})
	f.answer(t, "GET /api/v3/diskspace", []map[string]any{{"path": "/", "totalSpace": int64(1000) << 30}})
	f.answer(t, "GET /api/v3/log", page(map[string]any{"id": 3, "time": ago(time.Hour), "level": "error", "logger": "Newznab", "message": "An error occurred while processing feed " + asked, "exception": "HttpException: 500 " + asked}))
	f.mux.HandleFunc("GET /api/v3/log/file/sonarr.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1|Error|the feed failed " + asked + "\n"))
	})
	f.answer(t, "GET /api/v3/queue", page(map[string]any{
		"id": 5, "seriesId": 1, "series": firefly, "title": "Firefly.S01E01.1080p-GRP", "status": "warning", "trackedDownloadState": "downloading", "trackedDownloadStatus": "warning",
		"errorMessage": "the download failed " + asked, "statusMessages": []map[string]any{{"title": "Firefly.S01E01", "messages": []string{"nothing came " + asked}}},
	}))
	f.answer(t, "GET /api/v3/manualimport", []any{map[string]any{"id": 1, "path": "/downloads/Firefly.S01E01.mkv", "size": 1, "rejections": []any{map[string]any{"reason": "Unable to look it up " + asked, "type": "permanent"}}}})
	cs := session(t, f, Options{Toolsets: []string{"all"}})

	for tool, args := range map[string]map[string]any{"server_health": nil, "log_list": nil, "logfile_read": nil, "queue_list": nil, "audit_health": nil, "import_scan": {"folder": "/downloads"}} {
		said := fmt.Sprint(mustCall(t, cs, tool, args))
		if strings.Contains(said, key) || !strings.Contains(said, "apikey=REDACTED") || !strings.Contains(said, "http://indexer.example/api") {
			t.Errorf("%s = %v, want the address Sonarr asked without the key", tool, said)
		}
	}

	if said := fmt.Sprint(mustCall(t, cs, "server_health", nil)); !strings.Contains(said, wiki) {
		t.Errorf("server_health = %v, want the wiki page as Sonarr gave it, %s", said, wiki)
	}
}

// What an edit says it changed is a setting before and after, and a setting can be an address with a credential in it: the address Sonarr is reached
// at, written with who signs in. It is said without the credential, whether the change was made, would be made, or did not hold.
func TestWhatAnEditSaysChangedCarriesNoCredential(t *testing.T) {
	t.Parallel()

	const key = "s3cret-key"
	f := newFakeServer(t)
	// the fake answers the section as it was, so what is set does not hold
	f.answer(t, "GET /api/v3/config/host", map[string]any{"id": 1, "port": 8989, "applicationUrl": "", "instanceName": "Sonarr", "authenticationMethod": "none"})
	f.mux.HandleFunc("PUT /api/v3/config/host/1", keeper(t, 1))
	cs := session(t, f, Options{})

	set := map[string]any{"applicationUrl": "https://kt:" + key + "@tv.example/sonarr"}
	would := mustCall(t, cs, "config_edit", map[string]any{"section": "host", "set": set})
	done := mustCall(t, cs, "config_edit", map[string]any{"section": "host", "set": set, "confirm": true})
	for name, said := range map[string][]string{"would_change": strs(would["would_change"]), "changed": strs(done["changed"]), "not_kept": strs(done["not_kept"])} {
		if joined := strings.Join(said, "\n"); strings.Contains(joined, key) || !strings.Contains(joined, "https://REDACTED@tv.example/sonarr") {
			t.Errorf("%s = %q, want the address without who signs in", name, said)
		}
	}
	if sent := f.sent(http.MethodPut, "/api/v3/config/host/1"); len(sent) != 1 || !strings.Contains(sent[0], key) {
		t.Errorf("the section sent = %v, want the address saved as given", sent)
	}
}

// tracer keeps what the Go client says it sent and was sent.
type tracer struct {
	mu    sync.Mutex
	lines []string
}

func (tr *tracer) Debugf(format string, args ...any) { tr.Tracef(format, args...) }

func (tr *tracer) Tracef(format string, args ...any) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.lines = append(tr.lines, fmt.Sprintf(format, args...))
}

func (tr *tracer) said() string {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	return strings.Join(tr.lines, "\n")
}

// With SONARR_LOG=trace the client writes down every request and answer, and
// none of it may carry a credential: not Sonarr's own key in the header, not
// a provider's key, which is sent as the value of a setting named apiKey and
// so sits under "value", not the host's password or the confirmation of it,
// and not a key Sonarr sends back because it does not count it private.
func TestATraceShowsNoCredential(t *testing.T) {
	t.Parallel()

	secrets := map[string]string{
		"Sonarr's own API key":            "s3cret-server-key",
		"an indexer's key, sent":          "s3cret-indexer-key",
		"the host's password":             "s3cret-password-one",
		"the host's password, confirmed":  "s3cret-password-two",
		"an import list's key, sent back": "s3cret-list-key",
	}

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer/schema", []any{newznab()})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.mux.HandleFunc("POST /api/v3/indexer", keeper(t, 9))
	f.mux.HandleFunc("PUT /api/v3/indexer/9", keeper(t, 9))
	f.answer(t, "GET /api/v3/config/host", map[string]any{"id": 1, "port": 8989, "username": "kt", "password": "", "passwordConfirmation": "", "apiKey": secrets["Sonarr's own API key"], "authenticationMethod": "forms"})
	f.mux.HandleFunc("PUT /api/v3/config/host/1", keeper(t, 1))
	f.answer(t, "GET /api/v3/importlist", []any{map[string]any{"id": 3, "name": "Other Sonarr", "implementation": "SonarrImport", "fields": []any{map[string]any{"name": "baseUrl", "value": "http://other.example:8989"}, map[string]any{"name": "apiKey", "value": secrets["an import list's key, sent back"], "privacy": "normal"}}}})

	tr := &tracer{}
	sc, err := sonarr.New(f.srv.URL, secrets["Sonarr's own API key"], client.WithLog(tr))
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(srv, sc, Options{Toolsets: []string{"all"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, srv)

	mustCall(t, cs, "indexer_add", map[string]any{"kind": "newznab", "name": "Usenet One", "settings": map[string]any{"baseUrl": "https://indexer.example", "apiKey": secrets["an indexer's key, sent"]}, "force": true})
	mustCall(t, cs, "config_edit", map[string]any{"section": "host", "confirm": true, "set": map[string]any{"password": secrets["the host's password"], "passwordConfirmation": secrets["the host's password, confirmed"]}})
	mustCall(t, cs, "importlist_list", nil)

	said := tr.said()
	if !strings.Contains(said, "/api/v3/indexer") || !strings.Contains(said, "Usenet One") {
		t.Fatalf("the trace does not hold what was sent, so it proves nothing: %.300s", said)
	}
	for what, secret := range secrets {
		if at := strings.Index(said, secret); at >= 0 {
			t.Errorf("the trace shows %s: ...%s...", what, strings.ReplaceAll(said[max(0, at-90):at], "\n", " ")+"[the secret]")
		}
	}
}

// A setting is a credential by its name where Sonarr does not mark it one.
func TestSecretSettingNames(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"apiKey": true, "passkey": true, "userKey": true, "accessToken": true, "botToken": true, "appSecret": true, "cookie": true,
		"password": true, "passwordConfirmation": true, "sslCertPassword": true, "webHookUrl": true, "statelessUrls": true,
		"url": false, "baseUrl": false, "serverUrl": false, "host": false, "username": false, "tvCategory": false, "sslKeyPath": false, "topics": false,
	} {
		if got := secretSetting(name); got != want {
			t.Errorf("secretSetting(%q) = %v, want %v", name, got, want)
		}
	}
}
