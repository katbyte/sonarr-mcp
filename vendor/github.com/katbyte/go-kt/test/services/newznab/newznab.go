// Package newznab is a Newznab and Torznab indexer for a live suite to run
// beside the server under test: one listener serving any number of indexers,
// each at /<name>/api, each usenet or torrent, and each healthy or broken in
// its own way. A test decides which releases exist, sees every request made,
// and can make an indexer fail, fail now and then, answer slowly or refuse
// its key.
//
// The test's data is the test's to decide. A release says its title and its
// category, and one that does not is refused. What a release need not say
// but a feed has to carry - when it was posted, and how big a torrent is -
// is not one fixed value a test could come to lean on without knowing it:
// it is picked from a spread by a seed (Options.Seed), differently each run
// unless the seed is given, and Indexer.Releases says what was picked.
//
// It is the indexers sonarr-mcp, radarr-mcp and prowlarr-mcp each wrote for
// their suites, made one. Every answer is in the shape the *arr parsers read
// (NewznabCapabilitiesProvider, NewznabRssParser, TorznabRssParser and
// NzbValidationService in their source), and where a comment says what one
// of them does with an answer, that is why the answer is as it is.
//
// It runs on the host, and the container reaches the host by another name
// than the test does, so the links in its feeds are built from the address
// the container uses (Options.PublicHost), not the one the test dialled.
package newznab

import (
	"context"
	"crypto/sha1" //nolint:gosec // a stable id for a release title and a torrent's info hash, neither a security boundary
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The protocols an indexer speaks: Newznab for usenet, Torznab for torrents.
const (
	Usenet  = "usenet"
	Torrent = "torrent"
)

// The kinds of search an indexer's capabilities list, for Site.Searches.
const (
	SearchText  = "search"
	SearchTV    = "tv-search"
	SearchMovie = "movie-search"
	SearchMusic = "music-search"
	SearchBook  = "book-search"
)

// The standard categories. A release is filed under one subcategory, and a
// search for its parent (2000, 5000) finds it too.
const (
	CategoryMovies        = 2000
	CategoryMoviesSD      = 2030
	CategoryMoviesHD      = 2040
	CategoryMoviesUHD     = 2045
	CategoryAudio         = 3000
	CategoryTV            = 5000
	CategoryTVForeign     = 5020
	CategoryTVSD          = 5030
	CategoryTVHD          = 5040
	CategoryTVUHD         = 5045
	CategoryTVOther       = 5050
	CategoryTVSport       = 5060
	CategoryTVAnime       = 5070
	CategoryTVDocumentary = 5080
	CategoryBooks         = 7000
)

// The Newznab error codes an indexer answers with. The *arrs read 100-199 as
// a key that was refused, and 500 as a request limit to back off from.
const (
	ErrIncorrectCredentials = 100
	ErrMissingParameter     = 200
	ErrIncorrectParameter   = 201
	ErrNoSuchFunction       = 202
	ErrNoSuchItem           = 300
	ErrRequestLimitReached  = 500
	ErrUnknown              = 900
)

// DefaultPageSize is the page size an indexer advertises and serves unless
// its Site says otherwise.
const DefaultPageSize = 100

// Category is a category an indexer's capabilities list, with its
// subcategories.
type Category struct {
	ID   int
	Name string
	Subs []Category
}

// DefaultCategories are movies and TV with their subcategories, which is what
// Sonarr's and Radarr's own default categories are found in.
var DefaultCategories = []Category{
	{ID: CategoryMovies, Name: "Movies", Subs: []Category{{ID: CategoryMoviesSD, Name: "SD"}, {ID: CategoryMoviesHD, Name: "HD"}, {ID: CategoryMoviesUHD, Name: "UHD"}}},
	{ID: CategoryTV, Name: "TV", Subs: []Category{{ID: CategoryTVForeign, Name: "Foreign"}, {ID: CategoryTVSD, Name: "SD"}, {ID: CategoryTVHD, Name: "HD"}, {ID: CategoryTVUHD, Name: "UHD"}, {ID: CategoryTVOther, Name: "Other"}, {ID: CategoryTVSport, Name: "Sport"}, {ID: CategoryTVAnime, Name: "Anime"}, {ID: CategoryTVDocumentary, Name: "Documentary"}}},
}

// Release is one entry in an indexer's catalogue.
type Release struct {
	// GUID identifies the release in its links; empty derives a stable one
	// from the title.
	GUID string
	// Title is the scene name an *arr parses the series or film, the
	// quality and the group from.
	Title string
	// Size is the release's size in bytes, which an *arr checks against the
	// quality's size limits; 0 is unknown, and not checked. A torrent has
	// to hold something - Prowlarr refuses one of nothing and counts it
	// against the indexer - so a torrent that does not say its size is
	// given one (Options.Seed).
	Size int64
	// PubDate is when it was posted. There is no leaving it unknown, as a
	// size can be: Sonarr refuses a whole feed for one item with no date
	// (RssParser.GetPublishDate). So a release that does not say is given
	// one (Options.Seed), which it keeps however often it is offered: an
	// *arr knows a blocklisted usenet release by its date.
	PubDate time.Time
	// Category is the subcategory it is filed under, which decides the
	// searches that find it. It is the test's to say: a release with none
	// is refused.
	Category int
	// IMDBID, TMDBID, TVDBID and TvMazeID are what a search by id matches.
	// IMDBID is written with or without its tt.
	IMDBID   string
	TMDBID   int
	TVDBID   int
	TvMazeID int
	// Season and Episode are what a TV search by season and episode
	// matches. Episode 0 is a season pack.
	Season  int
	Episode int
	// Seeders and Peers are what a torrent indexer reports, and Freeleech a
	// torrent that costs no download ratio.
	Seeders   int
	Peers     int
	Freeleech bool
	// Grabs counts the times it was fetched.
	Grabs int
	// Files is the number of files its NZB lists; 0 is one.
	Files int
	// Group and Poster are the usenet group and poster the feed and the NZB
	// name; empty is a made-up one of each.
	Group  string
	Poster string
	// Languages are language names (English, German) an *arr reads from the
	// feed rather than the title.
	Languages []string
	// Scene and Nuked set the prematch and nuked attributes an *arr turns
	// into indexer flags.
	Scene bool
	Nuked bool
}

// Failure is how an indexer misbehaves; the zero value is healthy.
type Failure struct {
	// Status, when set, is the HTTP status every call answers, the way an
	// indexer that is down answers a 503.
	Status int
	// Code, when set and Status is not, answers every call with a Newznab
	// error document under HTTP 200: a key the indexer has revoked
	// (ErrIncorrectCredentials) or a request limit (ErrRequestLimitReached).
	Code        int
	Description string
	// Every, when above 1, fails only every Every-th search, the way a flaky
	// indexer does, and answers the rest; capabilities and downloads are
	// always answered, so the indexer can still be added and tested. A
	// failure lasts failureSpell, long enough to outlast the retries an
	// *arr makes of a server error, which would otherwise hide it.
	Every int
}

// failureSpell is how long a flaky indexer's failure lasts once it starts.
const failureSpell = 12 * time.Second

// Site describes one indexer.
type Site struct {
	// Name is its path: its API is at /<Name>/api.
	Name string
	// Protocol is Usenet or Torrent; empty is Usenet.
	Protocol string
	// APIKey is the key every call but capabilities must carry as apikey=;
	// empty accepts any.
	APIKey string
	// Categories are what its capabilities list; nil is DefaultCategories.
	Categories []Category
	// Searches are the kinds of search it offers; nil is SearchText,
	// SearchTV and SearchMovie.
	Searches []string
	// TVSearchParams are the tv-search parameters its capabilities list;
	// nil is q, season, ep and tvdbid. An *arr sends only the ids listed.
	TVSearchParams []string
	// PageSize is the default and largest page it serves; 0 is
	// DefaultPageSize.
	PageSize int
	// Releases is its catalogue to start with.
	Releases []Release
	// Delay holds every answer back this long, the way a slow indexer does.
	Delay time.Duration
	// Failure is how it misbehaves.
	Failure Failure
}

// Request is one call an indexer received.
type Request struct {
	// Site is the name of the indexer asked.
	Site   string
	Method string
	Path   string
	// Function is the t= parameter (caps, search, tvsearch, movie), or
	// "download" for a fetch of a release.
	Function string
	// Query is every parameter as sent, the API key included.
	Query url.Values
	Time  time.Time
}

// Options configure a Server.
type Options struct {
	// Addr is where to listen, e.g. ":18081"; empty picks a free port on
	// 127.0.0.1. A container reaches the host from outside it, so a live
	// run listens on every interface.
	Addr string
	// PublicHost is the name the container reaches this machine by, which
	// the feeds' links are built from; empty is 127.0.0.1.
	PublicHost string
	// Sites are the indexers to serve from the start.
	Sites []Site
	// Seed decides what a release is given for what it does not say and
	// has to have: when it was posted, and for a torrent how big it is.
	// Each is picked from a spread, so that no test comes to lean on one
	// made-up value without knowing it, and Releases says what was picked.
	// The same seed gives the same release the same again; 0 picks a seed
	// and says which on stderr, where a failing run's output has it, and
	// Server.Seed reports it.
	Seed uint64
}

// Server is the listener the indexers are served from.
type Server struct {
	publicHost string
	listener   net.Listener
	srv        *http.Server
	started    time.Time
	seed       uint64

	mu       sync.Mutex
	indexers map[string]*Indexer
	requests []Request
}

// Indexer is one indexer a Server serves.
type Indexer struct {
	server *Server

	// what follows is the server's to guard
	site      Site
	searches  int
	failUntil time.Time
	grabbed   []string
}

// New starts a server listening on opts.Addr.
func New(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.PublicHost == "" {
		opts.PublicHost = "127.0.0.1"
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("newznab: listening on %s: %w", opts.Addr, err)
	}

	s := &Server{publicHost: opts.PublicHost, listener: ln, started: time.Now().UTC().Truncate(time.Second), seed: opts.Seed, indexers: map[string]*Indexer{}}
	if s.seed == 0 {
		s.seed = rand.Uint64() | 1 //nolint:gosec // test data to vary from run to run, not a secret
		// said where a failing run's output has it, so a suite that printed
		// nothing can still be run again on the same data
		_, _ = fmt.Fprintf(os.Stderr, "newznab: seed %d (give it as Options.Seed to run on the same data again)\n", s.seed)
	}
	for _, site := range opts.Sites {
		if _, err := s.Add(site); err != nil {
			_ = ln.Close()

			return nil, err
		}
	}

	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()

	return s, nil
}

// Close stops the server at once. There is nothing to drain, and a graceful
// shutdown waits on any connection a client opened and never used.
func (s *Server) Close() error { return s.srv.Close() }

// Addr is the address listened on.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Seed is the seed this run's releases were given what they did not say from
// (Options.Seed).
func (s *Server) Seed() uint64 { return s.seed }

// Port is the port listened on.
func (s *Server) Port() int {
	if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}

	return 0
}

// Add starts serving an indexer, in place of any of the same name.
func (s *Server) Add(site Site) (*Indexer, error) {
	if site.Protocol == "" {
		site.Protocol = Usenet
	}
	switch {
	case site.Name == "" || strings.ContainsAny(site.Name, "/?#"):
		return nil, fmt.Errorf("newznab: %q is not a name an indexer can be served under", site.Name)
	case site.Protocol != Usenet && site.Protocol != Torrent:
		return nil, fmt.Errorf("newznab: indexer %s: the protocol is usenet or torrent, not %q", site.Name, site.Protocol)
	}

	if site.Categories == nil {
		site.Categories = DefaultCategories
	}
	if site.Searches == nil {
		site.Searches = []string{SearchText, SearchTV, SearchMovie}
	}
	if site.TVSearchParams == nil {
		site.TVSearchParams = []string{"q", "season", "ep", "tvdbid"}
	}
	if site.PageSize <= 0 {
		site.PageSize = DefaultPageSize
	}

	releases, err := s.filled(site.Releases, site.Protocol)
	if err != nil {
		return nil, fmt.Errorf("newznab: indexer %s: %w", site.Name, err)
	}
	ix := &Indexer{server: s, site: site}
	ix.site.Releases = releases

	s.mu.Lock()
	defer s.mu.Unlock()

	s.indexers[site.Name] = ix

	return ix, nil
}

// Indexer is the indexer served under a name, nil when there is none.
func (s *Server) Indexer(name string) *Indexer {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.indexers[name]
}

// Requests are the calls every indexer received, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// The spreads a release is given what it did not say from. A date is so long
// before the server started, in seconds: about an hour, some hours, about a
// day, or about a week. A torrent's size is an episode's, an episode's or a
// small film's in HD, a film's, or a film's as it came off the disc.
var (
	postedAges   = [][2]int64{{30 * 60, 90 * 60}, {2 * 3600, 20 * 3600}, {22 * 3600, 26 * 3600}, {6 * 86400, 8 * 86400}}
	torrentSizes = [][2]int64{{200 << 20, 1 << 30}, {1 << 30, 4 << 30}, {4 << 30, 15 << 30}, {20 << 30, 60 << 30}}
)

// pick is a number from one of the spreads, each as likely as the next,
// decided by the seed, the release and what is being picked: a release is
// given the same every time it is offered, and a run with the same seed gives
// every release the same again.
func (s *Server) pick(guid, what string, spreads [][2]int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(guid + "\x00" + what))
	dice := rand.New(rand.NewPCG(s.seed, h.Sum64())) //nolint:gosec // test data that comes out the same for the same seed, not a secret
	spread := spreads[dice.IntN(len(spreads))]

	return spread[0] + dice.Int64N(spread[1]-spread[0]+1)
}

// filled is releases with what each left unsaid decided, for an indexer of a
// protocol. A title and a category are the test's to say, and a release
// without one is refused.
func (s *Server) filled(releases []Release, protocol string) ([]Release, error) {
	out := make([]Release, 0, len(releases))
	for _, r := range releases {
		switch {
		case r.Title == "":
			return nil, errors.New("a release has no title")
		case r.Category == 0:
			return nil, fmt.Errorf("release %s does not say its category, which decides the searches that find it", r.Title)
		}

		if r.GUID == "" {
			sum := sha1.Sum([]byte(r.Title)) //nolint:gosec // an id, not a secret
			r.GUID = hex.EncodeToString(sum[:])
		}
		if r.PubDate.IsZero() {
			r.PubDate = s.started.Add(-time.Duration(s.pick(r.GUID, "posted", postedAges)) * time.Second)
		}
		if protocol == Torrent && r.Size <= 0 {
			r.Size = s.pick(r.GUID, "size", torrentSizes)
		}
		if r.Files <= 0 {
			r.Files = 1
		}
		if r.Group == "" {
			r.Group = "alt.binaries.test"
		}
		if r.Poster == "" {
			r.Poster = "poster@newznab.invalid (test)"
		}
		r.Languages = slices.Clone(r.Languages)
		out = append(out, r)
	}

	return out, nil
}

// Name is the name the indexer is served under.
func (ix *Indexer) Name() string { return ix.site.Name }

// URL is the indexer's address as the container reaches it: the base URL an
// *arr is given for it, with /api as its API path.
func (ix *Indexer) URL() string {
	return "http://" + net.JoinHostPort(ix.server.publicHost, strconv.Itoa(ix.server.Port())) + "/" + ix.site.Name
}

// LocalURL is the indexer's address from this process.
func (ix *Indexer) LocalURL() string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(ix.server.Port())) + "/" + ix.site.Name
}

// SetReleases replaces the catalogue, or leaves it as it was when a release
// is refused (Release).
func (ix *Indexer) SetReleases(releases ...Release) error {
	filled, err := ix.server.filled(releases, ix.site.Protocol)
	if err != nil {
		return fmt.Errorf("newznab: indexer %s: %w", ix.site.Name, err)
	}

	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	ix.site.Releases = filled

	return nil
}

// Offer adds releases to the catalogue, each in place of any with its GUID,
// or adds none when one is refused (Release).
func (ix *Indexer) Offer(releases ...Release) error {
	filled, err := ix.server.filled(releases, ix.site.Protocol)
	if err != nil {
		return fmt.Errorf("newznab: indexer %s: %w", ix.site.Name, err)
	}

	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	for _, r := range filled {
		if i := slices.IndexFunc(ix.site.Releases, func(have Release) bool { return have.GUID == r.GUID }); i >= 0 {
			ix.site.Releases[i] = r

			continue
		}
		ix.site.Releases = append(ix.site.Releases, r)
	}

	return nil
}

// Withdraw takes releases out of the catalogue by title.
func (ix *Indexer) Withdraw(titles ...string) {
	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	ix.site.Releases = slices.DeleteFunc(ix.site.Releases, func(r Release) bool { return slices.Contains(titles, r.Title) })
}

// Releases is the catalogue, with what each release left unsaid decided and
// its grabs counted.
func (ix *Indexer) Releases() []Release {
	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	out := make([]Release, len(ix.site.Releases))
	for i, r := range ix.site.Releases {
		r.Languages = slices.Clone(r.Languages)
		out[i] = r
	}

	return out
}

// Requests are the calls the indexer received, oldest first.
func (ix *Indexer) Requests() []Request {
	var out []Request
	for _, r := range ix.server.Requests() {
		if r.Site == ix.site.Name {
			out = append(out, r)
		}
	}

	return out
}

// Grabbed is the titles of the releases fetched from the indexer, in the
// order they were: what the server under test grabbed.
func (ix *Indexer) Grabbed() []string {
	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	return slices.Clone(ix.grabbed)
}

// SetFailure changes how the indexer misbehaves; the zero Failure mends it.
func (ix *Indexer) SetFailure(f Failure) {
	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	ix.site.Failure = f
	ix.searches = 0
	ix.failUntil = time.Time{}
}

// SetDelay changes how long the indexer holds its answers back.
func (ix *Indexer) SetDelay(d time.Duration) {
	ix.server.mu.Lock()
	defer ix.server.mu.Unlock()

	ix.site.Delay = d
}
