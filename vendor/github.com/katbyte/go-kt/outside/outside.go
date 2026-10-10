// Package outside makes text that came from outside safe to hand on: a
// release's name as a public indexer gave it, a file's name, a title or a
// description fetched from a service anyone can write to.
//
// Such text is written by strangers and read by a model, which takes what it
// reads as it finds it. What cannot be seen is the easiest to hide an
// instruction in, and what runs on for pages the easiest to bury one in, so
// Text takes out the characters that do not show or that change how the rest
// is shown, and cuts what is left to a length. It does not judge what the
// words say: what is left is still a stranger's, and is data, not
// instructions.
//
// Clean what is read, not what is handed back. A value a later call takes as
// an argument - a path a scan listed, a release's id, a folder's name - has
// to go back as it came, or it no longer finds the thing it names: a file
// whose name holds a character that does not show is not found by the name
// without it. Leave such a value as it is, or look the thing up by the
// cleaned form.
package outside

import "unicode"

// cut is what text cut short ends with, inside the limit it was cut to.
const cut = "…"

// Text is s with what does not show taken out, and no longer than limit
// characters, ending in an ellipsis where it was cut; a limit of 0 or less
// is no limit.
//
// Taken out is every character that does not show: the control characters,
// of which a tab, a line break or the like becomes a space, so the words
// either side of one stay apart; the format characters, which are the
// zero-width ones, the joiners, the soft hyphen and the marks, embeddings,
// overrides and isolates of the direction text runs in, which can show a
// line as other than it is; the tag characters and the variation selectors,
// either of which can carry a hidden message after any letter; and the
// fillers that stand where a letter would and show as nothing. Letters,
// marks and spaces of every script are left as they are. An emoji loses the
// selector that asks for its coloured form, and shows as its viewer draws
// it without one.
func Text(s string, limit int) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case breaks(r):
			r = ' '
		case hidden(r):
			continue
		}

		out = append(out, r)
		if limit > 0 && len(out) > limit {
			return string(out[:limit-1]) + cut
		}
	}

	return string(out)
}

// breaks reports whether a character ends a line or stands where a gap
// does, and so is read as a space: a tab, the line breaks among the control
// characters, and the line and paragraph separators.
func breaks(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x85, 0x2028, 0x2029:
		return true
	}

	return false
}

// hidden reports whether a character does not show, or changes how what is
// around it is shown.
func hidden(r rune) bool {
	switch {
	case unicode.Is(unicode.Cc, r):
		// the C0 and C1 control characters and DEL
		return true
	case unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		// the format characters, all but the few that are signs written
		// before a number, which show
		return true
	case unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		// the variation selectors, and what else Unicode says is to show
		// as nothing: the fillers and the like
		return true
	}

	return false
}
