package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// Shared projections and lookups: the trimmed shapes every tool answers in,
// and the name resolution for the things Sonarr only knows by id - quality
// profiles, tags, root folders.

// boolv reads an optional flag, false when unset.
func boolv(b *bool) bool { return b != nil && *b }

// episodeLabel is how an episode is named for a person: S01E02, or S01E02-E03
// for a file holding several.
func episodeLabel(season int, episodes ...int) string {
	if len(episodes) == 0 {
		return fmt.Sprintf("S%02d", season)
	}
	eps := slices.Clone(episodes)
	slices.Sort(eps)
	label := fmt.Sprintf("S%02dE%02d", season, eps[0])
	if len(eps) > 1 && eps[len(eps)-1] != eps[0] {
		label += fmt.Sprintf("-E%02d", eps[len(eps)-1])
	}

	return label
}

// qualityName is a file or release's quality as Sonarr names it, with a
// proper or repack marked.
func qualityName(q *sonarr.QualityModel) string {
	if q == nil || q.Quality == nil {
		return ""
	}
	name := q.Quality.Name
	if q.Revision != nil {
		switch {
		case boolv(q.Revision.IsRepack):
			name += " (repack)"
		case q.Revision.Version > 1:
			name += fmt.Sprintf(" (proper v%d)", q.Revision.Version)
		}
	}

	return name
}

// languageNames lists languages by name.
func languageNames(langs []sonarr.Language) []string {
	out := make([]string, 0, len(langs))
	for _, l := range langs {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}

	return out
}

// humanSize renders a byte count the way a person reads disk space.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// day trims a timestamp to its date, "" for none.
func day(stamp string) string {
	if len(stamp) >= 10 {
		return stamp[:10]
	}

	return stamp
}

// parseTime reads one of Sonarr's timestamps; the zero time for none.
func parseTime(stamp string) time.Time {
	if stamp == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, stamp); err == nil {
			return t
		}
	}

	return time.Time{}
}

// aired reports whether an episode has aired: it has an air date, and the
// date is in the past.
func aired(e *sonarr.EpisodeResource, now time.Time) bool {
	t := parseTime(e.AirDateUtc)
	if t.IsZero() {
		t = parseTime(e.AirDate)
	}

	return !t.IsZero() && t.Before(now)
}

// runtimeMinutes reads a media info runtime ("00:44:12", "44:12" or
// "1:02:03.456") as whole minutes, and whether it could.
func runtimeMinutes(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	var secs float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + v
	}

	return secs / 60, true
}

// profileNames maps quality profile ids to names.
func (r *registry) profileNames(ctx context.Context) (map[int]string, error) {
	res, err := r.client.GetQualityProfile(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(res.Model))
	for _, p := range res.Model {
		out[p.Id] = p.Name
	}

	return out, nil
}

// resolveProfile finds a quality profile by name (any case) or id, listing
// the profiles when none matches.
func (r *registry) resolveProfile(ctx context.Context, ref string) (*sonarr.QualityProfileResource, error) {
	res, err := r.client.GetQualityProfile(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.Model))
	for i := range res.Model {
		p := &res.Model[i]
		if strings.EqualFold(p.Name, strings.TrimSpace(ref)) || strconv.Itoa(p.Id) == strings.TrimSpace(ref) {
			return p, nil
		}
		names = append(names, p.Name)
	}

	return nil, fmt.Errorf("no quality profile %q (have: %s)", ref, strings.Join(names, ", "))
}

// tagLabels maps tag ids to labels.
func (r *registry) tagLabels(ctx context.Context) (map[int]string, error) {
	res, err := r.client.GetTag(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(res.Model))
	for _, t := range res.Model {
		out[t.Id] = t.Label
	}

	return out, nil
}

// labels names a list of tag ids, sorted.
func labels(ids []int, byID map[int]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l, ok := byID[id]; ok {
			out = append(out, l)
		} else {
			out = append(out, strconv.Itoa(id))
		}
	}
	slices.Sort(out)

	return out
}

// resolveTags turns tag labels into ids. A label Sonarr does not have is
// created when create is set, and an error naming the existing labels
// otherwise. Sonarr stores labels lower case, so matching ignores case.
func (r *registry) resolveTags(ctx context.Context, refs []string, create bool) ([]int, error) {
	res, err := r.client.GetTag(ctx)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		id := 0
		for _, t := range res.Model {
			if strings.EqualFold(t.Label, ref) || strconv.Itoa(t.Id) == ref {
				id = t.Id
				break
			}
		}
		if id == 0 {
			if !create {
				have := make([]string, 0, len(res.Model))
				for _, t := range res.Model {
					have = append(have, t.Label)
				}
				return nil, fmt.Errorf("no tag %q (have: %s)", ref, strings.Join(have, ", "))
			}
			made, err := r.client.PostTag(ctx, sonarr.TagResource{Label: strings.ToLower(ref)})
			if err != nil {
				return nil, fmt.Errorf("creating tag %q: %w", ref, err)
			}
			id = made.Model.Id
			res.Model = append(res.Model, *made.Model)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}

	return out, nil
}

// resolveRootFolder finds a root folder by its path, or the only one when
// ref is empty and there is exactly one.
func (r *registry) resolveRootFolder(ctx context.Context, ref string) (*sonarr.RootFolderResource, error) {
	res, err := r.client.GetRootFolder(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(res.Model))
	for i := range res.Model {
		f := &res.Model[i]
		if ref == "" && len(res.Model) == 1 {
			return f, nil
		}
		if strings.TrimRight(f.Path, "/\\") == strings.TrimRight(strings.TrimSpace(ref), "/\\") || strconv.Itoa(f.Id) == ref {
			return f, nil
		}
		paths = append(paths, f.Path)
	}
	if len(res.Model) == 0 {
		return nil, errors.New("sonarr has no root folders; add one first")
	}
	if ref == "" {
		return nil, fmt.Errorf("sonarr has %d root folders, so name one: %s", len(res.Model), strings.Join(paths, ", "))
	}

	return nil, fmt.Errorf("no root folder %q (have: %s)", ref, strings.Join(paths, ", "))
}

// Commands -----------------------------------------------------------------

// commandWait is how long a tool that runs a command waits for it by
// default: long enough for a refresh or a search against a few indexers, short
// enough that a slow one is reported as still running rather than hanging the
// session.
const commandWait = 60 * time.Second

// commandPoll is how often a running command is looked at.
var commandPoll = 500 * time.Millisecond

// commandOut is what a tool that ran a command reports about it.
type commandOut struct {
	ID       int    `json:"command_id"`
	Name     string `json:"command"`
	Status   string `json:"status"             jsonschema:"queued, started, completed, failed, aborted or cancelled; queued or started means it was still running when the wait ended"`
	Message  string `json:"message,omitempty"  jsonschema:"what Sonarr said about the run, e.g. how many releases a search grabbed"`
	Started  string `json:"started,omitempty"`
	Ended    string `json:"ended,omitempty"`
	Duration string `json:"duration,omitempty"`
}

func projectCommand(c *sonarr.CommandResource) commandOut {
	name := c.CommandName
	if name == "" {
		name = c.Name
	}

	return commandOut{
		ID: c.Id, Name: name, Status: string(c.Status), Message: c.Message,
		Started: c.Started, Ended: c.Ended, Duration: c.Duration,
	}
}

// runCommand queues a command with its own fields, and waits up to wait for
// it to finish (0 does not wait). A command that fails is an error; one still
// running when the wait ends is not, and is reported as it stands.
func (r *registry) runCommand(ctx context.Context, name string, fields map[string]any, wait time.Duration) (commandOut, error) {
	body := map[string]any{"name": name}
	maps.Copy(body, fields)
	raw, err := json.Marshal(body)
	if err != nil {
		return commandOut{}, err
	}
	res, err := r.client.PostCommand(ctx, raw)
	if err != nil {
		return commandOut{}, fmt.Errorf("queueing %s: %w", name, err)
	}
	cmd := res.Model
	if wait <= 0 {
		return projectCommand(cmd), nil
	}

	deadline := time.Now().Add(wait)
	for !commandDone(cmd.Status) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return projectCommand(cmd), ctx.Err()
		case <-time.After(commandPoll):
		}
		got, err := r.client.GetCommandById(ctx, cmd.Id)
		if err != nil {
			return projectCommand(cmd), fmt.Errorf("checking %s: %w", name, err)
		}
		cmd = got.Model
	}
	out := projectCommand(cmd)
	switch cmd.Status {
	case sonarr.CommandStatusFailed, sonarr.CommandStatusAborted, sonarr.CommandStatusCancelled, sonarr.CommandStatusOrphaned:
		msg := cmd.Message
		if cmd.Exception != "" {
			msg = strings.TrimSpace(msg + " " + firstLine(cmd.Exception))
		}
		return out, fmt.Errorf("%s %s: %s", name, cmd.Status, msg)
	default:
		return out, nil
	}
}

// commandDone reports whether a command has stopped, one way or another.
func commandDone(s sonarr.CommandStatus) bool {
	switch s {
	case sonarr.CommandStatusQueued, sonarr.CommandStatusStarted, "":
		return false
	default:
		return true
	}
}

// waitFor is the wait a tool's input asks for: the default when unset, none
// when negative.
func waitFor(seconds int) time.Duration {
	switch {
	case seconds < 0:
		return 0
	case seconds == 0:
		return commandWait
	default:
		return time.Duration(seconds) * time.Second
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return strings.TrimSpace(line)
}

// limitOr is a limit input with its default.
func limitOr(limit, def int) int {
	if limit <= 0 {
		return def
	}

	return limit
}
