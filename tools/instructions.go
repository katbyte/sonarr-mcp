package tools

import "strings"

// Instructions is what this server tells a client that connects, after what
// go-kt says for every server: the rules that hold between its tools, which
// no one tool's description is the place for. Each is said only when a tool
// it is about is registered, so a session with the read tools alone is told
// nothing about deleting.
func Instructions(opts Options) ([]string, error) {
	described, err := Describe(opts)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	lists, deletes := false, false
	for _, t := range described {
		has[t.Name] = true
		lists = lists || strings.HasSuffix(t.Name, "_list")
		deletes = deletes || t.Kind == deleteTool.String()
	}

	out := []string{
		"Name a series by its title, its Sonarr id or tvdb:<id>. A title that fits more than one series is refused, with the series it could be.",
		"A path, a folder, a guid or a download id in an answer is as Sonarr holds it: pass it back unchanged. All other text has had what does not show taken out and is cut at 2000 characters.",
	}
	if lists {
		out = append(out, "Every tool named _list takes limit (100 unless given, 1000 at most) and offset, and answers total, offset and, while there is another page, next_offset: pass that back as offset.")
	}
	if deletes {
		out = append(out, "A tool that deletes a series or files changes nothing without confirm: true. Called without it, it answers would_delete: what a confirmed call would remove.")
	}
	if has["audit_all"] {
		out = append(out, "To find what a library needs, start with audit_all: it counts what each audit finds, and each audit's findings name the tool that fixes them. An audit listed under failed could not run: its count is unknown, not zero.")
	}
	if has["command_list"] {
		out = append(out, "A search, refresh or rename that outlasts its wait answers a command_id with a status of queued or started: follow it with command_list and that command_id.")
	}

	return out, nil
}
