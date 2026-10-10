package tools

import (
	"reflect"
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
var handles = map[string]bool{
	"path": true, "relative_path": true, "output_path": true, "from": true, "to": true,
	"folder": true, "unmapped_folders": true, "root_folder": true,
	"guid": true, "download_id": true, "subject": true,
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
		if s := outside.Text(v.String(), answerLength); v.CanSet() && s != v.String() {
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

func (e cleanedError) Error() string { return outside.Text(e.err.Error(), answerLength) }
func (e cleanedError) Unwrap() error { return e.err }

// cleanFailure is err with its words cleaned, and err itself when there is
// nothing to clean.
func cleanFailure(err error) error {
	if err == nil || outside.Text(err.Error(), answerLength) == err.Error() {
		return err
	}

	return cleanedError{err}
}
