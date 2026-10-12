package client

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseTime reads a JSON date as a server writes it, trying each layout in turn: null and "" are the
// zero time, which is how a server says it has no date, and a date no layout reads is an error naming
// it, so a layout a server adds is found rather than read as nothing. A generated package's Time and
// Day decode with it, each with the layouts its service's config gives.
func ParseTime(data []byte, layouts []string) (time.Time, error) {
	if string(data) == "null" {
		return time.Time{}, nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return time.Time{}, fmt.Errorf("a date that is not a string: %s", data)
	}
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, &UnreadDateError{Text: s, Layouts: layouts}
}

// UnreadDateError is the error for a date no layout reads, carrying the text so a generated type set to keep
// such dates can hold it rather than fail.
type UnreadDateError struct {
	Text    string
	Layouts []string
}

func (e *UnreadDateError) Error() string {
	return fmt.Sprintf("a date no layout reads: %q (layouts %s)", e.Text, strings.Join(e.Layouts, ", "))
}

// ParseUnix reads a JSON date written as seconds or milliseconds since 1970: a number, or a string of
// digits as some servers write it; null and 0 are zero, which is how a server says it has no date.
func ParseUnix(data []byte) (int64, error) {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if text == "null" || text == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("a date that is not a whole number of seconds or milliseconds: %s", data)
	}

	return n, nil
}
