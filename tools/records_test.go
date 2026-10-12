package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// newznab is the blank indexer Sonarr answers for the kind, as its schema
// does: everything off, what the kind supports said, its settings unset and
// the API key marked as the credential it is.
func newznab() map[string]any {
	return map[string]any{
		"implementation": "Newznab", "implementationName": "Newznab", "configContract": "NewznabSettings", "protocol": "usenet",
		"supportsRss": true, "supportsSearch": true, "enableRss": false, "enableAutomaticSearch": false, "enableInteractiveSearch": false, "priority": 25, "tags": []int{},
		"fields": []map[string]any{
			{"name": "baseUrl", "label": "URL", "type": "textbox"},
			{"name": "apiKey", "label": "API Key", "type": "textbox", "privacy": "apiKey"},
			{"name": "categories", "label": "Categories", "type": "select", "value": []int{5030, 5040}},
		},
	}
}

// keeper answers a POST or a PUT with what it was sent, an id added, as
// Sonarr answers a record saved: created for the one, accepted for the
// other.
func keeper(t *testing.T, id int) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		body["id"] = id
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(map[string]int{http.MethodPost: http.StatusCreated, http.MethodPut: http.StatusAccepted}[r.Method])
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}
}

// A provider is added from the blank of its kind: its name and properties
// set, its own settings by name, its tags by label and made when new. What
// is answered leaves its credential out, and what the kind does not have is
// refused with what it has.
func TestRecordAdd(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer/schema", []any{newznab(), map[string]any{"implementation": "Torznab", "fields": []any{}}})
	f.answer(t, "GET /api/v3/tag", []any{map[string]any{"id": 3, "label": "anime"}})
	f.mux.HandleFunc("POST /api/v3/indexer", keeper(t, 9))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "indexer_add", map[string]any{
		// a new indexer is used for everything its kind supports unless told
		// otherwise, as Sonarr's own page adds one: the blank has it all off
		"kind": "newznab", "name": "Usenet One", "set": map[string]any{"priority": 10, "enable_rss": false, "tags": []any{"anime"}},
		"settings": map[string]any{"baseUrl": "https://indexer.example", "apikey": "s3cret-key"},
	})
	sent := f.sent(http.MethodPost, "/api/v3/indexer")
	if len(sent) != 1 {
		t.Fatalf("%d indexers were sent, want one", len(sent))
	}
	var body sonarr.IndexerResource
	if err := json.Unmarshal([]byte(sent[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "Usenet One" || body.Implementation != "Newznab" || body.ConfigContract != "NewznabSettings" || body.Priority != 10 || boolv(body.EnableRss) || !boolv(body.EnableAutomaticSearch) || !boolv(body.EnableInteractiveSearch) || !slices.Equal(body.Tags, []sonarr.TagID{3}) {
		t.Errorf("the indexer sent = %+v", body)
	}
	values := map[string]any{}
	for _, field := range body.Fields {
		values[field.Name] = field.Value
	}
	if values["baseUrl"] != "https://indexer.example" || values["apiKey"] != "s3cret-key" {
		t.Errorf("the settings sent = %v", values)
	}
	if q := f.requests("/api/v3/indexer"); !strings.Contains(q[len(q)-1].Query, "forceSave=false") {
		t.Errorf("the indexer was saved with %q, want it tried first", q[len(q)-1].Query)
	}

	record := asObject(out["record"])
	settings := asObject(record["settings"])
	if text(record["name"]) != "Usenet One" || number(record["id"]) != 9 || !slices.Equal(strs(record["tags"]), []string{"anime"}) || settings["baseUrl"] != "https://indexer.example" {
		t.Errorf("the record answered = %v", record)
	}
	if _, shown := settings["apiKey"]; shown || strings.Contains(fmt.Sprint(out), "s3cret-key") {
		t.Errorf("the answer carries the credential: %v", out)
	}
	for _, noise := range []string{"fields", "configContract", "implementationName"} {
		if _, there := record[noise]; there {
			t.Errorf("the record answered carries %s: %v", noise, record)
		}
	}

	for _, c := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"name": "x"}, []string{"which kind of indexer", "Newznab", "Torznab"}},
		{map[string]any{"kind": "Jackett"}, []string{`no kind of indexer "Jackett"`, "Newznab"}},
		{map[string]any{"kind": "Newznab", "settings": map[string]any{"url": "x"}}, []string{`no setting "url"`, "baseUrl", "apiKey", "categories"}},
		{map[string]any{"kind": "Newznab", "set": map[string]any{"rss": true}}, []string{`no property "rss"`, "enableRss", "priority"}},
		{map[string]any{"kind": "Newznab", "set": map[string]any{"priority": "high"}}, []string{"takes a int, not a string"}},
		{map[string]any{"kind": "Newznab", "set": map[string]any{"implementation": "Torznab"}}, []string{`no property "implementation"`}},
	} {
		msg := mustFail(t, cs, "indexer_add", c.args)
		for _, want := range c.want {
			if !strings.Contains(msg, want) {
				t.Errorf("indexer_add %v = %q, want %q in it", c.args, msg, want)
			}
		}
	}
	if again := f.sent(http.MethodPost, "/api/v3/indexer"); len(again) != 1 {
		t.Errorf("%d indexers were sent, though every later call was refused", len(again))
	}
}

// A record is changed by what is given and nothing else: what changed is
// said from and to, a credential only that it was set, and a call that
// changes nothing sends nothing. A delete waits for confirm.
func TestRecordEditAndDelete(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	held := newznab()
	held["id"], held["name"], held["enableRss"] = 9, "Usenet One", true
	f.answer(t, "GET /api/v3/indexer", []any{held})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.mux.HandleFunc("PUT /api/v3/indexer/9", keeper(t, 9))
	f.mux.HandleFunc("DELETE /api/v3/indexer/9", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cs := session(t, f, Options{EnableDelete: true})

	out := mustCall(t, cs, "indexer_edit", map[string]any{"which": "usenet one", "set": map[string]any{"priority": 5}, "settings": map[string]any{"apiKey": "n3w-key", "baseUrl": "https://other.example"}, "force": true})
	changed := strs(out["changed"])
	slices.Sort(changed)
	if !slices.Equal(changed, []string{`apiKey: set`, `baseUrl: nothing -> "https://other.example"`, `priority: 25 -> 5`}) {
		t.Errorf("changed = %q", changed)
	}
	if strings.Contains(fmt.Sprint(out), "n3w-key") {
		t.Errorf("the answer carries the credential: %v", out)
	}
	if q := f.requests("/api/v3/indexer/9"); len(q) != 1 || !strings.Contains(q[0].Query, "forceSave=true") {
		t.Errorf("the indexer was saved with %+v, want it forced", q)
	}

	// by its id, and with nothing that differs: nothing is sent
	same := mustCall(t, cs, "indexer_edit", map[string]any{"which": "9", "set": map[string]any{"priority": 25}})
	if len(strs(same["changed"])) != 0 || len(f.sent(http.MethodPut, "/api/v3/indexer/9")) != 1 {
		t.Errorf("an edit that changes nothing = %v", same)
	}
	if msg := mustFail(t, cs, "indexer_edit", map[string]any{"which": "Jackett", "set": map[string]any{"priority": 1}}); !strings.Contains(msg, "Usenet One (id 9)") {
		t.Errorf("an indexer there is not = %q", msg)
	}
	if msg := mustFail(t, cs, "indexer_edit", map[string]any{"which": "9"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an edit of nothing = %q", msg)
	}

	would := mustCall(t, cs, "indexer_delete", map[string]any{"which": "Usenet One"})
	if text(would["would_delete"]) != "Usenet One" || text(would["deleted"]) != "" || !strings.Contains(text(would["loses"]), "API key") || len(f.sent(http.MethodDelete, "/api/v3/indexer/9")) != 0 {
		t.Errorf("without confirm = %v", would)
	}
	if done := mustCall(t, cs, "indexer_delete", map[string]any{"which": "Usenet One", "confirm": true}); text(done["deleted"]) != "Usenet One" || len(f.sent(http.MethodDelete, "/api/v3/indexer/9")) != 1 {
		t.Errorf("with confirm = %v", done)
	}
}

// A custom format's conditions are built from the blank of each one's kind,
// its settings by name, and answered the same way.
func TestRecordConditions(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/customformat/schema", []any{
		map[string]any{"implementation": "ReleaseTitleSpecification", "implementationName": "Release Title", "negate": false, "required": false, "fields": []map[string]any{{"name": "value", "label": "Regular Expression", "type": "textbox"}}},
		map[string]any{"implementation": "ResolutionSpecification", "fields": []map[string]any{{"name": "value", "type": "select"}}},
	})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.mux.HandleFunc("POST /api/v3/customformat", keeper(t, 4))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "customformat_add", map[string]any{"name": "x265", "set": map[string]any{"specifications": []any{
		map[string]any{"implementation": "releasetitlespecification", "name": "x265", "required": true, "settings": map[string]any{"value": `\bx265\b`}},
		map[string]any{"implementation": "ResolutionSpecification", "settings": map[string]any{"value": 1080}},
	}}})
	var body sonarr.CustomFormatResource
	if sent := f.sent(http.MethodPost, "/api/v3/customformat"); len(sent) != 1 || json.Unmarshal([]byte(sent[0]), &body) != nil {
		t.Fatalf("the format sent = %v", sent)
	}
	if body.Name != "x265" || len(body.Specifications) != 2 || body.Specifications[0].Implementation != "ReleaseTitleSpecification" || body.Specifications[0].Name != "x265" || !boolv(body.Specifications[0].Required) || body.Specifications[0].Fields[0].Value != `\bx265\b` || body.Specifications[1].Name != "ResolutionSpecification" {
		t.Errorf("the format sent = %+v", body)
	}
	record := asObject(out["record"])
	specs := rowsOf(record["specifications"])
	if settings := asObject(specs[0]["settings"]); len(specs) != 2 || settings["value"] != `\bx265\b` {
		t.Errorf("the conditions answered = %v", record["specifications"])
	}

	if msg := mustFail(t, cs, "customformat_add", map[string]any{"name": "y", "set": map[string]any{"specifications": []any{map[string]any{"implementation": "Codec"}}}}); !strings.Contains(msg, `no kind of condition "Codec"`) || !strings.Contains(msg, "ReleaseTitleSpecification") {
		t.Errorf("a kind of condition there is not = %q", msg)
	}
	if msg := mustFail(t, cs, "customformat_add", map[string]any{"set": map[string]any{}}); !strings.Contains(msg, "a name") {
		t.Errorf("a format with no name = %q", msg)
	}
}

// What an indexer, a download client or a notification is saved with is
// under names of Sonarr's choosing, and some of it is a credential: the line
// written for a write names each setting and shows the value of none.
func TestTheWriteLogShowsNoSetting(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer/schema", []any{newznab()})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.mux.HandleFunc("POST /api/v3/indexer", keeper(t, 9))

	var mu sync.Mutex
	var lines []string
	client, err := sonarr.New(f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	client.Client.HTTPClient = f.srv.Client()
	r := &registry{client: client, writeLog: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()

		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	queueTools(r)
	cs := hostRegistry(t, r)

	mustCall(t, cs, "indexer_add", map[string]any{"kind": "Newznab", "name": "Usenet One", "set": map[string]any{"priority": 7}, "settings": map[string]any{"baseUrl": "https://indexer.example/passkey-in-the-path", "apiKey": "s3cret-key"}})

	mu.Lock()
	defer mu.Unlock()

	if len(lines) != 1 || !strings.Contains(lines[0], "indexer_add") || !strings.Contains(lines[0], "answered") || !strings.Contains(lines[0], `"baseUrl":"***"`) || !strings.Contains(lines[0], `"priority":"***"`) || !strings.Contains(lines[0], "Usenet One") {
		t.Fatalf("the line written = %q, want the tool, how it ended and each setting's name", lines)
	}
	for _, secret := range []string{"s3cret-key", "passkey-in-the-path", "indexer.example"} {
		if strings.Contains(lines[0], secret) {
			t.Errorf("the line written carries %q: %s", secret, lines[0])
		}
	}
	if blankFreeForm(`{"settings":{"apiKey":"abc`) != "(arguments too long to show)" {
		t.Error("arguments cut short are shown, and could hold a value unblanked")
	}
}

// A section of the settings is changed by the names it is read under: what
// it does not have is refused with what it has, a value of the wrong kind
// before anything is sent, and what Sonarr took and did not keep is said.
// The host's settings change nothing without confirm.
func TestConfigEdit(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/config/naming", map[string]any{"id": 1, "renameEpisodes": false, "replaceIllegalCharacters": true, "standardEpisodeFormat": "{Series Title} - S{season:00}E{episode:00}", "multiEpisodeStyle": 5})
	f.mux.HandleFunc("PUT /api/v3/config/naming/1", keeper(t, 1))
	// the host keeps its port and drops a username, as Sonarr does while
	// nothing asks anyone to sign in
	f.answer(t, "GET /api/v3/config/host", map[string]any{"id": 1, "port": 8989, "urlBase": "", "username": "", "password": "", "apiKey": "abcdef", "instanceName": "Sonarr", "authenticationMethod": "none"})
	f.mux.HandleFunc("PUT /api/v3/config/host/1", keeper(t, 1))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "config_edit", map[string]any{"section": "naming", "set": map[string]any{"rename_episodes": true, "multiEpisodeStyle": 5}})
	if changed := strs(out["changed"]); !slices.Equal(changed, []string{"renameEpisodes: false -> true"}) {
		t.Errorf("changed = %q", changed)
	}
	var sent sonarr.NamingConfigResource
	if bodies := f.sent(http.MethodPut, "/api/v3/config/naming/1"); len(bodies) != 1 || json.Unmarshal([]byte(bodies[0]), &sent) != nil || !sent.RenameEpisodes || sent.Id != 1 || sent.StandardEpisodeFormat == "" || !sent.ReplaceIllegalCharacters {
		t.Errorf("the section sent = %v", bodies)
	}
	// the fake answers the section as it was, so what was set did not hold
	if kept := strs(out["not_kept"]); len(kept) != 1 || !strings.Contains(kept[0], "renameEpisodes") {
		t.Errorf("not_kept = %q, want the setting Sonarr does not hold", kept)
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"section": "naming", "set": map[string]any{"rename": true}}, `no setting "rename" (it has: `},
		{map[string]any{"section": "naming", "set": map[string]any{"renameEpisodes": "yes"}}, "takes a bool, not a string"},
		{map[string]any{"section": "naming", "set": map[string]any{}}, "nothing to change"},
		{map[string]any{"section": "quality", "set": map[string]any{"a": 1}}, "section must be naming"},
	} {
		if msg := mustFail(t, cs, "config_edit", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("config_edit %v = %q, want %q in it", c.args, msg, c.want)
		}
	}
	if same := mustCall(t, cs, "config_edit", map[string]any{"section": "naming", "set": map[string]any{"renameEpisodes": false}}); len(strs(same["changed"])) != 0 || len(f.sent(http.MethodPut, "/api/v3/config/naming/1")) != 1 {
		t.Errorf("an edit that changes nothing = %v", same)
	}

	would := mustCall(t, cs, "config_edit", map[string]any{"section": "host", "set": map[string]any{"port": 9000, "instanceName": "TV", "password": "hunter2"}})
	if w := strs(would["would_change"]); !slices.Contains(w, "port: 8989 -> 9000") || !slices.Contains(w, "password: set") || len(strs(would["changed"])) != 0 || !strings.Contains(text(would["warning"]), "port") || len(f.sent(http.MethodPut, "/api/v3/config/host/1")) != 0 {
		t.Errorf("the host without confirm = %v", would)
	}
	done := mustCall(t, cs, "config_edit", map[string]any{"section": "host", "set": map[string]any{"instanceName": "TV"}, "confirm": true})
	settings := asObject(done["settings"])
	if !slices.Equal(strs(done["changed"]), []string{`instanceName: "Sonarr" -> "TV"`}) || text(done["warning"]) != "" || len(f.sent(http.MethodPut, "/api/v3/config/host/1")) != 1 {
		t.Errorf("the host with confirm = %v", done)
	}
	if _, shown := settings["apiKey"]; shown || strings.Contains(fmt.Sprint(would, done), "hunter2") || strings.Contains(fmt.Sprint(done), "abcdef") {
		t.Errorf("the host's answer carries a credential: %v %v", would, done)
	}
}

// A quality profile is changed by the names its settings are answered
// under: the qualities it allows, where it stops upgrading, and what it
// scores each custom format, with a name it does not have refused.
func TestProfileChange(t *testing.T) {
	t.Parallel()

	quality := func(id int, name string, allowed bool) sonarr.QualityProfileQualityItemResource {
		return sonarr.QualityProfileQualityItemResource{Quality: &sonarr.Quality{Id: id, Name: name}, Allowed: new(allowed)}
	}
	profile := func() sonarr.QualityProfileResource {
		return sonarr.QualityProfileResource{
			Id: 4, Name: "HD", Cutoff: 4, UpgradeAllowed: new(false),
			Items: []sonarr.QualityProfileQualityItemResource{
				quality(1, "SDTV", false), quality(4, "HDTV-720p", true),
				{Id: 1001, Name: "WEB 1080p", Allowed: new(false), Items: []sonarr.QualityProfileQualityItemResource{quality(3, "WEBDL-1080p", false), quality(15, "WEBRip-1080p", false)}},
			},
			FormatItems: []sonarr.ProfileFormatItemResource{{Format: 7, Name: "x265"}},
		}
	}

	p := profile()
	change := profileChange{Name: "HD and up", UpgradeAllowed: new(true), Allow: []string{"webdl-1080p"}, Cutoff: "WEB 1080p", CutoffFormatScore: new(100), FormatScores: map[string]int{"X265": 50}}
	if err := change.apply(&p); err != nil {
		t.Fatal(err)
	}
	row := projectProfile(&p, 0)
	if row.Name != "HD and up" || !row.UpgradeAllowed || row.Cutoff != "WEB 1080p" || p.Cutoff != 1001 || !slices.Equal(row.Allowed, []string{"WEBRip-1080p", "WEBDL-1080p", "HDTV-720p"}) || row.CutoffFormatScore != 100 || row.FormatScores["x265"] != 50 {
		t.Errorf("the profile changed = %+v (cutoff %d)", row, p.Cutoff)
	}
	if changed := profileChanges(projectProfile(new(profile()), 0), row); len(changed) != 6 {
		t.Errorf("what changed = %q, want the six settings given", changed)
	}

	p = profile()
	replace := profileChange{Allowed: []string{"SDTV"}, Cutoff: "SDTV"}
	if err := replace.apply(&p); err != nil || !slices.Equal(allowedQualities(p.Items), []string{"SDTV"}) || p.Cutoff != 1 {
		t.Errorf("the allowed qualities replaced = %v, cutoff %d, %v", allowedQualities(p.Items), p.Cutoff, err)
	}
	p = profile()
	drop := profileChange{Disallow: []string{"HDTV-720p"}}
	if err := drop.apply(&p); err != nil || len(allowedQualities(p.Items)) != 0 {
		t.Errorf("a quality disallowed = %v, %v", allowedQualities(p.Items), err)
	}

	for _, c := range []struct {
		change profileChange
		want   string
	}{
		{profileChange{Allow: []string{"Bluray-4320p"}}, `no quality "Bluray-4320p" (have: WEB 1080p, WEBRip-1080p, WEBDL-1080p, HDTV-720p, SDTV)`},
		{profileChange{Cutoff: "Remux"}, `no quality "Remux" to stop upgrading at`},
		{profileChange{FormatScores: map[string]int{"HDR": 10}}, `no custom format "HDR" (have: x265)`},
	} {
		p = profile()
		if err := c.change.apply(&p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v = %v, want %q", c.change, err, c.want)
		}
	}
}

// Sonarr tries a provider that is switched on as it is added whatever force
// is, and never one that is switched off, so a forced add goes in switched
// off and is switched on by an edit, which force does carry.
func TestRecordAddForced(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer/schema", []any{newznab()})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.mux.HandleFunc("POST /api/v3/indexer", keeper(t, 9))
	f.mux.HandleFunc("PUT /api/v3/indexer/9", keeper(t, 9))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "indexer_add", map[string]any{"kind": "newznab", "name": "Not There Yet", "set": map[string]any{"enableRss": false}, "settings": map[string]any{"baseUrl": "https://indexer.example"}, "force": true})

	var added, switched sonarr.IndexerResource
	posts, puts := f.sent(http.MethodPost, "/api/v3/indexer"), f.sent(http.MethodPut, "/api/v3/indexer/9")
	if len(posts) != 1 || len(puts) != 1 {
		t.Fatalf("a forced add sent %d adds and %d edits, want one of each", len(posts), len(puts))
	}
	if err := json.Unmarshal([]byte(posts[0]), &added); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(puts[0]), &switched); err != nil {
		t.Fatal(err)
	}
	if boolv(added.EnableRss) || boolv(added.EnableAutomaticSearch) || boolv(added.EnableInteractiveSearch) {
		t.Errorf("the indexer went in switched on, which Sonarr tries: %+v", added)
	}
	if boolv(switched.EnableRss) || !boolv(switched.EnableAutomaticSearch) || !boolv(switched.EnableInteractiveSearch) || switched.Name != "Not There Yet" {
		t.Errorf("the edit switched on %+v, want the two searches and not the RSS set false", switched)
	}
	for _, q := range append(f.requests("/api/v3/indexer"), f.requests("/api/v3/indexer/9")...) {
		if !strings.Contains(q.Query, "forceSave=true") {
			t.Errorf("%s %s was sent with %q, want it forced", q.Method, q.Path, q.Query)
		}
	}
	if record := asObject(out["record"]); !flag(record["enableAutomaticSearch"]) || flag(record["enableRss"]) {
		t.Errorf("the record answered = %v", record)
	}

	// one that is to be switched off anyway needs no edit after it
	mustCall(t, cs, "indexer_add", map[string]any{"kind": "newznab", "name": "Kept Off", "set": map[string]any{"enableRss": false, "enableAutomaticSearch": false, "enableInteractiveSearch": false}, "force": true})
	if puts := f.sent(http.MethodPut, "/api/v3/indexer/9"); len(puts) != 1 {
		t.Errorf("an indexer added switched off was edited after: %d edits in all", len(puts))
	}
}

// Sonarr's test of every notification answers for none of them when one
// throws rather than fails, so each that is sent for something is tried on
// its own, and the one that threw is a row that failed.
func TestRecordTestAllWhenOneThrows(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/notification", []any{
		map[string]any{"id": 1, "name": "Dead Hook", "implementation": "Webhook", "onGrab": true, "fields": []any{}},
		map[string]any{"id": 2, "name": "Live Hook", "implementation": "Webhook", "onDownload": true, "fields": []any{}},
		map[string]any{"id": 3, "name": "Sent For Nothing", "implementation": "Webhook", "onGrab": false, "fields": []any{}},
	})
	f.answerStatus(t, "POST /api/v3/notification/testall", http.StatusInternalServerError, map[string]any{"message": "Connection refused (127.0.0.1:9)"})
	f.mux.HandleFunc("POST /api/v3/notification/test", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["name"] == "Dead Hook" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message": "Connection refused (127.0.0.1:9)"}`))

			return
		}
		writeJSON(t, w, map[string]any{})
	})
	cs := session(t, f, Options{})

	rows := rowsOf(mustCall(t, cs, "notification_test", nil)["results"])
	if len(rows) != 2 || text(rows[0]["name"]) != "Dead Hook" || flag(rows[0]["ok"]) || !strings.Contains(fmt.Sprint(rows[0]["problems"]), "Connection refused") || text(rows[1]["name"]) != "Live Hook" || !flag(rows[1]["ok"]) {
		t.Errorf("the results = %v, want the dead one failed, the live one passed and the one sent for nothing left out", rows)
	}
	if tried := len(f.sent(http.MethodPost, "/api/v3/notification/test")); tried != 2 {
		t.Errorf("%d notifications were tried on their own, want the two that are sent for something", tried)
	}
}
