package newznab

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// search is one search call, read.
type search struct {
	function   string
	categories []int
	// terms are the q= words, lower case; a release matches when its title
	// holds every one of them as a word.
	terms []string
	// the ids asked for; a release matches when it carries any of them
	tvdbID, tvMazeID, tmdbID, imdbID int
	season, episode                  int
	hasSeason, hasEpisode            bool
	offset, limit                    int
}

// parseSearch reads the parameters an *arr's request generator sends: cat, q,
// the ids the capabilities listed, season (00 for specials), ep, and offset
// and limit for the page. extended=1 changes nothing, because every attribute
// is always sent. wrong names a parameter that does not read, "" for none.
func parseSearch(function string, q url.Values, pageSize int) (s search, wrong string) {
	s = search{function: function, limit: pageSize, terms: words(q.Get("q"))}

	for c := range strings.SplitSeq(q.Get("cat"), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		n, err := strconv.Atoi(c)
		if err != nil {
			return search{}, "cat"
		}
		s.categories = append(s.categories, n)
	}

	for _, number := range []struct {
		name string
		into *int
	}{{"tvdbid", &s.tvdbID}, {"tvmazeid", &s.tvMazeID}, {"tmdbid", &s.tmdbID}, {"imdbid", &s.imdbID}, {"offset", &s.offset}, {"limit", &s.limit}} {
		v := q.Get(number.name)
		if v == "" {
			continue
		}
		if number.name == "imdbid" {
			v = strings.TrimPrefix(strings.ToLower(v), "tt")
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return search{}, number.name
		}
		*number.into = n
	}
	if s.limit <= 0 || s.limit > pageSize {
		s.limit = pageSize
	}
	s.offset = max(s.offset, 0)

	// season is a number, 00 for specials, sometimes S01; ep is a number, or
	// MM/DD for a daily show, which no release here is filed by
	if v := strings.TrimPrefix(strings.ToUpper(q.Get("season")), "S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return search{}, "season"
		}
		s.season, s.hasSeason = n, true
	}
	if v := strings.TrimPrefix(strings.ToUpper(q.Get("ep")), "E"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.episode, s.hasEpisode = n, true
		}
	}

	return s, ""
}

// run returns the page of releases the search matches, newest first, and how
// many matched in all.
func (s *search) run(releases []Release) (page []Release, total int) {
	matched := make([]Release, 0, len(releases))
	for i := range releases {
		if s.matches(&releases[i]) {
			matched = append(matched, releases[i])
		}
	}
	slices.SortStableFunc(matched, func(a, b Release) int { return cmp.Or(b.PubDate.Compare(a.PubDate), cmp.Compare(a.Title, b.Title)) })

	total = len(matched)
	if s.offset >= total {
		return []Release{}, total
	}

	return matched[s.offset:min(s.offset+s.limit, total)], total
}

func (s *search) matches(r *Release) bool {
	if len(s.categories) > 0 && !slices.Contains(s.categories, r.Category) && !slices.Contains(s.categories, parentOf(r.Category)) {
		return false
	}

	// ids are a TV or a movie search's parameters, and season and episode a
	// TV search's; a plain search is text alone
	byID := s.function == functionTVSearch || s.function == functionMovie
	if byID && s.tvdbID+s.tvMazeID+s.tmdbID+s.imdbID != 0 && !s.carries(r) {
		return false
	}
	if s.function == functionTVSearch && (s.hasSeason && r.Season != s.season || s.hasEpisode && r.Episode != s.episode) {
		return false
	}

	title := words(r.Title)
	for _, term := range s.terms {
		if !slices.Contains(title, term) {
			return false
		}
	}

	return true
}

// carries reports whether a release has any of the ids asked for.
func (s *search) carries(r *Release) bool {
	switch {
	case s.tvdbID != 0 && r.TVDBID == s.tvdbID:
	case s.tvMazeID != 0 && r.TvMazeID == s.tvMazeID:
	case s.tmdbID != 0 && r.TMDBID == s.tmdbID:
	case s.imdbID != 0 && imdbNumber(r.IMDBID) == s.imdbID:
	default:
		return false
	}

	return true
}

// parentOf is a category's top-level category: 5040 is under 5000.
func parentOf(category int) int { return category / 1000 * 1000 }

// words splits text into lower-case words at anything that is not a letter
// or a digit, the way a scene name separates them with dots and dashes.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// imdbNumber is the number of an IMDb id, written with its tt or without; 0
// when there is none.
func imdbNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(id), "tt"))
	if err != nil {
		return 0
	}

	return n
}
