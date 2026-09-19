package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lookupRow is a show SkyHook knows, as series_lookup and series_add name it.
type lookupRow struct {
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	TvdbID   int    `json:"tvdb_id"`
	Status   string `json:"status"`
	Network  string `json:"network,omitempty"`
	Seasons  int    `json:"seasons"`
	Overview string `json:"overview,omitempty" jsonschema:"the first sentences of the plot"`
	InSonarr bool   `json:"in_sonarr"          jsonschema:"already in the library"`
	ID       int    `json:"id,omitempty"       jsonschema:"the Sonarr id, when already in the library"`
}

func lookupSummary(s *sonarr.SeriesResource) lookupRow {
	seasons := 0
	for _, season := range s.Seasons {
		if season.SeasonNumber > 0 {
			seasons++
		}
	}

	return lookupRow{
		Title: s.Title, Year: s.Year, TvdbID: s.TvdbId, Status: string(s.Status), Network: s.Network,
		Seasons: seasons, Overview: shortText(s.Overview, 240), InSonarr: s.Id > 0, ID: s.Id,
	}
}

// shortText trims text to about n characters, at a word.
func shortText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndexByte(s[:n], ' ')
	if cut < n/2 {
		cut = n
	}

	return s[:cut] + "…"
}

// lookup asks Sonarr (and through it SkyHook, TheTVDB's front) for the shows a
// term could mean.
func (r *registry) lookup(ctx context.Context, term string) ([]sonarr.SeriesResource, error) {
	res, err := r.client.GetSeriesLookup(ctx, sonarr.GetSeriesLookupOperationOptions{Term: strings.TrimSpace(term)})
	if err != nil {
		return nil, err
	}

	return res.Model, nil
}

// folderYear reads the "(2002)" a folder or name ends with.
var folderYear = regexp.MustCompile(`^(.*?)\s*[\(\[]((?:19|20)\d\d)[\)\]]\s*$`)

// pickLookup chooses the one show a term means from a lookup's answer: the
// tvdb id asked for, or the one whose title (and year, when the term carries
// one) matches exactly. Anything else is ambiguous and the caller lists the
// candidates.
//
// TheTVDB tells same-named shows apart with a suffix - "The Office" is the
// 2001 show and "The Office (US)" the 2005 one - so a term matches a title
// with or without its suffix, and "The Office" is ambiguous rather than
// quietly the first of them; "The Office (US)" names one.
func pickLookup(term string, found []sonarr.SeriesResource) (*sonarr.SeriesResource, bool) {
	if m := providerRef.FindStringSubmatch(term); len(m) == 3 && strings.EqualFold(m[1], "tvdb") {
		for i := range found {
			if strconv.Itoa(found[i].TvdbId) == m[2] {
				return &found[i], true
			}
		}
		return nil, false
	}
	title, year := term, 0
	if m := folderYear.FindStringSubmatch(term); m != nil {
		title = m[1]
		year, _ = strconv.Atoi(m[2])
	}
	want := normaliseTitle(title)
	var hits []int
	for i := range found {
		name := found[i].Title
		if year != 0 && found[i].Year != year {
			continue
		}
		if normaliseTitle(name) == want || normaliseTitle(titleSuffix.ReplaceAllString(name, "")) == want {
			hits = append(hits, i)
		}
	}
	if len(hits) == 1 {
		return &found[hits[0]], true
	}

	return nil, false
}

// titleSuffix is the "(US)" or "(2005)" TheTVDB adds to tell a title from
// another show's.
var titleSuffix = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// mostUsedProfile is the quality profile most of the library is on, which is
// the right guess for a show added without naming one.
func (r *registry) mostUsedProfile(ctx context.Context) (*sonarr.QualityProfileResource, error) {
	series, err := r.allSeries(ctx)
	if err != nil {
		return nil, err
	}
	res, err := r.client.GetQualityProfile(ctx)
	if err != nil {
		return nil, err
	}
	if len(res.Model) == 0 {
		return nil, errors.New("sonarr has no quality profiles")
	}
	counts := map[int]int{}
	for i := range series {
		counts[series[i].QualityProfileId]++
	}
	best := &res.Model[0]
	for i := range res.Model {
		if counts[res.Model[i].Id] > counts[best.Id] {
			best = &res.Model[i]
		}
	}

	return best, nil
}

// monitorTypes are the add-time monitoring choices, as Sonarr's UI offers them.
var monitorTypes = []sonarr.MonitorTypes{
	sonarr.MonitorTypesAll, sonarr.MonitorTypesFuture, sonarr.MonitorTypesMissing, sonarr.MonitorTypesExisting,
	sonarr.MonitorTypesRecent, sonarr.MonitorTypesPilot, sonarr.MonitorTypesFirstSeason, sonarr.MonitorTypesLastSeason,
	sonarr.MonitorTypesMonitorSpecials, sonarr.MonitorTypesUnmonitorSpecials, sonarr.MonitorTypesNone,
}

func parseMonitor(s string, def sonarr.MonitorTypes) (sonarr.MonitorTypes, error) {
	if s == "" {
		return def, nil
	}
	for _, m := range monitorTypes {
		if strings.EqualFold(string(m), strings.ReplaceAll(s, "_", "")) {
			return m, nil
		}
	}
	names := make([]string, 0, len(monitorTypes))
	for _, m := range monitorTypes {
		names = append(names, string(m))
	}

	return "", fmt.Errorf("monitor must be one of %s, got %q", strings.Join(names, ", "), s)
}

func parseSeriesType(s string, def sonarr.SeriesTypes) (sonarr.SeriesTypes, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, nil
	case "standard", "daily", "anime":
		return sonarr.SeriesTypes(strings.ToLower(strings.TrimSpace(s))), nil
	}

	return "", fmt.Errorf("series_type must be standard, daily or anime, got %q", s)
}

func parseNewItems(s string) (sonarr.NewItemMonitorTypes, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "all":
		return sonarr.NewItemMonitorTypesAll, nil
	case "none":
		return sonarr.NewItemMonitorTypesNone, nil
	}

	return "", fmt.Errorf("monitor_new_items must be all or none, got %q", s)
}

func registerSeriesWriteTools(r *registry) {
	client := r.client

	type lookupIn struct {
		Term  string `json:"term"            jsonschema:"a title to search for, optionally with its year, or tvdb:<id>"`
		Limit int    `json:"limit,omitempty" jsonschema:"candidates to return, default 10"`
	}
	type lookupOut struct {
		Candidates []lookupRow `json:"candidates" jsonschema:"best match first, as TheTVDB ranks them"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_lookup",
		Description: "Search TheTVDB (through Sonarr) for a show to add, by title or tvdb:<id>. Each candidate says whether it is already in the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
		if strings.TrimSpace(in.Term) == "" {
			return nil, lookupOut{}, errors.New("term is required")
		}
		found, err := r.lookup(ctx, in.Term)
		if err != nil {
			return nil, lookupOut{}, err
		}
		out := lookupOut{}
		for i := range found {
			if len(out.Candidates) >= limitOr(in.Limit, 10) {
				break
			}
			out.Candidates = append(out.Candidates, lookupSummary(&found[i]))
		}

		return nil, out, nil
	})

	type addIn struct {
		Series          string   `json:"series"                      jsonschema:"the show: tvdb:<id>, or a title (with the year if titles clash) that the lookup must match exactly"`
		RootFolder      string   `json:"root_folder,omitempty"       jsonschema:"the root folder to put it in; defaults to the only one when there is one"`
		QualityProfile  string   `json:"quality_profile,omitempty"   jsonschema:"the quality profile; defaults to the one most of the library is on"`
		Monitor         string   `json:"monitor,omitempty"           jsonschema:"which episodes to monitor: all (default), future, missing, existing, recent, pilot, firstSeason, lastSeason, monitorSpecials, unmonitorSpecials, none"`
		SeriesType      string   `json:"series_type,omitempty"       jsonschema:"standard, daily or anime; defaults to what TheTVDB says"`
		SeasonFolder    *bool    `json:"season_folder,omitempty"     jsonschema:"put episodes in Season NN folders, default true"`
		MonitorNewItems string   `json:"monitor_new_items,omitempty" jsonschema:"all (default): monitor seasons that appear later; none: leave them unmonitored"`
		Tags            []string `json:"tags,omitempty"              jsonschema:"tag labels, created when Sonarr does not have them"`
		Search          bool     `json:"search,omitempty"            jsonschema:"search for the missing monitored episodes once added"`
	}
	type addOut struct {
		Added    seriesSummary `json:"added"`
		Searched bool          `json:"searched"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_add",
		Description: "Add a show to Sonarr by tvdb:<id> or exact title, into a root folder and quality profile (the only root folder and the library's most used profile unless named), choosing which episodes to monitor, and optionally search for them at once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		found, err := r.lookup(ctx, in.Series)
		if err != nil {
			return nil, addOut{}, err
		}
		show, ok := pickLookup(in.Series, found)
		if !ok {
			return nil, addOut{}, ambiguousLookup(in.Series, found)
		}
		if show.Id > 0 {
			return nil, addOut{}, fmt.Errorf("%s (%d) is already in Sonarr as id %d", show.Title, show.Year, show.Id)
		}
		folder, err := r.resolveRootFolder(ctx, in.RootFolder)
		if err != nil {
			return nil, addOut{}, err
		}
		profile, err := r.addProfile(ctx, in.QualityProfile)
		if err != nil {
			return nil, addOut{}, err
		}
		monitor, err := parseMonitor(in.Monitor, sonarr.MonitorTypesAll)
		if err != nil {
			return nil, addOut{}, err
		}
		seriesType, err := parseSeriesType(in.SeriesType, show.SeriesType)
		if err != nil {
			return nil, addOut{}, err
		}
		newItems, err := parseNewItems(in.MonitorNewItems)
		if err != nil {
			return nil, addOut{}, err
		}
		if newItems == "" {
			newItems = sonarr.NewItemMonitorTypesAll
		}
		tags, err := r.resolveTags(ctx, in.Tags, true)
		if err != nil {
			return nil, addOut{}, err
		}

		body := *show
		body.RootFolderPath = folder.Path
		body.QualityProfileId = profile.Id
		body.SeriesType = seriesType
		body.SeasonFolder = new(in.SeasonFolder == nil || *in.SeasonFolder)
		body.Monitored = new(monitor != sonarr.MonitorTypesNone)
		body.MonitorNewItems = newItems
		body.Tags = tags
		body.AddOptions = &sonarr.AddSeriesOptions{Monitor: monitor, SearchForMissingEpisodes: new(in.Search)}
		res, err := client.PostSeries(ctx, body)
		if err != nil {
			return nil, addOut{}, err
		}
		r.seriesCache().invalidate()
		profiles, tagNames, err := r.lookupNames(ctx)
		if err != nil {
			return nil, addOut{}, err
		}

		return nil, addOut{Added: summariseSeries(res.Model, profiles, tagNames), Searched: in.Search}, nil
	})

	type editIn struct {
		Series          string   `json:"series"                      jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Monitored       *bool    `json:"monitored,omitempty"`
		QualityProfile  string   `json:"quality_profile,omitempty"`
		SeriesType      string   `json:"series_type,omitempty"       jsonschema:"standard, daily or anime"`
		SeasonFolder    *bool    `json:"season_folder,omitempty"`
		MonitorNewItems string   `json:"monitor_new_items,omitempty" jsonschema:"all or none: whether seasons that appear later are monitored"`
		Path            string   `json:"path,omitempty"              jsonschema:"a new folder for the series"`
		MoveFiles       bool     `json:"move_files,omitempty"        jsonschema:"with path: move the files there too; without it only the record changes, for a folder already moved by hand"`
		AddTags         []string `json:"add_tags,omitempty"          jsonschema:"tag labels to add, created when missing"`
		RemoveTags      []string `json:"remove_tags,omitempty"       jsonschema:"tag labels to take off"`
	}
	type editOut struct {
		Series  seriesSummary `json:"series"`
		Changed []string      `json:"changed" jsonschema:"what was changed, from and to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_edit",
		Description: "Change how Sonarr handles a series: monitoring, quality profile, series type, season folders, whether new seasons are monitored, its folder (moving the files or not), and its tags. Only the fields given change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, editOut{}, err
		}
		// the index holds a series without its statistics' latest word, so
		// the record to write back is read fresh
		cur, err := client.GetSeriesById(ctx, s.Id, sonarr.GetSeriesByIdOperationOptions{})
		if err != nil {
			return nil, editOut{}, err
		}
		body := *cur.Model
		profiles, tagNames, err := r.lookupNames(ctx)
		if err != nil {
			return nil, editOut{}, err
		}

		var changed []string
		note := func(field string, from, to any) {
			changed = append(changed, fmt.Sprintf("%s: %v -> %v", field, from, to))
		}
		if in.Monitored != nil && *in.Monitored != boolv(body.Monitored) {
			note("monitored", boolv(body.Monitored), *in.Monitored)
			body.Monitored = new(*in.Monitored)
		}
		if in.QualityProfile != "" {
			var p *sonarr.QualityProfileResource
			if p, err = r.resolveProfile(ctx, in.QualityProfile); err != nil {
				return nil, editOut{}, err
			}
			if p.Id != body.QualityProfileId {
				note("quality_profile", profiles[body.QualityProfileId], p.Name)
				body.QualityProfileId = p.Id
			}
		}
		if in.SeriesType != "" {
			var t sonarr.SeriesTypes
			if t, err = parseSeriesType(in.SeriesType, body.SeriesType); err != nil {
				return nil, editOut{}, err
			}
			if t != body.SeriesType {
				note("series_type", body.SeriesType, t)
				body.SeriesType = t
			}
		}
		if in.SeasonFolder != nil && *in.SeasonFolder != boolv(body.SeasonFolder) {
			note("season_folder", boolv(body.SeasonFolder), *in.SeasonFolder)
			body.SeasonFolder = new(*in.SeasonFolder)
		}
		newItems, err := parseNewItems(in.MonitorNewItems)
		if err != nil {
			return nil, editOut{}, err
		}
		if newItems != "" && newItems != body.MonitorNewItems {
			note("monitor_new_items", body.MonitorNewItems, newItems)
			body.MonitorNewItems = newItems
		}
		if in.Path != "" && strings.TrimRight(in.Path, "/") != strings.TrimRight(body.Path, "/") {
			note("path", body.Path, in.Path)
			body.Path = in.Path
			body.RootFolderPath = path.Dir(strings.TrimRight(in.Path, "/"))
		}
		if len(in.AddTags) > 0 || len(in.RemoveTags) > 0 {
			var addIDs, removeIDs []int
			if addIDs, err = r.resolveTags(ctx, in.AddTags, true); err != nil {
				return nil, editOut{}, err
			}
			if removeIDs, err = r.resolveTags(ctx, in.RemoveTags, false); err != nil {
				return nil, editOut{}, err
			}
			tags := slices.DeleteFunc(slices.Clone(body.Tags), func(id int) bool { return slices.Contains(removeIDs, id) })
			for _, id := range addIDs {
				if !slices.Contains(tags, id) {
					tags = append(tags, id)
				}
			}
			if tagNames, err = r.tagLabels(ctx); err != nil {
				return nil, editOut{}, err
			}
			before, after := labels(body.Tags, tagNames), labels(tags, tagNames)
			if !slices.Equal(before, after) {
				note("tags", before, after)
				body.Tags = tags
			}
		}
		if len(changed) == 0 {
			return nil, editOut{Series: summariseSeries(&body, profiles, tagNames)}, nil
		}

		res, err := client.PutSeriesById(ctx, strconv.Itoa(body.Id), body, sonarr.PutSeriesByIdOperationOptions{MoveFiles: new(in.MoveFiles)})
		if err != nil {
			return nil, editOut{}, err
		}
		if profiles, err = r.profileNames(ctx); err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{Series: summariseSeries(res.Model, profiles, tagNames), Changed: changed}, nil
	})

	type deleteIn struct {
		Series             string `json:"series"                         jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		DeleteFiles        bool   `json:"delete_files,omitempty"         jsonschema:"also delete the series folder and every file in it; without this the files stay on disk"`
		AddImportExclusion bool   `json:"add_import_exclusion,omitempty" jsonschema:"stop import lists adding the show back"`
	}
	type deleteOut struct {
		Deleted      string `json:"deleted"`
		Path         string `json:"path"`
		FilesDeleted bool   `json:"files_deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "series_delete",
		Description: "Remove a series from Sonarr. Its files stay on disk unless delete_files is set, which deletes the series folder and everything in it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if _, err := client.DeleteSeriesById(ctx, s.Id, sonarr.DeleteSeriesByIdOperationOptions{
			DeleteFiles: new(in.DeleteFiles), AddImportListExclusion: new(in.AddImportExclusion),
		}); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: fmt.Sprintf("%s (%d)", s.Title, s.Year), Path: s.Path, FilesDeleted: in.DeleteFiles}, nil
	})

	type commandIn struct {
		Series string `json:"series"                 jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Wait   int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for Sonarr to finish, default 60; -1 queues it and returns at once"`
	}
	type seriesCommandOut struct {
		Command commandOut    `json:"command"`
		Series  seriesSummary `json:"series"  jsonschema:"the series as it stands after the command"`
	}
	seriesCommand := func(ctx context.Context, name string, in commandIn) (seriesCommandOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return seriesCommandOut{}, err
		}
		cmd, err := r.runCommand(ctx, name, map[string]any{"seriesId": s.Id}, waitFor(in.Wait))
		if err != nil {
			return seriesCommandOut{}, err
		}
		after, err := client.GetSeriesById(ctx, s.Id, sonarr.GetSeriesByIdOperationOptions{})
		if err != nil {
			return seriesCommandOut{}, err
		}
		profiles, tags, err := r.lookupNames(ctx)
		if err != nil {
			return seriesCommandOut{}, err
		}

		return seriesCommandOut{Command: cmd, Series: summariseSeries(after.Model, profiles, tags)}, nil
	}

	add(r, writeTool, &mcp.Tool{
		Name:        "series_refresh",
		Description: "Refresh a series from TheTVDB - new episodes, changed titles and air dates - then rescan its folder. Use after a show's listing changes, or when episodes Sonarr should know about are missing from it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, seriesCommandOut, error) {
		out, err := seriesCommand(ctx, "RefreshSeries", in)
		return nil, out, err
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "series_rescan",
		Description: "Rescan a series' folder, so Sonarr picks up files added, removed or replaced on disk outside it. Files it cannot place are left for import_scan to explain.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, seriesCommandOut, error) {
		out, err := seriesCommand(ctx, "RescanSeries", in)
		return nil, out, err
	})

	type searchOut struct {
		Command seriesCommandOut `json:"result"`
		Queued  []queueRow       `json:"queued" jsonschema:"what is in the download queue for the series after the search: what it grabbed, and anything already there"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_search",
		Description: "Search the indexers for every monitored episode of a series that is missing or below its profile's cutoff, grabbing the best releases Sonarr accepts, then report what is in the download queue for it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, searchOut, error) {
		res, err := seriesCommand(ctx, "SeriesSearch", in)
		if err != nil {
			return nil, searchOut{}, err
		}
		queued, err := r.queueFor(ctx, res.Series.ID, nil)
		if err != nil {
			return nil, searchOut{}, err
		}

		return nil, searchOut{Command: res, Queued: queued}, nil
	})

	type renameIn struct {
		Series string `json:"series"                 jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Files  []int  `json:"file_ids,omitempty"     jsonschema:"only these episode files; default every file whose name does not match the naming format"`
		DryRun bool   `json:"dry_run,omitempty"      jsonschema:"report what would be renamed without renaming"`
		Wait   int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the renames, default 60"`
	}
	type renameRow struct {
		FileID   int    `json:"file_id"`
		Episodes string `json:"episodes"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	type renameOut struct {
		Renamed []renameRow `json:"renamed"           jsonschema:"what was (or with dry_run, would be) renamed"`
		Command *commandOut `json:"command,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_rename",
		Description: "Rename a series' files to match Sonarr's naming format, all of them or the ones named, or with dry_run only report the renames. audit_naming finds the series that need it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, renameOut{}, err
		}
		// with renaming off, Sonarr's plan is empty whatever the names are,
		// which would read as "nothing to rename" when it means "will not"
		if on, err := r.renaming(ctx); err != nil {
			return nil, renameOut{}, err
		} else if !on {
			return nil, renameOut{}, errors.New("sonarr's Rename Episodes setting is off, so it renames nothing; turn it on in its media management settings first")
		}
		plan, err := client.GetRename(ctx, sonarr.GetRenameOperationOptions{SeriesId: s.Id})
		if err != nil {
			return nil, renameOut{}, err
		}
		out := renameOut{}
		var ids []int
		for _, p := range plan.Model {
			if len(in.Files) > 0 && !slices.Contains(in.Files, p.EpisodeFileId) {
				continue
			}
			ids = append(ids, p.EpisodeFileId)
			// the plan's paths are relative to the series folder
			out.Renamed = append(out.Renamed, renameRow{
				FileID: p.EpisodeFileId, Episodes: episodeLabel(p.SeasonNumber, p.EpisodeNumbers...),
				From: path.Join(s.Path, p.ExistingPath), To: path.Join(s.Path, p.NewPath),
			})
		}
		for _, id := range in.Files {
			if !slices.Contains(ids, id) {
				return nil, renameOut{}, fmt.Errorf("file %d of %s is already named to the format, or is not one of its files", id, s.Title)
			}
		}
		if in.DryRun || len(ids) == 0 {
			return nil, out, nil
		}
		cmd, err := r.runCommand(ctx, "RenameFiles", map[string]any{"seriesId": s.Id, "files": ids}, waitFor(in.Wait))
		out.Command = &cmd

		return nil, out, err
	})

	type importIn struct {
		RootFolder     string            `json:"root_folder,omitempty"     jsonschema:"the root folder holding the folders; defaults to the only one"`
		Folders        []string          `json:"folders,omitempty"         jsonschema:"the folder names to import, as audit_unmapped_folders lists them; default every folder Sonarr does not know"`
		QualityProfile string            `json:"quality_profile,omitempty" jsonschema:"defaults to the one most of the library is on"`
		Monitor        string            `json:"monitor,omitempty"         jsonschema:"which episodes to monitor: all (default), future, missing, existing, recent, pilot, firstSeason, lastSeason, none"`
		Matches        map[string]string `json:"matches,omitempty"         jsonschema:"for a folder whose name alone does not settle which show it is: folder name to the show, as tvdb:<id> or an exact title with its year"`
		DryRun         bool              `json:"dry_run,omitempty"         jsonschema:"report which show each folder would be imported as, without importing"`
	}
	type importRow struct {
		Folder     string      `json:"folder"`
		Status     string      `json:"status"               jsonschema:"imported, would import, ambiguous, no match, or already in library"`
		Series     *lookupRow  `json:"series,omitempty"     jsonschema:"the show the folder was (or would be) imported as"`
		Candidates []lookupRow `json:"candidates,omitempty" jsonschema:"for ambiguous: the shows the folder name could be; import it again with the one it is in matches"`
	}
	type importOut struct {
		Folders []importRow `json:"folders"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_import",
		Description: "Import folders in a root folder that Sonarr does not know - the folders audit_unmapped_folders lists - as series, matching each folder name (and its year) to a show and adding it with the folder as its path, so the files there are picked up rather than downloaded again. dry_run reports the matches first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in importIn) (*mcp.CallToolResult, importOut, error) {
		folder, err := r.resolveRootFolder(ctx, in.RootFolder)
		if err != nil {
			return nil, importOut{}, err
		}
		monitor, err := parseMonitor(in.Monitor, sonarr.MonitorTypesAll)
		if err != nil {
			return nil, importOut{}, err
		}
		unmapped := map[string]sonarr.UnmappedFolder{}
		for _, u := range folder.UnmappedFolders {
			unmapped[strings.ToLower(u.Name)] = u
		}
		names := in.Folders
		if len(names) == 0 {
			for _, u := range folder.UnmappedFolders {
				names = append(names, u.Name)
			}
		}
		var profile *sonarr.QualityProfileResource
		out := importOut{}
		var toImport []sonarr.SeriesResource
		for _, name := range names {
			u, ok := unmapped[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				out.Folders = append(out.Folders, importRow{Folder: name, Status: "no match", Candidates: nil})
				continue
			}
			term := u.Name
			for folderName, match := range in.Matches {
				if strings.EqualFold(strings.TrimSpace(folderName), u.Name) {
					term = match
				}
			}
			found, err := r.lookup(ctx, term)
			if err != nil {
				return nil, importOut{}, err
			}
			show, ok := pickLookup(term, found)
			if !ok && len(found) > 0 && term == u.Name && folderYear.FindStringSubmatch(u.Name) == nil {
				// a folder without a year names the first show when that one
				// alone has the title
				show, ok = pickLookup(u.Name+" ("+strconv.Itoa(found[0].Year)+")", found[:1])
			}
			row := importRow{Folder: u.Name}
			switch {
			case !ok:
				row.Status = "ambiguous"
				if len(found) == 0 {
					row.Status = "no match"
				}
				for i := range found {
					if i == 5 {
						break
					}
					row.Candidates = append(row.Candidates, lookupSummary(&found[i]))
				}
			case show.Id > 0:
				row.Status = "already in library"
				row.Series = new(lookupSummary(show))
			default:
				row.Status = "would import"
				row.Series = new(lookupSummary(show))
				if profile == nil {
					if profile, err = r.addProfile(ctx, in.QualityProfile); err != nil {
						return nil, importOut{}, err
					}
				}
				body := *show
				body.Path = u.Path
				body.RootFolderPath = folder.Path
				body.QualityProfileId = profile.Id
				body.SeasonFolder = new(true)
				body.Monitored = new(monitor != sonarr.MonitorTypesNone)
				body.MonitorNewItems = sonarr.NewItemMonitorTypesAll
				body.AddOptions = &sonarr.AddSeriesOptions{Monitor: monitor, SearchForMissingEpisodes: new(false)}
				toImport = append(toImport, body)
			}
			out.Folders = append(out.Folders, row)
		}
		if in.DryRun || len(toImport) == 0 {
			return nil, out, nil
		}
		if _, err := client.PostSeriesImport(ctx, toImport); err != nil {
			return nil, out, err
		}
		for i := range out.Folders {
			if out.Folders[i].Status == "would import" {
				out.Folders[i].Status = "imported"
			}
		}

		return nil, out, nil
	})
}

// addProfile is the profile a show is added with: the one named, or the one
// most of the library is on.
func (r *registry) addProfile(ctx context.Context, ref string) (*sonarr.QualityProfileResource, error) {
	if ref != "" {
		return r.resolveProfile(ctx, ref)
	}

	return r.mostUsedProfile(ctx)
}

// ambiguousLookup explains a lookup that did not name one show.
func ambiguousLookup(term string, found []sonarr.SeriesResource) error {
	if len(found) == 0 {
		return fmt.Errorf("TheTVDB knows no show matching %q", term)
	}
	var names []string
	for i := range found {
		if i == 8 {
			break
		}
		name := found[i].Title
		// TheTVDB puts the year in some titles already
		if year := fmt.Sprintf("(%d)", found[i].Year); !strings.HasSuffix(name, year) {
			name += " " + year
		}
		names = append(names, fmt.Sprintf("%s tvdb:%d", name, found[i].TvdbId))
	}

	return fmt.Errorf("%q does not name one show exactly; pick one by tvdb id: %s", term, strings.Join(names, ", "))
}
