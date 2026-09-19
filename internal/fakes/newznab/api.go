package newznab

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The API functions, the t= parameter.
const (
	functionCaps     = "caps"
	functionTVSearch = "tvsearch"
	functionSearch   = "search"
	functionGet      = "get"
)

const (
	contentTypeXML = "application/xml; charset=utf-8"
	contentTypeRSS = "application/rss+xml; charset=utf-8"
	contentTypeNZB = "application/x-nzb"
)

// ServeHTTP answers the Newznab API on /api (any path ending in /api, so an
// indexer configured with an API path such as /newznab/api works too).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api" && !strings.HasSuffix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	function := strings.ToLower(query.Get("t"))

	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		Function: function,
		Query:    query,
		Time:     time.Now(),
	})
	failure := s.failure
	s.mu.Unlock()

	// a failing indexer fails everything, caps included: Sonarr caches caps
	// for a week, so it is the searches that show the failure
	switch {
	case failure.Status != 0:
		http.Error(w, http.StatusText(failure.Status), failure.Status)
		return
	case failure.Code != 0:
		writeError(w, http.StatusOK, failure.Code, failure.Description)
		return
	}

	// real indexers answer caps without a key; Sonarr sends one anyway
	if function == functionCaps {
		writeXML(w, contentTypeXML, s.capsDocument())
		return
	}

	switch key := query.Get("apikey"); {
	case s.apiKey == "":
	case key == "":
		// Sonarr reads "apikey" in the description of a request it sent
		// without one as the indexer needing a key
		writeError(w, http.StatusOK, ErrMissingParameter, "Missing parameter (apikey)")
		return
	case key != s.apiKey:
		writeError(w, http.StatusOK, ErrIncorrectCredentials, "Incorrect user credentials")
		return
	}

	switch function {
	case functionTVSearch, functionSearch:
		s.serveSearch(w, function, query)
	case functionGet:
		s.serveNZB(w, query.Get("id"))
	case "":
		writeError(w, http.StatusOK, ErrMissingParameter, "Missing parameter (t)")
	default:
		writeError(w, http.StatusOK, ErrNoSuchFunction, "No such function ("+function+")")
	}
}

// serveSearch answers tvsearch and search with the matching page of the
// catalogue.
func (s *Server) serveSearch(w http.ResponseWriter, function string, query url.Values) {
	q, err := parseSearch(function, query, s.pageSize)
	if err != nil {
		description := err.Error()
		if pe, ok := errors.AsType[*parameterError](err); ok {
			description = pe.description()
		}
		writeError(w, http.StatusOK, ErrIncorrectParameter, description)
		return
	}

	s.mu.Lock()
	page, total := q.run(s.releases)
	s.mu.Unlock()

	writeXML(w, contentTypeRSS, s.feedDocument(page, q.offset, total))
}

// serveNZB answers a download with the release's NZB.
func (s *Server) serveNZB(w http.ResponseWriter, guid string) {
	if guid == "" {
		writeError(w, http.StatusOK, ErrMissingParameter, "Missing parameter (id)")
		return
	}
	release, err := s.grab(guid)
	if errors.Is(err, errNoRelease) {
		// a 404 is what Sonarr reads as a release that no longer exists
		writeError(w, http.StatusNotFound, ErrNoSuchItem, "No such item")
		return
	}

	w.Header().Set("Content-Type", contentTypeNZB)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(release.Title, `"`, "")+`.nzb"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, nzbDocument(&release))
}

// writeError answers a Newznab error document.
func writeError(w http.ResponseWriter, status, code int, description string) {
	w.Header().Set("Content-Type", contentTypeXML)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<error code="`+strconv.Itoa(code)+`" description="`+escape(description)+`"/>`+"\n")
}

func writeXML(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}
