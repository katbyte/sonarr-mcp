package newznab

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// search is one parsed tvsearch or search call.
type search struct {
	function   string
	categories []int
	// terms are the q= words, lower case; a release matches when its title
	// holds every one of them as a word.
	terms []string
	// the ids asked for; a release matches when it carries any of them
	tvdbID, tvMazeID, imdbID int
	season, episode          int
	hasSeason, hasEpisode    bool
	offset, limit            int
}

// parameterError is a search parameter that does not parse.
type parameterError struct{ name, value string }

func (e *parameterError) Error() string { return "incorrect parameter " + e.name + "=" + e.value }

// description is how an indexer words it in an error document.
func (e *parameterError) description() string {
	return "Incorrect parameter (" + e.name + "=" + e.value + ")"
}

// parseSearch reads the parameters Sonarr's NewznabRequestGenerator sends:
// cat, q, the series ids caps advertised, season (00 for specials), ep, and
// offset and limit for the page. extended=1 changes nothing here, because
// every attribute is always sent.
func parseSearch(function string, q url.Values, pageSize int) (search, error) {
	s := search{function: function, limit: pageSize, terms: words(q.Get("q"))}

	for c := range strings.SplitSeq(q.Get("cat"), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		n, err := strconv.Atoi(c)
		if err != nil {
			return search{}, &parameterError{"cat", c}
		}
		s.categories = append(s.categories, n)
	}

	var err error
	for _, id := range []struct {
		name string
		into *int
	}{{"tvdbid", &s.tvdbID}, {"tvmazeid", &s.tvMazeID}, {"imdbid", &s.imdbID}, {"offset", &s.offset}, {"limit", &s.limit}} {
		v := q.Get(id.name)
		if v == "" {
			continue
		}
		if id.name == "imdbid" {
			v = strings.TrimPrefix(strings.ToLower(v), "tt")
		}
		if *id.into, err = strconv.Atoi(v); err != nil {
			return search{}, &parameterError{id.name, q.Get(id.name)}
		}
	}
	if s.limit <= 0 || s.limit > pageSize {
		s.limit = pageSize
	}
	s.offset = max(s.offset, 0)

	// season is a number, 00 for specials, sometimes S01; ep is a number,
	// or MM/DD for a daily show, which no release here is filed by
	if v := strings.TrimPrefix(strings.ToUpper(q.Get("season")), "S"); v != "" {
		if s.season, err = strconv.Atoi(v); err != nil {
			return search{}, &parameterError{"season", q.Get("season")}
		}
		s.hasSeason = true
	}
	if v := strings.TrimPrefix(strings.ToUpper(q.Get("ep")), "E"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.episode, s.hasEpisode = n, true
		}
	}

	return s, nil
}

// run returns the page of releases the search matches, newest first, and
// how many matched in all.
func (s *search) run(releases []Release) (page []Release, total int) {
	matched := make([]Release, 0, len(releases))
	for i := range releases {
		if s.matches(&releases[i]) {
			matched = append(matched, releases[i])
		}
	}
	slices.SortStableFunc(matched, func(a, b Release) int {
		if c := b.PubDate.Compare(a.PubDate); c != 0 {
			return c
		}
		return cmp.Compare(a.Title, b.Title)
	})

	total = len(matched)
	if s.offset >= total {
		return []Release{}, total
	}

	return matched[s.offset:min(s.offset+s.limit, total)], total
}

func (s *search) matches(r *Release) bool {
	if len(s.categories) > 0 && !slices.Contains(s.categories, r.Category) && !slices.Contains(s.categories, r.Category/1000*1000) {
		return false
	}
	// ids, season and episode are tv-search parameters; a plain search is
	// text alone
	if s.function == functionTVSearch {
		if s.tvdbID != 0 || s.tvMazeID != 0 || s.imdbID != 0 {
			byID := s.tvdbID != 0 && r.TVDBID == s.tvdbID ||
				s.tvMazeID != 0 && r.TvMazeID == s.tvMazeID ||
				s.imdbID != 0 && imdbNumber(r.IMDBID) == s.imdbID
			if !byID {
				return false
			}
		}
		if s.hasSeason && r.Season != s.season {
			return false
		}
		if s.hasEpisode && r.Episode != s.episode {
			return false
		}
	}
	if len(s.terms) > 0 {
		title := words(r.Title)
		for _, term := range s.terms {
			if !slices.Contains(title, term) {
				return false
			}
		}
	}

	return true
}

// words splits text into lower-case words at anything that is not a letter
// or a digit, the way a scene name separates them with dots and dashes.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// imdbNumber is the number of an IMDb id, tt-prefixed or not; 0 when there is
// none.
func imdbNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(id), "tt"))
	if err != nil {
		return 0
	}

	return n
}
