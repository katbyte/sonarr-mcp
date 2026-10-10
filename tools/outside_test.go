package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/outside"
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

// Every piece of text in an answer is cleaned, however deep it sits - in a
// row, a list, a map, behind a pointer or an interface - and the values a
// later call hands back are left exactly as they were.
func TestCleanAnswer(t *testing.T) {
	t.Parallel()

	type row struct {
		Title    string         `json:"title"`
		Path     string         `json:"path"`
		GUID     string         `json:"guid"`
		Messages []string       `json:"messages"`
		Settings map[string]any `json:"settings"`
		Any      any            `json:"any"`
	}
	type page struct {
		Total int `json:"total"`
	}
	type answer struct {
		page
		Series string  `json:"series"`
		Rows   []row   `json:"rows"`
		One    *row    `json:"one"`
		None   *row    `json:"none"`
		Folder string  `json:"folder,omitempty"`
		Score  float64 `json:"score"`
	}
	hidden := "Zzyzx" + zeroWidth + ".Road" + backwards
	out := answer{
		Total: 1, Series: hidden, Folder: hidden, Score: 1.5,
		Rows: []row{{
			Title: hidden, Path: "/tv/" + hidden, GUID: hidden, Messages: []string{hidden, "plain"},
			Settings: map[string]any{"baseUrl": hidden, "path": hidden, "rootFolderPath": hidden, "tags": []any{hidden, 7}, "nested": map[string]any{"name": hidden}},
			Any:      hidden,
		}},
		One: &row{Title: strings.Repeat("a", answerLength+50)},
	}
	cleanAnswer(&out)

	const clean = "Zzyzx.Road"
	got := out.Rows[0]
	if out.Series != clean || got.Title != clean || got.Messages[0] != clean || got.Messages[1] != "plain" || got.Any != clean {
		t.Errorf("the text of the answer = %+v", out)
	}
	tags, isList := got.Settings["tags"].([]any)
	nested, isMap := got.Settings["nested"].(map[string]any)
	if got.Settings["baseUrl"] != clean || !isList || !isMap || len(tags) != 2 || tags[0] != clean || tags[1] != 7 || nested["name"] != clean {
		t.Errorf("the text in a map = %v", got.Settings)
	}
	if out.Folder != hidden || got.Path != "/tv/"+hidden || got.GUID != hidden || got.Settings["path"] != hidden || got.Settings["rootFolderPath"] != hidden {
		t.Errorf("a value handed back was changed: folder %q, path %q, guid %q, a map's path %q and root folder %q", out.Folder, got.Path, got.GUID, got.Settings["path"], got.Settings["rootFolderPath"])
	}
	if n := len([]rune(out.One.Title)); n != answerLength || !strings.HasSuffix(out.One.Title, "…") {
		t.Errorf("a long text is %d characters ending %q, want %d ending in an ellipsis", n, out.One.Title[len(out.One.Title)-6:], answerLength)
	}
	if out.Total != 1 || out.Score != 1.5 || out.None != nil {
		t.Errorf("what is not text was changed: %+v", out)
	}
}

// A refusal's words are cleaned too, and it is still the error it was.
func TestCleanFailure(t *testing.T) {
	t.Parallel()

	plain := errors.New("no series matches")
	if cleanFailure(plain) != plain || cleanFailure(nil) != nil { //nolint:errorlint // the same error, not one like it
		t.Error("an error with nothing to clean is not the error it was")
	}

	sentinel := errors.New("sonarr said: Zzyzx" + backwards + "daoR")
	cleaned := cleanFailure(sentinel)
	if cleaned.Error() != "sonarr said: ZzyzxdaoR" || !errors.Is(cleaned, sentinel) {
		t.Errorf("the cleaned error = %q, wrapping the first %v", cleaned, errors.Is(cleaned, sentinel))
	}
}

// Through the tools as a client calls them: what an indexer, TheTVDB or a
// download client wrote reaches the answer with nothing hidden in it, in the
// structured half and in the text block; a path and a download id come back
// as Sonarr holds them; and a title handed back as it was answered still
// names its series.
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
		"statusMessages": []map[string]any{{"title": "Firefly.S01E01", "messages": []string{"No files" + zeroWidth + " found"}}},
	}))
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
	if msgs := strs(row["messages"]); len(msgs) == 0 || slices.ContainsFunc(msgs, hides) {
		t.Errorf("the download's messages = %q", msgs)
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

	// and so does a refusal quoting what Sonarr said
	f.answerStatus(t, "GET /api/v3/command/9", http.StatusBadRequest, map[string]any{"message": "nothing to see" + backwards + " here"})
	if msg := mustFail(t, cs, "command_list", map[string]any{"command_id": 9}); hides(msg) || !strings.Contains(msg, "nothing to see here") {
		t.Errorf("the refusal = %q", msg)
	}
}

// Every handle is a name an answer really uses: one left behind by a field
// renamed would leave nothing raw and say nothing.
func TestEveryHandleIsAnAnswersField(t *testing.T) {
	t.Parallel()

	listed, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var schemas strings.Builder
	for _, tool := range listed.Tools {
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		schemas.Write(raw)
	}
	// three are never a tool's field: a settings record is answered as a map
	// by Sonarr's own names, so each is held to being a name Sonarr has
	underKeys := map[string][]string{
		"rootFolderPath": propertyNames(reflect.TypeFor[sonarr.ImportListResource]()),
		"remotePath":     propertyNames(reflect.TypeFor[sonarr.RemotePathMappingResource]()),
		"localPath":      propertyNames(reflect.TypeFor[sonarr.RemotePathMappingResource]()),
	}
	for name := range handles {
		if names, underKey := underKeys[name]; underKey {
			if !slices.Contains(names, name) {
				t.Errorf("handles has %q, which is no property of the record it is kept for", name)
			}
			if strings.Contains(schemas.String(), `"`+name+`":{`) {
				t.Errorf("handles has %q as a name only a record's map has, and a tool now answers it as a field", name)
			}

			continue
		}
		if !strings.Contains(schemas.String(), `"`+name+`":{`) {
			t.Errorf("handles has %q, which no tool answers", name)
		}
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

// An address in an answer's text is shown without who it signs in as and
// without the value of any parameter; what is around it, and a place on a
// page, is as it was.
func TestAddressesCarryNoCredential(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]string{ //nolint:gosec // made-up credentials, here to be taken out
		"Unable to connect to indexer. HTTP request failed: [500:InternalServerError] [GET] at [http://indexer.example:8080/api?t=tvsearch&cat=5030,5040&apikey=s3cret-key&limit=100].": "Unable to connect to indexer. HTTP request failed: [500:InternalServerError] [GET] at [http://indexer.example:8080/api?t=REDACTED&cat=REDACTED&apikey=REDACTED&limit=REDACTED].",
		"https://admin:hunter2@indexer.example/api":                                                       "https://REDACTED@indexer.example/api",
		"signed in at https://sonarr.example/callback#access_token=s3cret-key":                            "signed in at https://sonarr.example/callback#REDACTED",
		"see https://wiki.servarr.com/sonarr/system#indexers-are-unavailable-due-to-failures, then retry": "see https://wiki.servarr.com/sonarr/system#indexers-are-unavailable-due-to-failures, then retry",
		"https://hooks.example/in?s3cret-key":                                                             "https://hooks.example/in?REDACTED",
		"https://indexer.example/api?apikey=&t=caps":                                                      "https://indexer.example/api?apikey=&t=REDACTED",
		"/tv/Firefly/Season 01/Firefly - S01E01.mkv":                                                      "/tv/Firefly/Season 01/Firefly - S01E01.mkv",
		"no address here: 12:30":                                                                          "no address here: 12:30",
	} {
		if got := hideAddresses(text); got != want {
			t.Errorf("hideAddresses(%q)\n got %q\nwant %q", text, got, want)
		}
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
	f.answer(t, "GET /api/v3/notification", []any{map[string]any{"id": 2, "name": "Hook", "implementation": "Webhook", "onGrab": true, "fields": []any{map[string]any{"name": "url", "value": "https://hooks.example/in?token=" + key}}}})
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
	if listed := mustCall(t, cs, "notification_list", nil); strings.Contains(fmt.Sprint(listed), key) || !strings.Contains(fmt.Sprint(listed), "https://hooks.example/in?token=REDACTED") {
		t.Errorf("notification_list = %v, want the hook's address without its token", listed)
	}
}
