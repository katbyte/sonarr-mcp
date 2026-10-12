package newznab

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The API's functions, the t= parameter, and the name a fetch of a release is recorded under.
const (
	functionCaps     = "caps"
	functionSearch   = "search"
	functionTVSearch = "tvsearch"
	functionMovie    = "movie"
	functionMusic    = "music"
	functionBook     = "book"
	functionDownload = "download"
)

const (
	contentTypeXML     = "application/xml; charset=utf-8"
	contentTypeRSS     = "application/rss+xml; charset=utf-8"
	contentTypeNZB     = "application/x-nzb"
	contentTypeTorrent = "application/x-bittorrent"
)

// ServeHTTP answers each indexer under its name: the API at /<name>/api, or any path under the name ending in /api, and a release at
// /<name>/download/<guid>. An error names the parameter at fault, never what it was sent as.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	query := r.URL.Query()
	function := strings.ToLower(query.Get("t"))
	guid, download := strings.CutPrefix(rest, "download/")
	if download {
		function = functionDownload
	}

	s.mu.Lock()
	ix := s.indexers[name]
	var site Site
	if ix != nil {
		site = ix.site
		s.requests = append(s.requests, Request{Site: name, Method: r.Method, Path: r.URL.Path, Function: function, Query: query, Time: time.Now()})
	}
	s.mu.Unlock()

	if ix == nil || !download && rest != "api" && !strings.HasSuffix(rest, "/api") {
		http.NotFound(w, r)

		return
	}

	if site.Delay > 0 {
		select {
		case <-time.After(site.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if ix.misbehaves(w, function) {
		return
	}

	// a real indexer answers its capabilities without a key; an *arr sends one anyway
	if function == functionCaps {
		writeXML(w, contentTypeXML, capsDocument(&site))

		return
	}

	switch key := query.Get("apikey"); {
	case site.APIKey == "":
	case key == "":
		// an *arr reads "apikey" in the description of a request it sent without one as the indexer needing a key
		writeError(w, http.StatusOK, ErrMissingParameter, "Missing parameter (apikey)")

		return
	case key != site.APIKey:
		writeError(w, http.StatusOK, ErrIncorrectCredentials, "Incorrect user credentials")

		return
	}

	switch function {
	case functionDownload:
		ix.serveRelease(w, guid)
	case functionSearch, functionTVSearch, functionMovie, functionMusic, functionBook:
		ix.serveSearch(w, function, query)
	case "":
		writeError(w, http.StatusOK, ErrMissingParameter, "Missing parameter (t)")
	default:
		writeError(w, http.StatusOK, ErrNoSuchFunction, "No such function")
	}
}

// misbehaves answers for an indexer that is failing, and reports whether it did; false is to answer as a healthy one would.
func (ix *Indexer) misbehaves(w http.ResponseWriter, function string) bool {
	ix.server.mu.Lock()
	f := ix.site.Failure
	failing := f.Status != 0 || f.Code != 0
	if failing && f.Every > 1 {
		failing = ix.flakes(function)
	}
	ix.server.mu.Unlock()

	switch {
	case !failing:
		return false
	case f.Status != 0:
		http.Error(w, http.StatusText(f.Status), f.Status)
	default:
		description := f.Description
		if description == "" {
			description = "Unknown error"
		}
		writeError(w, http.StatusOK, f.Code, description)
	}

	return true
}

// flakes reports whether a flaky indexer fails this call: every Every-th search, and each after it until the spell ends. The caller holds the lock.
func (ix *Indexer) flakes(function string) bool {
	if function == functionCaps || function == functionDownload {
		return false
	}
	if time.Now().Before(ix.failUntil) {
		return true
	}

	ix.searches++
	if ix.searches%ix.site.Failure.Every != 0 {
		return false
	}
	ix.failUntil = time.Now().Add(failureSpell)

	return true
}

// serveSearch answers a search with the matching page of the catalogue.
func (ix *Indexer) serveSearch(w http.ResponseWriter, function string, query url.Values) {
	ix.server.mu.Lock()
	site := ix.site
	q, wrong := parseSearch(function, query, site.PageSize)
	var page []Release
	total := 0
	if wrong == "" {
		page, total = q.run(site.Releases)
	}
	ix.server.mu.Unlock()

	if wrong != "" {
		writeError(w, http.StatusOK, ErrIncorrectParameter, "Incorrect parameter ("+wrong+")")

		return
	}

	writeXML(w, contentTypeRSS, ix.feedDocument(&site, page, q.offset, total))
}

// serveRelease answers a fetch with the release's NZB or torrent, and counts the grab.
func (ix *Indexer) serveRelease(w http.ResponseWriter, guid string) {
	guid = strings.TrimSuffix(strings.TrimSuffix(guid, ".nzb"), ".torrent")

	ix.server.mu.Lock()
	protocol := ix.site.Protocol
	i := slices.IndexFunc(ix.site.Releases, func(r Release) bool { return r.GUID == guid })
	var release Release
	if i >= 0 {
		ix.site.Releases[i].Grabs++
		release = ix.site.Releases[i]
		ix.grabbed = append(ix.grabbed, release.Title)
	}
	ix.server.mu.Unlock()

	if i < 0 {
		// a 404 is what an *arr reads as a release that is no longer there
		writeError(w, http.StatusNotFound, ErrNoSuchItem, "No such item")

		return
	}

	contentType, extension, body := contentTypeNZB, ".nzb", nzbDocument(&release)
	if protocol == Torrent {
		contentType, extension, body = contentTypeTorrent, ".torrent", torrentFile(&release)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(release.Title, `"`, "")+extension+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

// writeError answers a Newznab error document.
func writeError(w http.ResponseWriter, status, code int, description string) {
	w.Header().Set("Content-Type", contentTypeXML)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<error code="`+strconv.Itoa(code)+`" description="`+escape(description)+`"/>`+"\n")
}

func writeXML(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}
