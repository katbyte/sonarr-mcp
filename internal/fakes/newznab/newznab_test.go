package newznab

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testKey    = "indexer-key"
	testBase   = "http://host.docker.internal:18081"
	tvsearch   = "tvsearch"
	sonarrCats = "5030,5040"
	fireflyID  = 78874

	// the parameters and attributes the tests send and read
	keyAPIKey = "apikey"
	keyCat    = "cat"
	keySeason = "season"
	keyTVDBID = "tvdbid"
	keyLimit  = "limit"

	// the catalogue's releases
	fireflyE07WEB  = "Firefly.S01E07.Jaynestown.1080p.WEB-DL.DDP5.1.H.264-FAKE"
	fireflyE07HDTV = "Firefly.S01E07.Jaynestown.720p.HDTV.x264-FAKE"
	fireflyE08     = "Firefly.S01E08.Out.of.Gas.DVDRip.XviD-FAKE"
	fireflyPack    = "Firefly.S01.2160p.BluRay.x265-FAKE"
	fireflySpecial = "Firefly.S00E01.Serenity.Pilot.1080p.WEB-DL-FAKE"
	chernobylE01   = "Chernobyl.S01E01.1.23.45.1080p.WEB-DL.German.DDP5.1.H.264-FAKE"
	showE01        = "Show.S01E01.720p.HDTV.x264-FAKE"
	fireflyIMDB    = "tt0303461"
)

// The mirrors below read the answers the way Sonarr's Newznab client does:
// the same elements, the same namespace, the same fallbacks. A shape Sonarr
// would not read fails these tests rather than a live run.

// capsDocument mirrors what NewznabCapabilitiesProvider.ParseCapabilities reads.
type capsMirror struct {
	XMLName xml.Name `xml:"caps"`
	Limits  struct {
		Default int `xml:"default,attr"`
		Max     int `xml:"max,attr"`
	} `xml:"limits"`
	Searching struct {
		Search   searchMirror `xml:"search"`
		TVSearch searchMirror `xml:"tv-search"`
	} `xml:"searching"`
	Categories []struct {
		ID      int    `xml:"id,attr"`
		Name    string `xml:"name,attr"`
		Subcats []struct {
			ID   int    `xml:"id,attr"`
			Name string `xml:"name,attr"`
		} `xml:"subcat"`
	} `xml:"categories>category"`
}

type searchMirror struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

// rssMirror mirrors what RssParser and NewznabRssParser read.
type rssMirror struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Response struct {
			Offset int `xml:"offset,attr"`
			Total  int `xml:"total,attr"`
		} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
		Items []itemMirror `xml:"item"`
	} `xml:"channel"`
}

type itemMirror struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	Comments  string `xml:"comments"`
	PubDate   string `xml:"pubDate"`
	Category  string `xml:"category"`
	Enclosure []struct {
		URL    string `xml:"url,attr"`
		Length string `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
}

// attr is TryGetNewznabAttribute: the first attribute of that name.
func (i *itemMirror) attr(name string) string {
	for _, a := range i.Attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value
		}
	}

	return ""
}

// sonarrRelease is the ReleaseInfo Sonarr builds from an item.
type sonarrRelease struct {
	Title, GUID, DownloadURL, InfoURL, CommentURL, ImdbID string
	Size                                                  int64
	PublishDate                                           time.Time
	TvdbID                                                int
	Languages                                             []string
	Scene, Nuked                                          bool
}

func parseItem(t *testing.T, i *itemMirror) sonarrRelease {
	t.Helper()

	r := sonarrRelease{Title: i.Title, GUID: i.GUID, CommentURL: i.Comments, InfoURL: strings.TrimSuffix(i.Comments, "#comments")}
	// UseEnclosureUrl with the usenet mime type preferred
	for _, e := range i.Enclosure {
		if e.Type == "application/x-nzb" {
			r.DownloadURL = e.URL
			if r.Size == 0 {
				r.Size, _ = strconv.ParseInt(e.Length, 10, 64)
			}
		}
	}
	if size, err := strconv.ParseInt(i.attr("size"), 10, 64); err == nil {
		r.Size = size
	}
	date := i.attr("usenetdate")
	if date == "" {
		date = i.PubDate
	}
	var err error
	if r.PublishDate, err = time.Parse(time.RFC1123Z, date); err != nil {
		t.Fatalf("%q: the publish date %q does not parse: %v", i.Title, date, err)
	}
	r.TvdbID, _ = strconv.Atoi(i.attr(keyTVDBID))
	if n, err := strconv.Atoi(i.attr("imdb")); err == nil && n > 0 {
		r.ImdbID = fmt.Sprintf("tt%07d", n)
	}
	for l := range strings.SplitSeq(i.attr("language"), ",") {
		if l = strings.TrimSpace(l); l != "" {
			r.Languages = append(r.Languages, l)
		}
	}
	r.Scene = i.attr("prematch") == "1" || i.attr("haspretime") == "1"
	r.Nuked = i.attr("nuked") == "1"

	return r
}

// errorMirror mirrors NewznabRssParser.CheckError.
type errorMirror struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// isAPIKeyError is how CheckError decides an answer means a bad or missing
// key.
func (e *errorMirror) isAPIKeyError(requestHadKey bool) bool {
	if e.Code >= 100 && e.Code <= 199 {
		return true
	}

	return !requestHadKey && (e.Description == "Missing parameter" || strings.Contains(e.Description, keyAPIKey))
}

// nzbMirror mirrors NzbValidationService: an nzb root with files in its
// namespace, and the segments SABnzbd sizes the job from.
type nzbMirror struct {
	XMLName xml.Name `xml:"http://www.newzbin.com/DTD/2003/nzb nzb"`
	Files   []struct {
		Subject  string `xml:"subject,attr"`
		Segments []struct {
			Bytes int64 `xml:"bytes,attr"`
		} `xml:"http://www.newzbin.com/DTD/2003/nzb segments>segment"`
	} `xml:"http://www.newzbin.com/DTD/2003/nzb file"`
}

// values builds query parameters from name, value pairs, in the order given.
func values(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Add(pairs[i], pairs[i+1])
	}

	return v
}

func newServer(t *testing.T, opts Options) *Server {
	t.Helper()

	if opts.BaseURL == "" && opts.PublicHost == "" {
		opts.BaseURL = testBase
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// get calls the API with the parameters, adding the test key unless one is
// given, and returns the status, content type and body.
func get(t *testing.T, s *Server, path string, params url.Values) (status int, contentType, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.LocalURL()+path+"?"+params.Encode(), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// search runs a tvsearch or search the way Sonarr sends it and returns what
// it parses.
func searchFeed(t *testing.T, s *Server, params url.Values) (rssMirror, []sonarrRelease) {
	t.Helper()

	if params.Get(keyAPIKey) == "" {
		params.Set(keyAPIKey, testKey)
	}
	status, contentType, body := get(t, s, "/api", params)
	if status != http.StatusOK || !strings.Contains(contentType, "xml") {
		t.Fatalf("search %v = %d %s: %s", params, status, contentType, body)
	}
	var feed rssMirror
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		t.Fatalf("the feed does not parse: %v\n%s", err, body)
	}
	releases := make([]sonarrRelease, 0, len(feed.Channel.Items))
	for i := range feed.Channel.Items {
		releases = append(releases, parseItem(t, &feed.Channel.Items[i]))
	}

	return feed, releases
}

func titles(releases []sonarrRelease) []string {
	out := make([]string, 0, len(releases))
	for _, r := range releases {
		out = append(out, r.Title)
	}

	return out
}

// catalogue is a small set of releases across two series, their seasons and
// the categories Sonarr searches.
func catalogue() []Release {
	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	return []Release{
		{Title: fireflyE07WEB, Size: 2_147_483_648, PubDate: day.Add(7 * time.Hour), TVDBID: fireflyID, TvMazeID: 180, IMDBID: fireflyIMDB, Season: 1, Episode: 7, Category: CategoryHD, Grabs: 3, Languages: []string{"English"}, Scene: true},
		{Title: fireflyE07HDTV, Size: 1_073_741_824, PubDate: day.Add(6 * time.Hour), TVDBID: fireflyID, Season: 1, Episode: 7, Category: CategoryHD},
		{Title: fireflyE08, Size: 367_001_600, PubDate: day.Add(5 * time.Hour), TVDBID: fireflyID, Season: 1, Episode: 8, Category: CategorySD, Nuked: true},
		{Title: fireflyPack, Size: 64_424_509_440, PubDate: day.Add(4 * time.Hour), TVDBID: fireflyID, Season: 1, Category: CategoryUHD, Files: 3},
		{Title: fireflySpecial, Size: 3_221_225_472, PubDate: day.Add(3 * time.Hour), TVDBID: fireflyID, Season: 0, Episode: 1, Category: CategoryHD},
		{Title: chernobylE01, Size: 2_684_354_560, PubDate: day.Add(2 * time.Hour), TVDBID: 360893, Season: 1, Episode: 1, Category: CategoryHD, Languages: []string{"German"}},
	}
}

func TestCaps(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey})
	// caps answers without a key, as real indexers do
	status, contentType, body := get(t, s, "/api", values("t", functionCaps))
	if status != http.StatusOK || !strings.Contains(contentType, "xml") {
		t.Fatalf("caps = %d %s: %s", status, contentType, body)
	}
	var caps capsMirror
	if err := xml.Unmarshal([]byte(body), &caps); err != nil {
		t.Fatalf("caps does not parse: %v\n%s", err, body)
	}

	// Newznab.GetProviderPageSize
	if size := min(100, max(caps.Limits.Default, caps.Limits.Max)); size != DefaultPageSize {
		t.Errorf("Sonarr would page by %d", size)
	}
	// Newznab.TestCapabilities: a tv-search that takes an id or q, and
	// season and ep
	if caps.Searching.TVSearch.Available != "yes" || caps.Searching.Search.Available != "yes" {
		t.Errorf("searching = %+v", caps.Searching)
	}
	params := strings.Split(caps.Searching.TVSearch.SupportedParams, ",")
	anyID := slices.ContainsFunc([]string{"q", keyTVDBID, "rid"}, func(p string) bool { return slices.Contains(params, p) })
	if !anyID || !slices.Contains(params, keySeason) || !slices.Contains(params, "ep") {
		t.Errorf("tv-search params %v would fail Sonarr's capability test", params)
	}
	if len(caps.Categories) != 1 || caps.Categories[0].ID != CategoryTV {
		t.Fatalf("categories = %+v", caps.Categories)
	}
	subcats := make([]int, 0, len(caps.Categories[0].Subcats))
	for _, c := range caps.Categories[0].Subcats {
		subcats = append(subcats, c.ID)
	}
	for _, want := range []int{CategorySD, CategoryHD, CategoryUHD, CategoryAnime} {
		if !slices.Contains(subcats, want) {
			t.Errorf("subcategory %d missing from %v", want, subcats)
		}
	}

	// the parameters advertised follow the options
	s = newServer(t, Options{TVSearchParams: []string{"q", keySeason, "ep", keyTVDBID, "tvmazeid"}, PageSize: 50})
	_, _, body = get(t, s, "/api", values("t", functionCaps))
	caps = capsMirror{}
	if err := xml.Unmarshal([]byte(body), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Searching.TVSearch.SupportedParams != "q,season,ep,tvdbid,tvmazeid" || caps.Limits.Max != 50 {
		t.Errorf("caps with options = %+v", caps)
	}
}

// The RSS sync Sonarr runs, and the first request of its indexer test:
// tvsearch in its categories with no query, newest first.
func TestFeed(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	feed, releases := searchFeed(t, s, values("t", tvsearch, keyCat, sonarrCats, "extended", "1", "offset", "0", keyLimit, "100"))

	// the UHD pack is outside 5030,5040
	want := []string{
		fireflyE07WEB,
		fireflyE07HDTV,
		fireflyE08,
		fireflySpecial,
		chernobylE01,
	}
	if got := titles(releases); !slices.Equal(got, want) {
		t.Fatalf("feed = %v\nwant %v", got, want)
	}
	if feed.Channel.Response.Total != len(want) || feed.Channel.Response.Offset != 0 {
		t.Errorf("newznab:response = %+v", feed.Channel.Response)
	}

	first := releases[0]
	stored := s.Releases()[0]
	if first.Size != 2_147_483_648 || !first.PublishDate.Equal(stored.PubDate) || first.TvdbID != fireflyID || first.ImdbID != fireflyIMDB {
		t.Errorf("parsed = %+v", first)
	}
	if !first.Scene || first.Nuked || !slices.Equal(first.Languages, []string{"English"}) {
		t.Errorf("flags and languages = %+v", first)
	}
	if !releases[2].Nuked || releases[4].Languages[0] != "German" {
		t.Errorf("nuked %v, languages %v", releases[2].Nuked, releases[4].Languages)
	}
	// the links are the container's, and the download carries the key
	download, err := url.Parse(first.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.DownloadURL, testBase+"/api?") || download.Query().Get("t") != functionGet ||
		download.Query().Get("id") != stored.GUID || download.Query().Get(keyAPIKey) != testKey {
		t.Errorf("download url = %s", first.DownloadURL)
	}
	if first.InfoURL != testBase+"/details/"+stored.GUID || first.CommentURL != first.InfoURL+"#comments" || first.GUID != first.InfoURL {
		t.Errorf("info %s, comments %s, guid %s", first.InfoURL, first.CommentURL, first.GUID)
	}
	item := feed.Channel.Items[0]
	if item.attr("category") != "5000" || item.Attrs[1].Name != "category" || item.Attrs[1].Value != "5040" || item.Category != "TV > HD" {
		t.Errorf("categories = %s, %+v", item.Category, item.Attrs[:2])
	}
	if item.attr(keySeason) != "1" || item.attr("episode") != "7" || item.attr("tvmazeid") != "180" || item.attr("grabs") != "3" {
		t.Errorf("attrs = %+v", item.Attrs)
	}
	// a pack has a season and no episode; a special is season 0, episode 1
	pack := feed.Channel.Items[3]
	if pack.attr(keySeason) != "0" || pack.attr("episode") != "1" {
		t.Errorf("special attrs = %+v", pack.Attrs)
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	tests := []struct {
		name   string
		params url.Values
		want   []string
	}{
		{
			name:   "an episode by tvdb id",
			params: values("t", tvsearch, keyCat, sonarrCats, keyTVDBID, strconv.Itoa(fireflyID), keySeason, "1", "ep", "7"),
			want:   []string{fireflyE07WEB, fireflyE07HDTV},
		},
		{
			name:   "a season by tvdb id, packs and episodes, in every tv category",
			params: values("t", tvsearch, keyCat, "5000", keyTVDBID, strconv.Itoa(fireflyID), keySeason, "1"),
			want: []string{
				fireflyE07WEB, fireflyE07HDTV,
				fireflyE08, fireflyPack,
			},
		},
		{
			name:   "specials are season 00",
			params: values("t", tvsearch, keyCat, sonarrCats, keyTVDBID, strconv.Itoa(fireflyID), keySeason, "00", "ep", "1"),
			want:   []string{fireflySpecial},
		},
		{
			name:   "season and episode spelled the way some clients do",
			params: values("t", tvsearch, keyTVDBID, strconv.Itoa(fireflyID), keySeason, "S01", "ep", "E08"),
			want:   []string{fireflyE08},
		},
		{
			name:   "by title words",
			params: values("t", tvsearch, keyCat, sonarrCats, "q", "Firefly", keySeason, "1", "ep", "8"),
			want:   []string{fireflyE08},
		},
		{
			name:   "every word of the query",
			params: values("t", tvsearch, "q", "out of gas"),
			want:   []string{fireflyE08},
		},
		{
			name:   "an aggregate id search matches any id given",
			params: values("t", tvsearch, keyTVDBID, "1", "tvmazeid", "180", keySeason, "1", "ep", "7"),
			want:   []string{fireflyE07WEB},
		},
		{
			name:   "by imdb id, with or without tt",
			params: values("t", tvsearch, "imdbid", fireflyIMDB),
			want:   []string{fireflyE07WEB},
		},
		{
			name:   "another series' id",
			params: values("t", tvsearch, keyCat, sonarrCats, keyTVDBID, "360893", keySeason, "1", "ep", "1"),
			want:   []string{chernobylE01},
		},
		{
			name:   "a daily episode number is no episode filter",
			params: values("t", tvsearch, keyTVDBID, "360893", keySeason, "1", "ep", "09/19"),
			want:   []string{chernobylE01},
		},
		{
			name:   "nothing for an id the catalogue lacks",
			params: values("t", tvsearch, keyTVDBID, "1"),
			want:   []string{},
		},
		{
			// the plain search Sonarr uses for anime and specials is text alone
			name:   "search ignores ids, season and episode",
			params: values("t", "search", keyCat, "5040", "q", "Serenity", keyTVDBID, "1", keySeason, "9"),
			want:   []string{fireflySpecial},
		},
		{
			name:   "anime category",
			params: values("t", tvsearch, keyCat, "5070"),
			want:   []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, releases := searchFeed(t, s, tt.params)
			if got := titles(releases); !slices.Equal(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestPaging(t *testing.T) {
	t.Parallel()

	releases := make([]Release, 0, 7)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 7 {
		releases = append(releases, Release{Title: fmt.Sprintf("Show.S01E%02d.720p.HDTV.x264-FAKE", i+1), PubDate: start.Add(time.Duration(i) * time.Hour), Season: 1, Episode: i + 1})
	}
	s := newServer(t, Options{Releases: releases, PageSize: 3})

	var got []string
	for offset := 0; offset < 9; offset += 3 {
		feed, page := searchFeed(t, s, values("t", tvsearch, "offset", strconv.Itoa(offset), keyLimit, "100"))
		// the limit is capped at the page size caps advertised
		if len(page) > 3 || feed.Channel.Response.Total != 7 || feed.Channel.Response.Offset != offset {
			t.Fatalf("page at %d = %v, response %+v", offset, titles(page), feed.Channel.Response)
		}
		got = append(got, titles(page)...)
	}
	if len(got) != 7 || got[0] != "Show.S01E07.720p.HDTV.x264-FAKE" || got[6] != showE01 {
		t.Errorf("pages = %v", got)
	}

	_, page := searchFeed(t, s, values("t", tvsearch, keyLimit, "2"))
	if len(page) != 2 {
		t.Errorf("limit 2 = %d items", len(page))
	}
	// a negative offset is the start
	_, page = searchFeed(t, s, values("t", tvsearch, "offset", "-4"))
	if len(page) != 3 || page[0].Title != "Show.S01E07.720p.HDTV.x264-FAKE" {
		t.Errorf("offset -4 = %v", titles(page))
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	tests := []struct {
		name   string
		params url.Values
		code   int
		hadKey bool
	}{
		{"no key", values("t", tvsearch), ErrMissingParameter, false},
		{"wrong key", values("t", tvsearch, keyAPIKey, "nope"), ErrIncorrectCredentials, true},
		{"wrong key on a download", values("t", functionGet, "id", "x", keyAPIKey, "nope"), ErrIncorrectCredentials, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, _, body := get(t, s, "/api", tt.params)
			var e errorMirror
			if err := xml.Unmarshal([]byte(body), &e); err != nil || status != http.StatusOK {
				t.Fatalf("%d %s: %v", status, body, err)
			}
			if e.Code != tt.code || !e.isAPIKeyError(tt.hadKey) {
				t.Errorf("error %+v is not an API key failure to Sonarr", e)
			}
		})
	}

	// with no key configured, any key, or none, is accepted
	open := newServer(t, Options{Releases: catalogue()})
	_, releases := searchFeed(t, open, values("t", tvsearch, keyAPIKey, "whatever"))
	if len(releases) != len(catalogue()) {
		t.Errorf("an open indexer answered %d releases", len(releases))
	}
	status, _, body := get(t, open, "/api", values("t", tvsearch))
	if status != http.StatusOK || !strings.Contains(body, "<rss") {
		t.Errorf("an open indexer with no key = %d %s", status, body)
	}
}

func TestDownload(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	_, releases := searchFeed(t, s, values("t", tvsearch, keyCat, "5000", "q", "2160p"))
	if len(releases) != 1 {
		t.Fatalf("the UHD pack = %v", titles(releases))
	}
	download, err := url.Parse(releases[0].DownloadURL)
	if err != nil {
		t.Fatal(err)
	}

	status, contentType, body := get(t, s, "/api", download.Query())
	if status != http.StatusOK || contentType != contentTypeNZB {
		t.Fatalf("download = %d %s: %s", status, contentType, body)
	}
	var nzb nzbMirror
	if err := xml.Unmarshal([]byte(body), &nzb); err != nil {
		t.Fatalf("the nzb does not parse: %v\n%s", err, body)
	}
	// three files whose segments add up to the release's size
	var total int64
	for _, f := range nzb.Files {
		for _, seg := range f.Segments {
			total += seg.Bytes
		}
	}
	if len(nzb.Files) != 3 || total != 64_424_509_440 || !strings.Contains(nzb.Files[0].Subject, "part1.rar") {
		t.Errorf("nzb = %d files, %d bytes", len(nzb.Files), total)
	}
	for _, r := range s.Releases() {
		if r.Title == releases[0].Title && r.Grabs != 1 {
			t.Errorf("grabs after one download = %d", r.Grabs)
		}
	}

	// a release that is gone is a 404, which Sonarr reads as unavailable
	status, _, body = get(t, s, "/api", values("t", functionGet, "id", "gone", keyAPIKey, testKey))
	var e errorMirror
	if err := xml.Unmarshal([]byte(body), &e); err != nil || status != http.StatusNotFound || e.Code != ErrNoSuchItem {
		t.Errorf("a missing release = %d %s", status, body)
	}
	status, _, body = get(t, s, "/api", values("t", functionGet, keyAPIKey, testKey))
	if err := xml.Unmarshal([]byte(body), &e); err != nil || status != http.StatusOK || e.Code != ErrMissingParameter {
		t.Errorf("a download with no id = %d %s", status, body)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey})
	tests := []struct {
		name   string
		params url.Values
		code   int
		desc   string
	}{
		{"no function", values(keyAPIKey, testKey), ErrMissingParameter, "Missing parameter (t)"},
		{"unknown function", values("t", "movie", keyAPIKey, testKey), ErrNoSuchFunction, "No such function (movie)"},
		{"bad category", values("t", tvsearch, keyCat, "tv", keyAPIKey, testKey), ErrIncorrectParameter, "Incorrect parameter (cat=tv)"},
		{"bad id", values("t", tvsearch, keyTVDBID, "x", keyAPIKey, testKey), ErrIncorrectParameter, "Incorrect parameter (tvdbid=x)"},
		{"bad season", values("t", tvsearch, keySeason, "one", keyAPIKey, testKey), ErrIncorrectParameter, "Incorrect parameter (season=one)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, _, body := get(t, s, "/api", tt.params)
			var e errorMirror
			if err := xml.Unmarshal([]byte(body), &e); err != nil || status != http.StatusOK {
				t.Fatalf("%d %s: %v", status, body, err)
			}
			if e.Code != tt.code || e.Description != tt.desc {
				t.Errorf("error = %+v, want %d %q", e, tt.code, tt.desc)
			}
		})
	}

	if status, _, _ := get(t, s, "/details/x", nil); status != http.StatusNotFound {
		t.Errorf("a path outside the API = %d", status)
	}
	// an API path under a prefix is still the API
	if status, _, body := get(t, s, "/newznab/api", values("t", functionCaps)); status != http.StatusOK || !strings.Contains(body, "<caps>") {
		t.Errorf("/newznab/api = %d %s", status, body)
	}
	if err := (&parameterError{"x", "y"}).Error(); err != "incorrect parameter x=y" {
		t.Errorf("Error() = %q", err)
	}
}

// A failing indexer is what drives Sonarr's indexer failure health check.
func TestFailure(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	s.SetFailure(Failure{Status: http.StatusServiceUnavailable})
	for _, params := range []url.Values{values("t", functionCaps), values("t", tvsearch, keyAPIKey, testKey)} {
		if status, _, _ := get(t, s, "/api", params); status != http.StatusServiceUnavailable {
			t.Errorf("%v while down = %d", params, status)
		}
	}

	s.SetFailure(Failure{Code: ErrRequestLimitReached, Description: "Request limit reached"})
	status, _, body := get(t, s, "/api", values("t", tvsearch, keyAPIKey, testKey))
	var e errorMirror
	if err := xml.Unmarshal([]byte(body), &e); err != nil || status != http.StatusOK || e.Description != "Request limit reached" {
		t.Errorf("rate limited = %d %s", status, body)
	}

	s.SetFailure(Failure{})
	if _, releases := searchFeed(t, s, values("t", tvsearch)); len(releases) != len(catalogue()) {
		t.Errorf("recovered = %d releases", len(releases))
	}
}

func TestRequests(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{APIKey: testKey, Releases: catalogue()})
	get(t, s, "/api", values("t", functionCaps, keyAPIKey, testKey))
	searchFeed(t, s, values("t", tvsearch, keyTVDBID, strconv.Itoa(fireflyID), keySeason, "1", "ep", "7"))
	searchFeed(t, s, values("t", "search", "q", "Firefly"))

	requests := s.Requests()
	if len(requests) != 3 || requests[0].Function != functionCaps || requests[1].Method != http.MethodGet || requests[1].Path != "/api" {
		t.Fatalf("requests = %+v", requests)
	}
	searches := s.RequestsFor(tvsearch)
	if len(searches) != 1 || searches[0].Query.Get(keyTVDBID) != strconv.Itoa(fireflyID) || searches[0].Query.Get("ep") != "7" || searches[0].Time.IsZero() {
		t.Errorf("tvsearch requests = %+v", searches)
	}
	s.Reset()
	if len(s.Requests()) != 0 {
		t.Error("Reset kept the requests")
	}
}

func TestCatalogue(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	before := time.Now().UTC().Add(-time.Second)
	s.AddRelease(Release{Title: showE01})
	got := s.Releases()
	if len(got) != 1 {
		t.Fatalf("releases = %+v", got)
	}
	r := got[0]
	if len(r.GUID) != 40 || r.Category != CategoryHD || r.PubDate.Before(before) || r.Files != 1 || r.Group == "" || r.Poster == "" {
		t.Errorf("defaults = %+v", r)
	}

	// the same title is the same GUID, so adding it again replaces it
	s.AddRelease(Release{Title: showE01, Size: 5}, Release{GUID: "second", Title: "Show.S01E02.720p.HDTV.x264-FAKE"})
	got = s.Releases()
	if len(got) != 2 || got[0].Size != 5 || got[0].GUID != r.GUID || got[1].GUID != "second" {
		t.Errorf("after AddRelease = %+v", got)
	}

	// what Releases hands back is a copy
	got[0].Languages = append(got[0].Languages, "French")
	if len(s.Releases()[0].Languages) != 0 {
		t.Error("Releases shares its languages with the catalogue")
	}

	s.SetReleases([]Release{{Title: "Other.S01E01.720p.HDTV.x264-FAKE", Category: CategoryTV}})
	if got := s.Releases(); len(got) != 1 || got[0].Title != "Other.S01E01.720p.HDTV.x264-FAKE" {
		t.Errorf("after SetReleases = %+v", got)
	}
	// filed under TV itself, with no subcategory
	feed, _ := searchFeed(t, s, values("t", tvsearch))
	if item := feed.Channel.Items[0]; item.Category != "TV" || len(item.Attrs) == 0 || item.Attrs[1].Name == "category" {
		t.Errorf("a release in 5000 = %s %+v", item.Category, item.Attrs)
	}
}

func TestURLs(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{BaseURL: testBase + "/"})
	if s.URL() != testBase {
		t.Errorf("URL = %q", s.URL())
	}
	if s.Port() == 0 || !strings.HasSuffix(s.Addr(), ":"+strconv.Itoa(s.Port())) || s.LocalURL() != "http://127.0.0.1:"+strconv.Itoa(s.Port()) {
		t.Errorf("Addr %s Port %d LocalURL %s", s.Addr(), s.Port(), s.LocalURL())
	}

	derived := newServer(t, Options{PublicHost: "host.docker.internal"})
	if derived.URL() != "http://host.docker.internal:"+strconv.Itoa(derived.Port()) {
		t.Errorf("derived URL = %q", derived.URL())
	}
	local, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if local.URL() != local.LocalURL() {
		t.Errorf("default URL = %q, want %q", local.URL(), local.LocalURL())
	}
	if err := local.Close(); err != nil {
		t.Error(err)
	}

	if _, err := New(Options{Addr: "256.0.0.1:0"}); err == nil {
		t.Error("New on an address that cannot be listened on succeeded")
	}
}
