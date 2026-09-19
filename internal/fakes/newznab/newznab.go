// Package newznab is a fake Newznab indexer for the live suites.
//
// Sonarr adds it the way it adds any usenet indexer, then asks it for its
// capabilities, RSS-syncs it, searches it by series id, season and episode,
// and fetches the NZB of whatever it decides to grab. Every answer is served
// in the shape Sonarr's own Newznab client reads (NewznabCapabilitiesProvider,
// NewznabRssParser, NzbValidationService in Sonarr's source), so a test
// controls exactly which releases exist, sees every request Sonarr made, and
// can make the indexer fail on demand to drive Sonarr's indexer health checks.
//
// It runs on the host and the container reaches it through
// host.docker.internal, so the links in its feed are built from the address
// the container uses (Options.BaseURL or Options.PublicHost), not the one the
// test process dialled.
package newznab

import (
	"context"
	"crypto/sha1" //nolint:gosec // a stable GUID for a release title, not a security boundary
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The categories Sonarr searches by default (5030, 5040) and the others a TV
// indexer carries. A release is filed under one subcategory; a search for
// the parent, 5000, finds every one of them.
const (
	CategoryTV          = 5000
	CategoryForeign     = 5020
	CategorySD          = 5030
	CategoryHD          = 5040
	CategoryUHD         = 5045
	CategoryOther       = 5050
	CategorySport       = 5060
	CategoryAnime       = 5070
	CategoryDocumentary = 5080
)

// The Newznab error codes this fake answers with. Sonarr treats 100-199 as a
// bad API key, and "Request limit reached" as a rate limit it backs off from.
const (
	ErrIncorrectCredentials = 100
	ErrMissingParameter     = 200
	ErrIncorrectParameter   = 201
	ErrNoSuchFunction       = 202
	ErrNoSuchItem           = 300
	ErrRequestLimitReached  = 500
	ErrUnknown              = 900
)

// DefaultPageSize is the page size caps advertises by default: Sonarr pages
// by min(100, max(default, max)), so 100 is what it asks for anyway.
const DefaultPageSize = 100

// defaultTVSearchParams are what caps advertises for tv-search: enough for
// Sonarr to search by id (tvdbid), by title (q), and by season and episode,
// which its TestCapabilities requires.
var defaultTVSearchParams = []string{"q", "season", "ep", "tvdbid"}

// Release is one entry in the indexer's catalogue.
type Release struct {
	// GUID identifies the release in its links and NZB download; empty
	// derives a stable one from the title.
	GUID string
	// Title is the scene name Sonarr parses the series, episode, quality
	// and group from, e.g. Firefly.S01E07.Jaynestown.1080p.WEB-DL.DDP5.1.H.264-FAKE.
	Title string
	// Size is the release size in bytes, which Sonarr checks against the
	// quality definition's size limits; 0 is unknown, and not checked.
	Size int64
	// PubDate is when the release was posted; zero is the moment it was
	// added. RSS lists newest first, and Sonarr rejects releases older than
	// its retention.
	PubDate time.Time
	// Category is the subcategory the release is filed under; 0 is HD.
	Category int
	// TVDBID, TvMazeID and IMDBID are the series ids a search by id
	// matches; IMDBID is written tt-prefixed, e.g. tt0303461.
	TVDBID   int
	TvMazeID int
	IMDBID   string
	// Season and Episode are what a search by season and episode matches.
	// Episode 0 is a season pack.
	Season  int
	Episode int
	// Grabs counts NZB downloads; every t=get adds one.
	Grabs int
	// Files is the number of files the NZB lists; 0 is one.
	Files int
	// Group and Poster fill the usenet group and poster the feed and the NZB
	// name; empty is alt.binaries.teevee and a fake poster.
	Group  string
	Poster string
	// Languages are language names (English, German) Sonarr reads from the
	// language attribute instead of the title.
	Languages []string
	// Scene and Nuked set the prematch and nuked attributes Sonarr turns
	// into indexer flags.
	Scene bool
	Nuked bool
}

// Request is one API call the indexer received.
type Request struct {
	Method string
	Path   string
	// Function is the t= parameter: caps, tvsearch, search, get.
	Function string
	// Query is every parameter as sent, the API key included.
	Query url.Values
	Time  time.Time
}

// Failure is how the indexer misbehaves; the zero value is healthy.
type Failure struct {
	// Status, when set, is the HTTP status every API call answers, the way
	// an indexer that is down answers a 503. Sonarr records a failure for
	// the indexer, and enough of them disable it and raise a health check.
	Status int
	// Code, when set and Status is not, answers every API call with a
	// Newznab error document under HTTP 200, the way an indexer reports a
	// rate limit (ErrRequestLimitReached, "Request limit reached") or a key
	// it has revoked (ErrIncorrectCredentials).
	Code        int
	Description string
}

// Options configure a Server.
type Options struct {
	// Addr is where to listen, e.g. ":18081"; ":0" picks a free port. The
	// container reaches the host through host.docker.internal, so a live run
	// listens on every interface.
	Addr string
	// APIKey is the key every call but caps must carry as apikey=; empty
	// accepts any.
	APIKey string
	// BaseURL is the address Sonarr is given for the indexer, and the one the
	// feed's links point at, e.g. http://host.docker.internal:18081. Empty
	// builds it from PublicHost and the port listened on.
	BaseURL string
	// PublicHost is the host the container reaches this process by, used
	// when BaseURL is empty; default 127.0.0.1.
	PublicHost string
	// TVSearchParams are the tv-search parameters caps advertises; default
	// q, season, ep and tvdbid. Sonarr sends only the ids listed here.
	TVSearchParams []string
	// PageSize is the default and maximum page size caps advertises;
	// default 100.
	PageSize int
	// Releases is the starting catalogue. Sonarr's test of a new indexer
	// fetches the RSS feed and fails unless it lists something in the
	// categories configured, so seed at least one before adding it.
	Releases []Release
}

// Server is a running fake indexer.
type Server struct {
	apiKey         string
	baseURL        string
	tvSearchParams []string
	pageSize       int

	listener net.Listener
	srv      *http.Server

	mu       sync.Mutex
	releases []Release
	requests []Request
	failure  Failure
}

// New starts an indexer listening on opts.Addr.
func New(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.PageSize <= 0 {
		opts.PageSize = DefaultPageSize
	}
	if len(opts.TVSearchParams) == 0 {
		opts.TVSearchParams = defaultTVSearchParams
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("newznab: listening on %s: %w", opts.Addr, err)
	}

	s := &Server{
		apiKey:         opts.APIKey,
		baseURL:        strings.TrimRight(opts.BaseURL, "/"),
		tvSearchParams: slices.Clone(opts.TVSearchParams),
		pageSize:       opts.PageSize,
		listener:       ln,
	}
	if s.baseURL == "" {
		host := opts.PublicHost
		if host == "" {
			host = "127.0.0.1"
		}
		s.baseURL = "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port()))
	}
	s.SetReleases(opts.Releases)

	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()

	return s, nil
}

// URL is the indexer's address as the container reaches it: the base URL
// Sonarr is configured with, and the root of the feed's links.
func (s *Server) URL() string { return s.baseURL }

// LocalURL is the indexer's address from this process.
func (s *Server) LocalURL() string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))
}

// Addr is the address listened on.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Port is the port listened on.
func (s *Server) Port() int {
	if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}

	return 0
}

// Close stops the indexer at once. There is nothing to drain, and a graceful
// shutdown waits five seconds on any connection a client opened and never
// used.
func (s *Server) Close() error { return s.srv.Close() }

// SetReleases replaces the catalogue.
func (s *Server) SetReleases(releases []Release) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.releases = s.releases[:0]
	for _, r := range releases {
		s.releases = append(s.releases, normalise(r))
	}
}

// AddRelease adds to the catalogue, replacing any release with the same GUID.
func (s *Server) AddRelease(releases ...Release) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range releases {
		r = normalise(r)
		if i := slices.IndexFunc(s.releases, func(have Release) bool { return have.GUID == r.GUID }); i >= 0 {
			s.releases[i] = r
			continue
		}
		s.releases = append(s.releases, r)
	}
}

// Releases returns the catalogue, with the defaults filled in and the grab
// counts as they stand.
func (s *Server) Releases() []Release {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Release, len(s.releases))
	for i, r := range s.releases {
		r.Languages = slices.Clone(r.Languages)
		out[i] = r
	}

	return out
}

// Requests returns every API call received since the last Reset, oldest
// first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// RequestsFor returns the calls of one function: caps, tvsearch, search or
// get.
func (s *Server) RequestsFor(function string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Function == function {
			out = append(out, r)
		}
	}

	return out
}

// Reset forgets the requests received so far.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests = nil
}

// SetFailure makes every API call fail as f describes, until it is called
// again with the zero Failure.
func (s *Server) SetFailure(f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failure = f
}

// normalise fills a release's defaults.
func normalise(r Release) Release {
	if r.GUID == "" {
		sum := sha1.Sum([]byte(r.Title)) //nolint:gosec // see the import
		r.GUID = hex.EncodeToString(sum[:])
	}
	if r.Category == 0 {
		r.Category = CategoryHD
	}
	if r.PubDate.IsZero() {
		r.PubDate = time.Now().UTC().Truncate(time.Second)
	}
	if r.Files <= 0 {
		r.Files = 1
	}
	if r.Group == "" {
		r.Group = "alt.binaries.teevee"
	}
	if r.Poster == "" {
		r.Poster = "poster@sonarr-mcp.invalid (fake)"
	}
	r.Languages = slices.Clone(r.Languages)

	return r
}

// errNoRelease is a download of a GUID the catalogue does not hold.
var errNoRelease = errors.New("no such release")

// grab returns the release with guid and counts the download.
func (s *Server) grab(guid string) (Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.releases {
		if s.releases[i].GUID == guid {
			s.releases[i].Grabs++
			return s.releases[i], nil
		}
	}

	return Release{}, errNoRelease
}
