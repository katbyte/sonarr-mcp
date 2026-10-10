package newznab

import (
	"crypto/sha1" //nolint:gosec // a torrent's info hash, which is SHA-1 by definition
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The namespaces the *arr parsers read a feed's attr elements from. They are
// names, never fetched, and https would be other names.
const (
	newznabNamespace = "http://www.newznab.com/DTD/2010/feeds/attributes/" //nolint:revive // unsecure-url-scheme: a namespace's name, see above
	torznabNamespace = "http://torznab.com/schemas/2015/feed"              //nolint:revive // unsecure-url-scheme: the same
)

// searchParams are the parameters each kind of search lists in an indexer's
// capabilities, tv-search aside, which is the Site's to say.
var searchParams = map[string]string{
	SearchText:  "q",
	SearchMovie: "q,imdbid,tmdbid",
	SearchMusic: "q,artist,album",
	searchAudio: "q,artist,album",
	SearchBook:  "q,author,title",
}

// searchAudio is music-search's other name: one *arr reads the capabilities
// for one, another for the other, so an indexer that offers music lists both.
const searchAudio = "audio-search"

// capsDocument is the t=caps answer, what NewznabCapabilitiesProvider reads:
// the page size, the kinds of search the indexer offers with their
// parameters, and its categories.
func capsDocument(site *Site) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<caps>\n")
	b.WriteString(`  <server version="1.0" title="` + escape(site.Name) + `"/>` + "\n")
	b.WriteString(`  <limits max="` + strconv.Itoa(site.PageSize) + `" default="` + strconv.Itoa(site.PageSize) + `"/>` + "\n")
	b.WriteString(`  <registration available="no" open="no"/>` + "\n")
	b.WriteString("  <searching>\n")
	for _, kind := range []string{SearchText, SearchTV, SearchMovie, SearchMusic, searchAudio, SearchBook} {
		available, params := "no", searchParams[kind]
		if slices.Contains(site.Searches, kind) || kind == searchAudio && slices.Contains(site.Searches, SearchMusic) {
			available = "yes"
		}
		if kind == SearchTV {
			params = strings.Join(site.TVSearchParams, ",")
		}
		b.WriteString(`    <` + kind + ` available="` + available + `" supportedParams="` + escape(params) + `"/>` + "\n")
	}
	b.WriteString("  </searching>\n  <categories>\n")
	for _, c := range site.Categories {
		b.WriteString(`    <category id="` + strconv.Itoa(c.ID) + `" name="` + escape(c.Name) + `">` + "\n")
		for _, sub := range c.Subs {
			b.WriteString(`      <subcat id="` + strconv.Itoa(sub.ID) + `" name="` + escape(sub.Name) + `"/>` + "\n")
		}
		b.WriteString("    </category>\n")
	}
	b.WriteString("  </categories>\n</caps>\n")

	return b.String()
}

// feedDocument is a search's or an RSS sync's answer: an RSS 2.0 channel of
// items with the attributes the *arr parsers read and the ones a real indexer
// sends besides, in the newznab namespace for usenet and torznab for
// torrents.
func (ix *Indexer) feedDocument(site *Site, page []Release, offset, total int) string {
	ns := "newznab"
	if site.Protocol == Torrent {
		ns = "torznab"
	}
	base := ix.URL()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:newznab="` + newznabNamespace + `" xmlns:torznab="` + torznabNamespace + `">` + "\n")
	b.WriteString("  <channel>\n")
	b.WriteString(`    <atom:link href="` + escape(base) + `/api" rel="self" type="application/rss+xml"/>` + "\n")
	b.WriteString("    <title>" + escape(site.Name) + "</title>\n")
	b.WriteString("    <description>" + escape(site.Name) + ", a " + site.Protocol + " indexer for tests</description>\n")
	b.WriteString("    <link>" + escape(base) + "/</link>\n")
	b.WriteString("    <language>en-gb</language>\n")
	b.WriteString(`    <` + ns + `:response offset="` + strconv.Itoa(offset) + `" total="` + strconv.Itoa(total) + `"/>` + "\n")
	for i := range page {
		writeItem(&b, site, base, ns, &page[i])
	}
	b.WriteString("  </channel>\n</rss>\n")

	return b.String()
}

func writeItem(b *strings.Builder, site *Site, base, ns string, r *Release) {
	details := base + "/details/" + url.PathEscape(r.GUID)
	download := downloadURL(site, base, r.GUID)
	posted := r.PubDate.UTC().Format(time.RFC1123Z)
	size := strconv.FormatInt(r.Size, 10)
	enclosure := contentTypeNZB
	if site.Protocol == Torrent {
		enclosure = contentTypeTorrent
	}

	b.WriteString("    <item>\n")
	b.WriteString("      <title>" + escape(r.Title) + "</title>\n")
	b.WriteString(`      <guid isPermaLink="true">` + escape(details) + "</guid>\n")
	b.WriteString("      <link>" + escape(download) + "</link>\n")
	b.WriteString("      <comments>" + escape(details) + "#comments</comments>\n")
	b.WriteString("      <pubDate>" + posted + "</pubDate>\n")
	b.WriteString("      <category>" + escape(categoryLabel(site, r.Category)) + "</category>\n")
	b.WriteString("      <description>" + escape(r.Title) + "</description>\n")
	b.WriteString(`      <enclosure url="` + escape(download) + `" length="` + size + `" type="` + enclosure + `"/>` + "\n")

	attr := func(name, value string) {
		b.WriteString(`      <` + ns + `:attr name="` + name + `" value="` + escape(value) + `"/>` + "\n")
	}
	number := func(name string, n int) {
		if n > 0 {
			attr(name, strconv.Itoa(n))
		}
	}

	attr("category", strconv.Itoa(parentOf(r.Category)))
	if r.Category != parentOf(r.Category) {
		attr("category", strconv.Itoa(r.Category))
	}
	attr("size", size)
	attr("guid", r.GUID)
	attr("grabs", strconv.Itoa(r.Grabs))
	if site.Protocol == Torrent {
		down := "1"
		if r.Freeleech {
			down = "0"
		}
		attr("seeders", strconv.Itoa(r.Seeders))
		attr("peers", strconv.Itoa(r.Seeders+r.Peers))
		attr("infohash", infoHash(r))
		attr("downloadvolumefactor", down)
		attr("uploadvolumefactor", "1")
	} else {
		attr("files", strconv.Itoa(r.Files))
		attr("poster", r.Poster)
		attr("group", r.Group)
		attr("usenetdate", posted)
		attr("password", "0")
	}

	number("tvdbid", r.TVDBID)
	number("tvmazeid", r.TvMazeID)
	number("tmdbid", r.TMDBID)
	// the IMDb id travels as its number alone, seven digits or more, which
	// an *arr turns back into tt and the number
	if n := imdbNumber(r.IMDBID); n > 0 {
		attr("imdb", fmt.Sprintf("%07d", n))
	}
	if r.Season > 0 || r.Episode > 0 {
		attr("season", strconv.Itoa(r.Season))
	}
	number("episode", r.Episode)
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

// downloadURL is where the feed says a release is, with the indexer's key,
// because an *arr fetches the link as it stands.
func downloadURL(site *Site, base, guid string) string {
	link := base + "/download/" + url.PathEscape(guid)
	if site.APIKey != "" {
		link += "?apikey=" + url.QueryEscape(site.APIKey)
	}

	return link
}

// categoryLabel is an item's category element, "TV > HD", from the
// categories the indexer lists; a category it does not list is its number.
func categoryLabel(site *Site, category int) string {
	for _, c := range site.Categories {
		if c.ID == category {
			return c.Name
		}
		for _, sub := range c.Subs {
			if sub.ID == category {
				return c.Name + " > " + sub.Name
			}
		}
	}

	return strconv.Itoa(category)
}

// nzbDocument is the NZB a fetch from a usenet indexer answers. An *arr's
// NzbValidationService wants an nzb root with at least one file in its
// namespace; a download client reads the size from the segments, so their
// bytes add up to the release's size.
func nzbDocument(r *Release) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">` + "\n")
	b.WriteString("  <head>\n    <meta type=\"title\">" + escape(r.Title) + "</meta>\n  </head>\n")

	files := max(r.Files, 1)
	per := r.Size / int64(files)
	for i := range files {
		part, name := per, r.Title+".mkv"
		if i == files-1 {
			part = r.Size - per*int64(files-1)
		}
		if files > 1 {
			name = r.Title + ".part" + strconv.Itoa(i+1) + ".rar"
		}
		subject := "[" + strconv.Itoa(i+1) + "/" + strconv.Itoa(files) + `] - "` + name + `" yEnc (1/1) ` + strconv.FormatInt(part, 10)
		b.WriteString(`  <file poster="` + escape(r.Poster) + `" date="` + strconv.FormatInt(r.PubDate.Unix(), 10) + `" subject="` + escape(subject) + `">` + "\n")
		b.WriteString("    <groups>\n      <group>" + escape(r.Group) + "</group>\n    </groups>\n")
		b.WriteString("    <segments>\n")
		b.WriteString(`      <segment bytes="` + strconv.FormatInt(part, 10) + `" number="1">` + escape(r.GUID+"."+strconv.Itoa(i+1)+"@newznab.invalid") + "</segment>\n")
		b.WriteString("    </segments>\n  </file>\n")
	}
	b.WriteString("</nzb>\n")

	return b.String()
}

// torrentPieceLength is the piece size of the torrents served.
const torrentPieceLength = 1 << 22

// torrentInfo is a release's torrent info dictionary, bencoded: one file of
// its size, and a hash for every piece that size takes (zeroes: nothing is
// ever downloaded), which a torrent parser checks the count of.
func torrentInfo(r *Release) string {
	name := r.Title + ".mkv"
	pieces := int((r.Size + torrentPieceLength - 1) / torrentPieceLength)
	hashes := strings.Repeat("\x00", 20*pieces)

	return fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi%de6:pieces%d:%s7:privatei1ee", r.Size, len(name), name, torrentPieceLength, len(hashes), hashes)
}

// torrentFile is the .torrent a fetch from a torrent indexer answers: a
// tracker that is never reached, and the info dictionary.
func torrentFile(r *Release) string {
	announce := "http://tracker.invalid/announce" //nolint:revive // unsecure-url-scheme: a tracker that is never reached, in a file a client must parse

	return fmt.Sprintf("d8:announce%d:%s4:info%se", len(announce), announce, torrentInfo(r))
}

// infoHash is the SHA-1 of a release's info dictionary, as a torrent client
// works it out.
func infoHash(r *Release) string {
	sum := sha1.Sum([]byte(torrentInfo(r))) //nolint:gosec // the info hash is SHA-1 by definition

	return hex.EncodeToString(sum[:])
}

// escape makes text safe in XML element content and in a double-quoted
// attribute.
func escape(text string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(text))

	return b.String()
}
