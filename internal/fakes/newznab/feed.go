package newznab

import (
	"encoding/xml"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// attrNamespace is the namespace Sonarr's NewznabRssParser reads the
// newznab:attr elements from. It is a name, never fetched, and https would be
// a different name.
const attrNamespace = "http://www.newznab.com/DTD/2010/feeds/attributes/" //nolint:revive // unsecure-url-scheme: a namespace identifier, see above

// categoryNames label the subcategories in caps and in each item's category.
var categoryNames = map[int]string{
	CategoryForeign:     "Foreign",
	CategorySD:          "SD",
	CategoryHD:          "HD",
	CategoryUHD:         "UHD",
	CategoryOther:       "Other",
	CategorySport:       "Sport",
	CategoryAnime:       "Anime",
	CategoryDocumentary: "Documentary",
}

// subcategories are listed in caps in this order.
var subcategories = []int{CategoryForeign, CategorySD, CategoryHD, CategoryUHD, CategoryOther, CategorySport, CategoryAnime, CategoryDocumentary}

// capsDocument is the t=caps answer: what NewznabCapabilitiesProvider parses.
// search and tv-search are available, tv-search with the parameters Sonarr's
// TestCapabilities needs (an id or q, plus season and ep), and every TV
// subcategory is listed so its category picker offers them.
func (s *Server) capsDocument() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<caps>\n")
	b.WriteString(`  <server version="1.0" title="sonarr-mcp fake indexer" strapline="Newznab for the live suites" url="` + escape(s.baseURL) + `/"/>` + "\n")
	b.WriteString(`  <limits max="` + strconv.Itoa(s.pageSize) + `" default="` + strconv.Itoa(s.pageSize) + `"/>` + "\n")
	b.WriteString(`  <registration available="no" open="no"/>` + "\n")
	b.WriteString("  <searching>\n")
	b.WriteString(`    <search available="yes" supportedParams="q"/>` + "\n")
	b.WriteString(`    <tv-search available="yes" supportedParams="` + escape(strings.Join(s.tvSearchParams, ",")) + `"/>` + "\n")
	b.WriteString(`    <movie-search available="no" supportedParams="q"/>` + "\n")
	b.WriteString(`    <audio-search available="no" supportedParams="q"/>` + "\n")
	b.WriteString(`    <book-search available="no" supportedParams="q"/>` + "\n")
	b.WriteString("  </searching>\n")
	b.WriteString("  <categories>\n")
	b.WriteString(`    <category id="` + strconv.Itoa(CategoryTV) + `" name="TV">` + "\n")
	for _, id := range subcategories {
		b.WriteString(`      <subcat id="` + strconv.Itoa(id) + `" name="` + categoryNames[id] + `"/>` + "\n")
	}
	b.WriteString("    </category>\n  </categories>\n</caps>\n")

	return b.String()
}

// feedDocument is a search or RSS answer: an RSS 2.0 channel of items in the
// shape of the nzb.su feed Sonarr's own tests parse, with the attributes its
// NewznabRssParser reads (size, usenetdate, tvdbid, imdb, language, prematch,
// nuked) and the ones a real indexer sends besides.
func (s *Server) feedDocument(page []Release, offset, total int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:newznab="` + attrNamespace + `">` + "\n")
	b.WriteString("  <channel>\n")
	b.WriteString(`    <atom:link href="` + escape(s.baseURL) + `/api" rel="self" type="application/rss+xml"/>` + "\n")
	b.WriteString("    <title>sonarr-mcp fake indexer</title>\n")
	b.WriteString("    <description>sonarr-mcp fake indexer feed</description>\n")
	b.WriteString("    <link>" + escape(s.baseURL) + "/</link>\n")
	b.WriteString("    <language>en-gb</language>\n")
	b.WriteString(`    <newznab:response offset="` + strconv.Itoa(offset) + `" total="` + strconv.Itoa(total) + `"/>` + "\n")
	for i := range page {
		s.writeItem(&b, &page[i])
	}
	b.WriteString("  </channel>\n</rss>\n")

	return b.String()
}

func (s *Server) writeItem(b *strings.Builder, r *Release) {
	details := s.baseURL + "/details/" + url.PathEscape(r.GUID)
	download := s.downloadURL(r.GUID)
	posted := r.PubDate.UTC().Format(time.RFC1123Z)
	size := strconv.FormatInt(r.Size, 10)

	b.WriteString("    <item>\n")
	b.WriteString("      <title>" + escape(r.Title) + "</title>\n")
	b.WriteString(`      <guid isPermaLink="true">` + escape(details) + "</guid>\n")
	b.WriteString("      <link>" + escape(download) + "</link>\n")
	b.WriteString("      <comments>" + escape(details) + "#comments</comments>\n")
	b.WriteString("      <pubDate>" + posted + "</pubDate>\n")
	b.WriteString("      <category>" + escape(categoryLabel(r.Category)) + "</category>\n")
	b.WriteString("      <description>" + escape(r.Title) + "</description>\n")
	b.WriteString(`      <enclosure url="` + escape(download) + `" length="` + size + `" type="` + contentTypeNZB + `"/>` + "\n")

	attr := func(name, value string) {
		b.WriteString(`      <newznab:attr name="` + name + `" value="` + escape(value) + `"/>` + "\n")
	}
	attr("category", strconv.Itoa(r.Category/1000*1000))
	if r.Category%1000 != 0 {
		attr("category", strconv.Itoa(r.Category))
	}
	attr("size", size)
	attr("guid", r.GUID)
	attr("files", strconv.Itoa(r.Files))
	attr("poster", r.Poster)
	attr("group", r.Group)
	attr("grabs", strconv.Itoa(r.Grabs))
	attr("usenetdate", posted)
	attr("password", "0")
	if r.TVDBID > 0 {
		attr("tvdbid", strconv.Itoa(r.TVDBID))
	}
	if r.TvMazeID > 0 {
		attr("tvmazeid", strconv.Itoa(r.TvMazeID))
	}
	// Newznab carries the IMDb id as its number alone, which Sonarr turns
	// back into tt plus seven digits
	if n := imdbNumber(r.IMDBID); n > 0 {
		attr("imdb", strconv.Itoa(n))
	}
	if r.Season > 0 || r.Episode > 0 {
		attr("season", strconv.Itoa(r.Season))
	}
	if r.Episode > 0 {
		attr("episode", strconv.Itoa(r.Episode))
	}
	if len(r.Languages) > 0 {
		attr("language", strings.Join(r.Languages, ", "))
	}
	if r.Scene {
		attr("prematch", "1")
	}
	if r.Nuked {
		attr("nuked", "1")
	}
	b.WriteString("    </item>\n")
}

// downloadURL is where the feed says a release's NZB is: the API's get
// function, with the key, because Sonarr fetches the link as it stands.
func (s *Server) downloadURL(guid string) string {
	q := url.Values{"t": {functionGet}, "id": {guid}}
	if s.apiKey != "" {
		q.Set("apikey", s.apiKey)
	}

	return s.baseURL + "/api?" + q.Encode()
}

// categoryLabel is an item's category element, "TV > HD".
func categoryLabel(category int) string {
	if name, ok := categoryNames[category]; ok {
		return "TV > " + name
	}

	return "TV"
}

// nzbDocument is the NZB a download answers. Sonarr's NzbValidationService
// wants an nzb root with at least one file in its namespace; SABnzbd reads
// the size from the segments, so their bytes add up to the release's size.
func nzbDocument(r *Release) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">` + "\n")
	b.WriteString("  <head>\n")
	b.WriteString(`    <meta type="title">` + escape(r.Title) + "</meta>\n")
	b.WriteString(`    <meta type="category">` + escape(categoryLabel(r.Category)) + "</meta>\n")
	b.WriteString("  </head>\n")

	per := r.Size / int64(r.Files)
	for i := range r.Files {
		part := per
		if i == r.Files-1 {
			part = r.Size - per*int64(r.Files-1)
		}
		name := r.Title + ".mkv"
		if r.Files > 1 {
			name = r.Title + ".part" + strconv.Itoa(i+1) + ".rar"
		}
		subject := `[` + strconv.Itoa(i+1) + "/" + strconv.Itoa(r.Files) + `] - "` + name + `" yEnc (1/1) ` + strconv.FormatInt(part, 10)
		b.WriteString(`  <file poster="` + escape(r.Poster) + `" date="` + strconv.FormatInt(r.PubDate.Unix(), 10) + `" subject="` + escape(subject) + `">` + "\n")
		b.WriteString("    <groups>\n      <group>" + escape(r.Group) + "</group>\n    </groups>\n")
		b.WriteString("    <segments>\n")
		b.WriteString(`      <segment bytes="` + strconv.FormatInt(part, 10) + `" number="1">` + escape(r.GUID+"."+strconv.Itoa(i+1)+"@fake.newznab.invalid") + "</segment>\n")
		b.WriteString("    </segments>\n  </file>\n")
	}
	b.WriteString("</nzb>\n")

	return b.String()
}

// escape makes text safe in XML element content and in a double-quoted
// attribute.
func escape(text string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(text))

	return b.String()
}
