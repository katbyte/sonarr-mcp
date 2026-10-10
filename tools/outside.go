package tools

import (
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/go-kt/outside"
)

// Most of what a tool answers was written by someone else: a release's name
// as a public indexer gave it, a title and a plot from TheTVDB, a message
// from a download client, a line of Sonarr's log quoting any of them. A
// model reads it as it finds it, so every piece of text in an answer, and in
// a refusal, has what does not show taken out of it and is cut to a length
// (go-kt's outside), here, once, around every tool, rather than by each tool
// where it remembers to.

// answerLength is the most of one piece of text an answer carries.
const answerLength = 2000

// handles are the values in an answer that a later call hands back, to a
// tool here or through it to Sonarr, by the names they are answered under: a
// path or a folder, a release's guid, a download's id, and an audit's
// subject, which is a path as often as not. Each has to find the same thing
// again, so it is answered as Sonarr holds it and not cleaned. A series'
// title is handed back too, and is cleaned: titles are compared with what
// does not show taken out (normaliseTitle).
//
// The last three are Sonarr's own names, and no tool's field: a settings
// record is answered as a map by the names Sonarr gives its properties, and
// these are the paths among them, the folder an import list adds series
// under and the two sides of a remote path mapping.
var handles = map[string]bool{
	"path": true, "relative_path": true, "output_path": true, "from": true, "to": true,
	"folder": true, "unmapped_folders": true, "root_folder": true,
	"guid": true, "download_id": true, "subject": true,
	"rootFolderPath": true, "remotePath": true, "localPath": true,
}

// Sonarr quotes an address back with a credential of its own on the end of
// it: an indexer that fails its test is said to have failed "at" the address
// Sonarr asked, the API key it keeps for that indexer and all. That key was
// never the caller's to be handed, and it comes in a test's problems, which
// are an answer, as well as in a refused save. So every address in the text
// of an answer or a refusal is shown without who it signs in as and without
// the value of any parameter, the rule go-kt's registry has for a failure's
// words. The handles are left: a release's guid is its address as often as
// not, and has to find the release again.

// addresses finds the addresses in a piece of text.
var addresses = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// hidden is what stands where an address carried a credential.
const hidden = "REDACTED"

// hideAddresses is text with each address in it as a caller may see one.
func hideAddresses(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}

	return addresses.ReplaceAllStringFunc(s, func(found string) string {
		// what ends the sentence an address is quoted in is not part of it
		address := strings.TrimRight(found, ".,;:!?)]}")

		return hideAddress(address) + found[len(address):]
	})
}

// hideAddress is one address without who it signs in as or the value of any
// parameter. What follows a # is a place on a page as often as not, a health
// check's wiki link, and is kept unless it is written as parameters are.
func hideAddress(address string) string {
	rest, fragment, _ := strings.Cut(address, "#")
	rest, query, hasQuery := strings.Cut(rest, "?")
	scheme, rest, _ := strings.Cut(rest, "://")
	host, path, hasPath := strings.Cut(rest, "/")

	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = hidden + host[at:]
	}
	out := scheme + "://" + host
	if hasPath {
		out += "/" + path
	}

	if hasQuery {
		parts := strings.Split(query, "&")
		for i, part := range parts {
			name, value, named := strings.Cut(part, "=")
			switch {
			case named && value != "":
				parts[i] = name + "=" + hidden
			case !named && part != "":
				parts[i] = hidden
			}
		}
		out += "?" + strings.Join(parts, "&")
	}

	switch {
	case strings.Contains(fragment, "="):
		out += "#" + hidden
	case fragment != "":
		out += "#" + fragment
	}

	return out
}

// cleanText is one piece of an answer's text as a caller is given it.
func cleanText(s string) string {
	return outside.Text(hideAddresses(s), answerLength)
}

// cleanAnswer cleans every piece of text in an answer, in place: out is a
// pointer to it.
func cleanAnswer(out any) {
	cleanValue(reflect.ValueOf(out))
}

// cleanValue cleans the text a value holds, however deep, leaving the
// handles as they are.
func cleanValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if s := cleanText(v.String()); v.CanSet() && s != v.String() {
			v.SetString(s)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			cleanValue(v.Elem())
		}
	case reflect.Interface:
		// what an interface holds cannot be changed where it is, so a copy
		// is cleaned and put in its place
		if v.IsNil() || !v.CanSet() {
			return
		}
		held := reflect.New(v.Elem().Type()).Elem()
		held.Set(v.Elem())
		cleanValue(held)
		v.Set(held)
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if (f.IsExported() || f.Anonymous) && !handles[name] {
				cleanValue(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			cleanValue(v.Index(i))
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return
		}
		// nor can what a map holds, and its keys are ranged over in order
		// only so that a test sees the same thing twice
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys {
			if handles[k.String()] {
				continue
			}
			held := reflect.New(v.Type().Elem()).Elem()
			held.Set(v.MapIndex(k))
			cleanValue(held)
			v.SetMapIndex(k, held)
		}
	default:
		// a number, a flag: nothing to clean
	}
}

// cleanedError is a tool's refusal with its words cleaned: an error quotes
// what it was given and what Sonarr said, and its text is an answer too.
type cleanedError struct{ err error }

func (e cleanedError) Error() string { return cleanText(e.err.Error()) }
func (e cleanedError) Unwrap() error { return e.err }

// cleanFailure is err with its words cleaned, and err itself when there is
// nothing to clean.
func cleanFailure(err error) error {
	if err == nil || cleanText(err.Error()) == err.Error() {
		return err
	}

	return cleanedError{err}
}
