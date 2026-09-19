package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	apiclient "github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"golang.org/x/text/unicode/norm"
)

// The library's series, read once and matched against in process.
//
// Sonarr answers every series in one call, so a name is matched against the
// whole library rather than asking Sonarr to search: every tool that takes a
// series takes a title as readily as an id, and the matching has to be the
// same everywhere and has to say so when a title is ambiguous rather than
// quietly picking one of "The Office" (2001) and "The Office" (2005).

// seriesIndexTTL is how long a read of the library is trusted. A write through
// this server drops it at once; the TTL is for changes made anywhere else -
// Sonarr's own web interface, an import list - and a name the index cannot
// place is looked for again in a fresh read regardless, so a show added a
// minute ago still resolves.
const seriesIndexTTL = 2 * time.Minute

type seriesIndex struct {
	series []sonarr.SeriesResource
	byID   map[int]int
	read   time.Time
}

// seriesCache holds the one index.
type seriesCache struct {
	mu  sync.Mutex
	idx *seriesIndex
}

func (r *registry) seriesCache() *seriesCache {
	r.seriesOnce.Do(func() { r.series = &seriesCache{} })

	return r.series
}

// invalidate drops the index, so the next call reads the library again.
func (c *seriesCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.idx = nil
}

// get returns the index, reading the library when there is none, it has gone
// stale, or fresh is set.
func (c *seriesCache) get(ctx context.Context, client *sonarr.Client, fresh bool) (*seriesIndex, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.idx != nil && !fresh && time.Since(c.idx.read) < seriesIndexTTL {
		return c.idx, nil
	}
	res, err := client.GetSeries(ctx, sonarr.GetSeriesOperationOptions{})
	if err != nil {
		return nil, err
	}
	idx := &seriesIndex{series: res.Model, byID: map[int]int{}, read: time.Now()}
	for i := range idx.series {
		idx.byID[idx.series[i].Id] = i
	}
	c.idx = idx

	return idx, nil
}

// allSeries returns every series in the library, sorted by title, read
// fresh: a list reports each series' statistics, which Sonarr changes on its
// own as downloads import, so the cached read is only for resolving names.
// The fresh read replaces the cached one.
func (r *registry) allSeries(ctx context.Context) ([]sonarr.SeriesResource, error) {
	idx, err := r.seriesCache().get(ctx, r.client, true)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(idx.series)
	slices.SortFunc(out, func(a, b sonarr.SeriesResource) int {
		if c := strings.Compare(sortKey(&a), sortKey(&b)); c != 0 {
			return c
		}
		return a.Id - b.Id
	})

	return out, nil
}

func sortKey(s *sonarr.SeriesResource) string {
	if s.SortTitle != "" {
		return s.SortTitle
	}

	return normaliseTitle(s.Title)
}

// seriesByID reads one series as it stands now.
func (r *registry) seriesByID(ctx context.Context, id int) (*sonarr.SeriesResource, error) {
	res, err := r.client.GetSeriesById(ctx, id, sonarr.GetSeriesByIdOperationOptions{})
	switch {
	case apiclient.IsNotFound(err):
		return nil, fmt.Errorf("no series with id %d in Sonarr", id)
	case err != nil:
		return nil, err
	}

	return res.Model, nil
}

// seriesMatch is a candidate a name could mean, for an ambiguity report.
type seriesMatch struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	TvdbID int    `json:"tvdb_id,omitempty"`
}

func matchOf(s *sonarr.SeriesResource) seriesMatch {
	return seriesMatch{ID: s.Id, Title: s.Title, Year: s.Year, TvdbID: s.TvdbId}
}

func (m seriesMatch) String() string {
	return fmt.Sprintf("%s (%d) [id %d, tvdb %d]", m.Title, m.Year, m.ID, m.TvdbID)
}

// resolveSeries finds the one series a reference names: a Sonarr id, a
// provider id (tvdb:78874, imdb:tt0303461, tmdb:1437), or a title, with or
// without its year ("Firefly", "firefly (2002)", "Firefly 2002"). A title
// matches the series title, any of Sonarr's alternate and scene titles, or,
// failing an exact match, a series whose title starts every one of its
// words. More than one match is an error listing them, never a guess.
func (r *registry) resolveSeries(ctx context.Context, ref string) (*sonarr.SeriesResource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("name a series: a title, a Sonarr id, or tvdb:<id>")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		return r.seriesByID(ctx, id)
	}

	for _, fresh := range []bool{false, true} {
		idx, err := r.seriesCache().get(ctx, r.client, fresh)
		if err != nil {
			return nil, err
		}
		found, err := idx.match(ref)
		switch {
		case err != nil:
			return nil, err
		case found != nil:
			// the index names it; the series itself is read now, since its
			// statistics and settings may have changed since the index was
			return r.seriesByID(ctx, found.Id)
		}
	}

	return nil, fmt.Errorf("no series in Sonarr matches %q; name it by title, id or tvdb:<id>, or look it up to add it", ref)
}

var (
	providerRef = regexp.MustCompile(`^(?i)(tvdb|imdb|tmdb|tvmaze):\s*(\S+)$`)
	yearSuffix  = regexp.MustCompile(`^(.*?)\s*\(?((?:19|20)\d\d)\)?$`)
)

// match finds ref in the index: nil, nil when nothing matches, an error when
// more than one series does.
func (idx *seriesIndex) match(ref string) (*sonarr.SeriesResource, error) {
	if m := providerRef.FindStringSubmatch(ref); m != nil {
		return idx.matchProvider(strings.ToLower(m[1]), m[2])
	}

	title, year := ref, 0
	if m := yearSuffix.FindStringSubmatch(ref); len(m) == 3 && strings.TrimSpace(m[1]) != "" {
		title = m[1]
		year, _ = strconv.Atoi(m[2])
	}
	want := normaliseTitle(title)
	whole := normaliseTitle(ref)

	var exact, prefix []int
	for i := range idx.series {
		s := &idx.series[i]
		names := append([]string{s.Title}, alternateTitles(s)...)
		hit := false
		for _, n := range names {
			n = normaliseTitle(n)
			// a title that itself ends in a year ("Doctor Who (2005)") is
			// matched whole before the year is taken as a filter
			if n == whole || n == want && (year == 0 || s.Year == year) {
				hit = true
				break
			}
		}
		switch {
		case hit:
			exact = append(exact, i)
		case (year == 0 || s.Year == year) && wordPrefixes(want, normaliseTitle(s.Title)):
			prefix = append(prefix, i)
		}
	}

	for _, set := range [][]int{exact, prefix} {
		switch len(set) {
		case 0:
			continue
		case 1:
			return new(idx.series[set[0]]), nil
		default:
			matches := make([]string, 0, len(set))
			for _, i := range set {
				matches = append(matches, matchOf(&idx.series[i]).String())
			}
			slices.Sort(matches)
			return nil, fmt.Errorf("%q matches %d series: %s; add the year, or use the id or tvdb:<id>", ref, len(set), strings.Join(matches, ", "))
		}
	}

	return nil, nil //nolint:nilnil // nothing matched, which the caller reports once it has read the library again
}

func (idx *seriesIndex) matchProvider(provider, id string) (*sonarr.SeriesResource, error) {
	for i := range idx.series {
		s := &idx.series[i]
		var have string
		switch provider {
		case "tvdb":
			have = strconv.Itoa(s.TvdbId)
		case "tmdb":
			have = strconv.Itoa(s.TmdbId)
		case "tvmaze":
			have = strconv.Itoa(s.TvMazeId)
		case "imdb":
			have = s.ImdbId
		}
		if have != "" && have != "0" && strings.EqualFold(have, id) {
			return new(*s), nil
		}
	}

	return nil, nil //nolint:nilnil // nothing matched, which the caller reports once it has read the library again
}

// alternateTitles are the scene and alternate titles Sonarr knows a series by.
func alternateTitles(s *sonarr.SeriesResource) []string {
	out := make([]string, 0, len(s.AlternateTitles))
	for _, a := range s.AlternateTitles {
		if a.Title != "" {
			out = append(out, a.Title)
		}
	}

	return out
}

// wordPrefixes reports whether every word of want starts a different word of
// have, in order: "sev" finds "Severance", "star trek next" finds "Star Trek:
// The Next Generation".
func wordPrefixes(want, have string) bool {
	ws, hs := strings.Fields(want), strings.Fields(have)
	if len(ws) == 0 {
		return false
	}
	j := 0
	for _, w := range ws {
		for j < len(hs) && !strings.HasPrefix(hs[j], w) {
			j++
		}
		if j == len(hs) {
			return false
		}
		j++
	}

	return true
}

// normaliseTitle folds a title to the form names are compared in: lower case,
// accents dropped, "&" read as "and", punctuation as spaces, and single spaces.
func normaliseTitle(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")
	var b strings.Builder
	space := true
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// a combining accent: dropped, so "Café" is "cafe"
		case r == '\'' || r == '’':
			// an apostrophe joins its word: "Grey's" is "greys"
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}

	return strings.TrimSpace(b.String())
}
