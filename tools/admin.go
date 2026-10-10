package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/go-kt/lock"
	apiclient "github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools for running Sonarr itself rather than its library: quality
// profiles and the sizes behind them, what import lists may not add, one
// change across many series, backups, updates, restarting, disk space, the
// log files, and the folders it can see.

// qualityNames are the qualities and groups a profile's items name, best
// first.
func qualityNames(items []sonarr.QualityProfileQualityItemResource) []string {
	var out []string
	for _, it := range slices.Backward(items) {
		switch {
		case len(it.Items) > 0:
			out = append(out, it.Name)
			out = append(out, qualityNames(it.Items)...)
		case it.Quality != nil:
			out = append(out, it.Quality.Name)
		}
	}

	return out
}

// itemNamed reports whether a profile's item is, or holds, one of the names.
func itemNamed(it *sonarr.QualityProfileQualityItemResource, names []string) bool {
	has := func(name string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(strings.TrimSpace(n), name) })
	}
	if len(it.Items) == 0 {
		return it.Quality != nil && has(it.Quality.Name)
	}

	return has(it.Name) || slices.ContainsFunc(it.Items, func(in sonarr.QualityProfileQualityItemResource) bool {
		return in.Quality != nil && has(in.Quality.Name)
	})
}

// allow sets whether a profile's item, and what it holds, is allowed.
func allow(it *sonarr.QualityProfileQualityItemResource, allowed bool) {
	it.Allowed = new(allowed)
	for i := range it.Items {
		it.Items[i].Allowed = new(allowed)
	}
}

// profileChange is what profile_create and profile_edit take: the settings a
// quality profile has, by the names profile_list answers them under.
type profileChange struct {
	Name              string         `json:"name,omitempty"                       jsonschema:"what to call it"`
	UpgradeAllowed    *bool          `json:"upgrade_allowed,omitempty"            jsonschema:"whether Sonarr replaces a file with a better one"`
	Cutoff            string         `json:"cutoff,omitempty"                     jsonschema:"the quality (or group of qualities) Sonarr stops upgrading at, by name; it has to be one the profile allows, and a quality a profile groups with others is cut off at as its group"`
	Allowed           []string       `json:"allowed_qualities,omitempty"          jsonschema:"the qualities the profile grabs, by name, replacing the ones it allows now; qualities a profile groups (WEBDL-1080p with WEBRip-1080p as WEB 1080p) are allowed together"`
	Allow             []string       `json:"allow,omitempty"                      jsonschema:"qualities to allow as well, by name"`
	Disallow          []string       `json:"disallow,omitempty"                   jsonschema:"qualities to stop allowing, by name"`
	MinFormatScore    *int           `json:"min_custom_format_score,omitempty"    jsonschema:"a release scoring below this is not grabbed"`
	CutoffFormatScore *int           `json:"cutoff_custom_format_score,omitempty" jsonschema:"Sonarr stops upgrading for score once a file reaches this"`
	FormatScores      map[string]int `json:"custom_format_scores,omitempty"       jsonschema:"custom format name to the score this profile gives it; 0 stops scoring it"`
}

// apply changes a profile as asked, refusing a quality or a format it does
// not have with the ones it does.
func (c *profileChange) apply(p *sonarr.QualityProfileResource) error {
	known := qualityNames(p.Items)
	for _, name := range slices.Concat(c.Allowed, c.Allow, c.Disallow) {
		if !slices.ContainsFunc(known, func(k string) bool { return strings.EqualFold(k, strings.TrimSpace(name)) }) {
			return fmt.Errorf("no quality %q (have: %s)", name, strings.Join(known, ", "))
		}
	}

	if c.Name != "" {
		p.Name = strings.TrimSpace(c.Name)
	}
	if c.UpgradeAllowed != nil {
		p.UpgradeAllowed = new(*c.UpgradeAllowed)
	}
	for i := range p.Items {
		it := &p.Items[i]
		switch {
		case len(c.Allowed) > 0:
			allow(it, itemNamed(it, c.Allowed))
		case itemNamed(it, c.Allow):
			allow(it, true)
		}
		if itemNamed(it, c.Disallow) {
			allow(it, false)
		}
	}
	if c.Cutoff != "" {
		i := slices.IndexFunc(p.Items, func(it sonarr.QualityProfileQualityItemResource) bool { return itemNamed(&it, []string{c.Cutoff}) })
		if i < 0 {
			return fmt.Errorf("no quality %q to stop upgrading at (have: %s)", c.Cutoff, strings.Join(known, ", "))
		}
		// a quality in a group is cut off at by the group's id
		p.Cutoff = p.Items[i].Id
		if p.Items[i].Quality != nil {
			p.Cutoff = p.Items[i].Quality.Id
		}
	}
	if c.MinFormatScore != nil {
		p.MinFormatScore = *c.MinFormatScore
	}
	if c.CutoffFormatScore != nil {
		p.CutoffFormatScore = *c.CutoffFormatScore
	}
	for name, score := range c.FormatScores {
		i := slices.IndexFunc(p.FormatItems, func(f sonarr.ProfileFormatItemResource) bool {
			return strings.EqualFold(f.Name, strings.TrimSpace(name))
		})
		if i < 0 {
			have := make([]string, 0, len(p.FormatItems))
			for _, f := range p.FormatItems {
				have = append(have, f.Name)
			}
			return fmt.Errorf("no custom format %q (have: %s)", name, strings.Join(have, ", "))
		}
		p.FormatItems[i].Score = score
	}

	return nil
}

// profileChanges says what differs between a profile and what it was.
func profileChanges(was, now profileRow) []string {
	var out []string
	note := func(what string, from, to any) {
		if fmt.Sprint(from) != fmt.Sprint(to) {
			out = append(out, fmt.Sprintf("%s: %v -> %v", what, from, to))
		}
	}
	note("name", was.Name, now.Name)
	note("upgrade_allowed", was.UpgradeAllowed, now.UpgradeAllowed)
	note("cutoff", was.Cutoff, now.Cutoff)
	note("allowed_qualities", was.Allowed, now.Allowed)
	note("min_custom_format_score", was.MinFormatScore, now.MinFormatScore)
	note("cutoff_custom_format_score", was.CutoffFormatScore, now.CutoffFormatScore)
	note("custom_format_scores", was.FormatScores, now.FormatScores)

	return out
}

func registerProfileTools(r *registry) {
	client := r.client

	type profileOut struct {
		Profile profileRow `json:"profile"           jsonschema:"the profile as Sonarr now holds it"`
		Changed []string   `json:"changed,omitempty" jsonschema:"what changed, from and to"`
	}

	type createIn struct {
		CopyOf string `json:"copy_of,omitempty" jsonschema:"the profile to start from, by name or id; without it, Sonarr's blank profile with nothing allowed"`
		profileChange
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "profile_create",
		Description: "Create a quality profile: a name, and either a copy of another profile to start from or the qualities it allows, where it stops upgrading (cutoff), and the scores it gives custom formats. A series is put on it by editing the series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, profileOut, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, profileOut{}, errors.New("give the profile a name")
		}
		var body sonarr.QualityProfileResource
		if in.CopyOf != "" {
			from, err := r.resolveProfile(ctx, in.CopyOf)
			if err != nil {
				return nil, profileOut{}, err
			}
			body = *from
		} else {
			blank, err := client.GetQualityProfileSchema(ctx)
			if err != nil {
				return nil, profileOut{}, err
			}
			body = *blank.Model
		}
		body.Id = 0
		if err := in.apply(&body); err != nil {
			return nil, profileOut{}, err
		}
		res, err := client.PostQualityProfile(ctx, body)
		if err != nil {
			return nil, profileOut{}, err
		}

		return nil, profileOut{Profile: projectProfile(res.Model, 0)}, nil
	})

	type editIn struct {
		Profile string `json:"profile" jsonschema:"the profile to change, by name or id"`
		profileChange
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "profile_edit",
		Description: "Change a quality profile: its name, whether it upgrades, the quality it stops upgrading at (cutoff), the qualities it allows (all of them with allowed_qualities, or some with allow and disallow), its minimum and cutoff custom format scores, and the score it gives each custom format. Only what is given changes. Every series on the profile is searched and upgraded by the new rules from then on.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, profileOut, error) {
		cur, err := r.resolveProfile(ctx, in.Profile)
		if err != nil {
			return nil, profileOut{}, err
		}
		was := projectProfile(cur, 0)
		body := *cur
		if err := in.apply(&body); err != nil {
			return nil, profileOut{}, err
		}
		changed := profileChanges(was, projectProfile(&body, 0))
		if len(changed) == 0 {
			return nil, profileOut{Profile: was}, nil
		}
		res, err := client.PutQualityProfileById(ctx, strconv.Itoa(body.Id), body)
		if err != nil {
			return nil, profileOut{}, err
		}

		return nil, profileOut{Profile: projectProfile(res.Model, 0), Changed: changed}, nil
	})

	type deleteIn struct {
		Profile string `json:"profile"           jsonschema:"the profile to delete, by name or id"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to delete; without it nothing changes and the answer says what a confirmed call would delete"`
	}
	type deleteOut struct {
		Deleted     string `json:"deleted,omitempty"      jsonschema:"the profile deleted"`
		WouldDelete string `json:"would_delete,omitempty" jsonschema:"without confirm: the profile a confirmed call deletes; nothing has changed"`
		Loses       string `json:"loses"                  jsonschema:"what goes with it"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "profile_delete",
		Description: "Delete a quality profile no series is on - the unused profiles the profiles audit finds. Sonarr refuses to delete one still in use; put its series on another profile first. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		cur, err := r.resolveProfile(ctx, in.Profile)
		if err != nil {
			return nil, deleteOut{}, err
		}
		out := deleteOut{Loses: "the qualities it allowed, its cutoff and the scores it gave custom formats"}
		if !in.Confirm {
			out.WouldDelete = cur.Name

			return nil, out, nil
		}
		if _, err := client.DeleteQualityProfileById(ctx, cur.Id); err != nil {
			return nil, deleteOut{}, err
		}
		out.Deleted = cur.Name

		return nil, out, nil
	})

	type definitionRow struct {
		Quality   string  `json:"quality"        jsonschema:"the quality, by Sonarr's name for it"`
		Title     string  `json:"title"          jsonschema:"the name it is given in file names"`
		Min       float64 `json:"min_size"       jsonschema:"megabytes per minute of running time a release has to be at least"`
		Preferred float64 `json:"preferred_size" jsonschema:"megabytes per minute Sonarr prefers; 0 is no preference"`
		Max       float64 `json:"max_size"       jsonschema:"megabytes per minute a release may be at most; 0 is no limit"`
	}
	projectDefinition := func(d *sonarr.QualityDefinitionResource) definitionRow {
		row := definitionRow{Title: d.Title, Min: d.MinSize, Preferred: d.PreferredSize, Max: d.MaxSize}
		if d.Quality != nil {
			row.Quality = d.Quality.Name
		}

		return row
	}
	type definitionsOut struct {
		pageOut
		Definitions []definitionRow `json:"definitions" jsonschema:"best quality first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "qualitydefinition_list",
		Description: "The quality definitions: for each quality (HDTV-720p, WEBDL-1080p, Bluray-2160p and the rest) the name it is given in file names and the size a release of it may be, in megabytes per minute of running time. A release rejected as too big or too small was measured against these.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, definitionsOut, error) {
		res, err := client.GetQualityDefinition(ctx)
		if err != nil {
			return nil, definitionsOut{}, err
		}
		slices.SortStableFunc(res.Model, func(a, b sonarr.QualityDefinitionResource) int { return b.Weight - a.Weight })
		definitions, paged := pageRows(res.Model, in)
		out := definitionsOut{pageOut: paged, Definitions: make([]definitionRow, 0, len(definitions))}
		for i := range definitions {
			out.Definitions = append(out.Definitions, projectDefinition(&definitions[i]))
		}

		return nil, out, nil
	})

	type definitionIn struct {
		Quality   string   `json:"quality"                  jsonschema:"the quality to change, by name, e.g. WEBDL-1080p"`
		Title     string   `json:"title,omitempty"          jsonschema:"the name it is given in file names"`
		Min       *float64 `json:"min_size,omitempty"       jsonschema:"megabytes per minute a release has to be at least"`
		Preferred *float64 `json:"preferred_size,omitempty" jsonschema:"megabytes per minute Sonarr prefers"`
		Max       *float64 `json:"max_size,omitempty"       jsonschema:"megabytes per minute a release may be at most; 0 for no limit"`
	}
	type definitionOut struct {
		Definition definitionRow `json:"definition"`
		Changed    []string      `json:"changed,omitempty" jsonschema:"what changed, from and to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "qualitydefinition_edit",
		Description: "Change a quality's definition: the name it is given in file names, and the smallest, preferred and largest size a release of it may be, in megabytes per minute of running time. Releases outside the sizes are rejected from then on, for every series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in definitionIn) (*mcp.CallToolResult, definitionOut, error) {
		res, err := client.GetQualityDefinition(ctx)
		if err != nil {
			return nil, definitionOut{}, err
		}
		i := slices.IndexFunc(res.Model, func(d sonarr.QualityDefinitionResource) bool {
			return d.Quality != nil && strings.EqualFold(d.Quality.Name, strings.TrimSpace(in.Quality))
		})
		if i < 0 {
			have := make([]string, 0, len(res.Model))
			for _, d := range res.Model {
				if d.Quality != nil {
					have = append(have, d.Quality.Name)
				}
			}
			return nil, definitionOut{}, fmt.Errorf("no quality %q (have: %s)", in.Quality, strings.Join(have, ", "))
		}
		body := res.Model[i]
		was := projectDefinition(&body)
		if in.Title != "" {
			body.Title = in.Title
		}
		if in.Min != nil {
			body.MinSize = *in.Min
		}
		if in.Preferred != nil {
			body.PreferredSize = *in.Preferred
		}
		if in.Max != nil {
			body.MaxSize = *in.Max
		}
		now := projectDefinition(&body)
		if now == was {
			return nil, definitionOut{Definition: was}, nil
		}
		saved, err := client.PutQualityDefinitionById(ctx, strconv.Itoa(body.Id), body)
		if err != nil {
			return nil, definitionOut{}, err
		}
		out := definitionOut{Definition: projectDefinition(saved.Model)}
		for _, c := range []struct {
			what     string
			from, to any
		}{{"title", was.Title, now.Title}, {"min_size", was.Min, now.Min}, {"preferred_size", was.Preferred, now.Preferred}, {"max_size", was.Max, now.Max}} {
			if c.from != c.to {
				out.Changed = append(out.Changed, fmt.Sprintf("%s: %v -> %v", c.what, c.from, c.to))
			}
		}

		return nil, out, nil
	})

	type languageRow struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type languagesOut struct {
		pageOut
		Languages []languageRow `json:"languages"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "language_list",
		Description: "The languages Sonarr knows, by the names a file's recorded languages and a custom format's language condition take.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, languagesOut, error) {
		res, err := client.GetLanguage(ctx)
		if err != nil {
			return nil, languagesOut{}, err
		}
		languages, paged := pageRows(res.Model, in)
		out := languagesOut{pageOut: paged, Languages: make([]languageRow, 0, len(languages))}
		for _, l := range languages {
			out.Languages = append(out.Languages, languageRow{ID: l.Id, Name: l.Name})
		}

		return nil, out, nil
	})
}

func registerExclusionTools(r *registry) {
	client := r.client

	type exclusionRow struct {
		ID     int    `json:"id"`
		Title  string `json:"title"`
		TvdbID int    `json:"tvdb_id"`
	}
	type listOut struct {
		pageOut
		Exclusions []exclusionRow `json:"exclusions"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "exclusion_list",
		Description: "The shows import lists are told never to add: the ones deleted with add_import_exclusion, or excluded by hand. A show an import list keeps not adding is usually here.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, listOut, error) {
		res, err := client.GetImportListExclusionPagedComplete(ctx, sonarr.GetImportListExclusionPagedOperationOptions{})
		if err != nil {
			return nil, listOut{}, err
		}
		slices.SortStableFunc(res.Items, func(a, b sonarr.ImportListExclusionResource) int {
			return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		})
		exclusions, paged := pageRows(res.Items, in)
		out := listOut{pageOut: paged, Exclusions: make([]exclusionRow, 0, len(exclusions))}
		for _, e := range exclusions {
			out.Exclusions = append(out.Exclusions, exclusionRow{ID: e.Id, Title: e.Title, TvdbID: e.TvdbId})
		}

		return nil, out, nil
	})

	type addIn struct {
		Series string `json:"series" jsonschema:"the show to keep import lists from adding: tvdb:<id>, or a title (with the year if titles clash) that the lookup must match exactly"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "exclusion_add",
		Description: "Keep import lists from adding a show, by tvdb:<id> or exact title. A show already in the library stays; this only stops a list adding it again once it is gone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, exclusionRow, error) {
		found, err := r.lookup(ctx, in.Series)
		if err != nil {
			return nil, exclusionRow{}, err
		}
		show, ok := pickLookup(in.Series, found)
		if !ok {
			return nil, exclusionRow{}, ambiguousLookup(in.Series, found)
		}
		res, err := client.PostImportListExclusion(ctx, sonarr.ImportListExclusionResource{TvdbId: show.TvdbId, Title: show.Title})
		if err != nil {
			return nil, exclusionRow{}, err
		}

		return nil, exclusionRow{ID: res.Model.Id, Title: res.Model.Title, TvdbID: res.Model.TvdbId}, nil
	})

	type removeIn struct {
		Exclusion string `json:"exclusion" jsonschema:"the exclusion to lift: its id, tvdb:<id>, or the show's title as exclusion_list shows it"`
	}
	type removeOut struct {
		Removed exclusionRow `json:"removed" jsonschema:"the show import lists may add again"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "exclusion_remove",
		Description: "Lift an import list exclusion, so the lists may add the show again on their next sync.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		res, err := client.GetImportListExclusionPagedComplete(ctx, sonarr.GetImportListExclusionPagedOperationOptions{})
		if err != nil {
			return nil, removeOut{}, err
		}
		ref := strings.TrimSpace(in.Exclusion)
		tvdb := 0
		if m := providerRef.FindStringSubmatch(ref); len(m) == 3 && strings.EqualFold(m[1], "tvdb") {
			tvdb, _ = strconv.Atoi(m[2])
		}
		id, numeric := strconv.Atoi(ref)
		i := slices.IndexFunc(res.Items, func(e sonarr.ImportListExclusionResource) bool {
			return (numeric == nil && e.Id == id) || (tvdb != 0 && e.TvdbId == tvdb) || normaliseTitle(e.Title) == normaliseTitle(ref)
		})
		if i < 0 {
			return nil, removeOut{}, fmt.Errorf("no exclusion %q; exclusion_list shows the ones there are", in.Exclusion)
		}
		e := res.Items[i]
		if _, err := client.DeleteImportListExclusionById(ctx, e.Id); err != nil {
			return nil, removeOut{}, err
		}

		return nil, removeOut{Removed: exclusionRow{ID: e.Id, Title: e.Title, TvdbID: e.TvdbId}}, nil
	})
}

func registerBulkTools(r *registry) {
	client := r.client

	type bulkIn struct {
		Series          []string `json:"series"                      jsonschema:"the series to change, each by title, Sonarr id or tvdb:<id>"`
		Monitored       *bool    `json:"monitored,omitempty"`
		QualityProfile  string   `json:"quality_profile,omitempty"   jsonschema:"the quality profile to put them on, by name or id"`
		SeriesType      string   `json:"series_type,omitempty"       jsonschema:"standard, daily or anime"`
		SeasonFolder    *bool    `json:"season_folder,omitempty"`
		MonitorNewItems string   `json:"monitor_new_items,omitempty" jsonschema:"all or none: whether seasons that appear later are monitored"`
		RootFolder      string   `json:"root_folder,omitempty"       jsonschema:"the root folder to put them under; each keeps its own folder name"`
		MoveFiles       bool     `json:"move_files,omitempty"        jsonschema:"with root_folder: move the files there too; without it only the records change, for folders already moved by hand"`
		AddTags         []string `json:"add_tags,omitempty"          jsonschema:"tag labels to add to each, created when missing"`
		RemoveTags      []string `json:"remove_tags,omitempty"       jsonschema:"tag labels to take off each"`
	}
	type bulkOut struct {
		Series  []seriesSummary `json:"series"  jsonschema:"the series as they now stand"`
		Changed []string        `json:"changed" jsonschema:"what was set on every one of them"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "series_bulk_edit",
		Description: "Make one change across many series at once: monitoring, quality profile, series type, season folders, whether new seasons are monitored, the root folder (moving the files or not), and tags to add or take off. Every series named gets every setting given; series_edit changes one series and says what it was before.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bulkIn) (*mcp.CallToolResult, bulkOut, error) {
		if len(in.Series) == 0 {
			return nil, bulkOut{}, errors.New("name the series to change")
		}
		body := sonarr.SeriesEditorResource{Monitored: in.Monitored, SeasonFolder: in.SeasonFolder, MoveFiles: new(in.MoveFiles)}
		things := []lock.Thing{}
		for _, ref := range in.Series {
			s, err := r.resolveSeries(ctx, ref)
			if err != nil {
				return nil, bulkOut{}, err
			}
			if !slices.Contains(body.SeriesIds, s.Id) {
				body.SeriesIds = append(body.SeriesIds, s.Id)
				things = append(things, seriesLock(s.Id))
			}
		}

		var changed []string
		if in.Monitored != nil {
			changed = append(changed, fmt.Sprintf("monitored: %v", *in.Monitored))
		}
		if in.SeasonFolder != nil {
			changed = append(changed, fmt.Sprintf("season_folder: %v", *in.SeasonFolder))
		}
		if in.QualityProfile != "" {
			p, err := r.resolveProfile(ctx, in.QualityProfile)
			if err != nil {
				return nil, bulkOut{}, err
			}
			body.QualityProfileId = p.Id
			changed = append(changed, "quality_profile: "+p.Name)
		}
		if in.SeriesType != "" {
			t, err := parseSeriesType(in.SeriesType, "")
			if err != nil {
				return nil, bulkOut{}, err
			}
			body.SeriesType = t
			changed = append(changed, "series_type: "+string(t))
		}
		newItems, newItemsErr := parseNewItems(in.MonitorNewItems)
		if newItemsErr != nil {
			return nil, bulkOut{}, newItemsErr
		}
		if newItems != "" {
			body.MonitorNewItems = newItems
			changed = append(changed, "monitor_new_items: "+string(newItems))
		}
		if in.RootFolder != "" {
			folder, err := r.resolveRootFolder(ctx, in.RootFolder)
			if err != nil {
				return nil, bulkOut{}, err
			}
			body.RootFolderPath = folder.Path
			changed = append(changed, "root_folder: "+folder.Path)
		}
		if len(changed) == 0 && len(in.AddTags) == 0 && len(in.RemoveTags) == 0 {
			return nil, bulkOut{}, errors.New("nothing to change: give a setting, or tags to add or take off")
		}

		// the same series edited beside this call waits its turn, and a tag
		// made on the way is made once
		if len(in.AddTags) > 0 {
			things = append(things, lock.String(tagsLock))
		}
		unlock := r.locking().By(things...)
		defer unlock()

		// Sonarr's editor adds tags or takes them off in one call, not both
		send := func(tags []int, how sonarr.ApplyTags) ([]sonarr.SeriesResource, error) {
			call := body
			call.Tags, call.ApplyTags = tags, how
			res, err := client.PutSeriesEditor(ctx, call)

			return res.Model, err
		}
		var after []sonarr.SeriesResource
		switch {
		case len(in.AddTags) > 0:
			ids, err := r.resolveTags(ctx, in.AddTags, true)
			if err != nil {
				return nil, bulkOut{}, err
			}
			if after, err = send(ids, sonarr.ApplyTagsAdd); err != nil {
				return nil, bulkOut{}, err
			}
			changed = append(changed, "tags added: "+strings.Join(in.AddTags, ", "))
		case len(changed) > 0:
			var err error
			if after, err = send(nil, ""); err != nil {
				return nil, bulkOut{}, err
			}
		}
		if len(in.RemoveTags) > 0 {
			ids, err := r.resolveTags(ctx, in.RemoveTags, false)
			if err != nil {
				return nil, bulkOut{}, err
			}
			only := sonarr.SeriesEditorResource{SeriesIds: body.SeriesIds, Tags: ids, ApplyTags: sonarr.ApplyTagsRemove}
			res, err := client.PutSeriesEditor(ctx, only)
			if err != nil {
				return nil, bulkOut{}, err
			}
			after = res.Model
			changed = append(changed, "tags removed: "+strings.Join(in.RemoveTags, ", "))
		}

		profiles, tags, err := r.lookupNames(ctx)
		if err != nil {
			return nil, bulkOut{}, err
		}
		out := bulkOut{Changed: changed, Series: make([]seriesSummary, 0, len(after))}
		for i := range after {
			out.Series = append(out.Series, summariseSeries(&after[i], profiles, tags))
		}

		return nil, out, nil
	})
}

func registerSystemTools(r *registry) {
	client := r.client

	type backupRow struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind" jsonschema:"scheduled, manual or update"`
		Size string `json:"size"`
		Time string `json:"time"`
	}
	projectBackup := func(b *sonarr.BackupResource) backupRow {
		return backupRow{ID: b.Id, Name: b.Name, Kind: string(b.Type), Size: humanSize(b.Size), Time: b.Time}
	}
	backups := func(ctx context.Context) ([]sonarr.BackupResource, error) {
		res, err := client.GetSystemBackup(ctx)
		if err != nil {
			return nil, err
		}
		slices.SortStableFunc(res.Model, func(a, b sonarr.BackupResource) int { return strings.Compare(b.Time, a.Time) })

		return res.Model, nil
	}
	findBackup := func(ctx context.Context, ref string) (*sonarr.BackupResource, error) {
		all, err := backups(ctx)
		if err != nil {
			return nil, err
		}
		id, numeric := strconv.Atoi(strings.TrimSpace(ref))
		i := slices.IndexFunc(all, func(b sonarr.BackupResource) bool {
			return (numeric == nil && b.Id == id) || strings.EqualFold(b.Name, strings.TrimSpace(ref))
		})
		if i < 0 {
			return nil, fmt.Errorf("no backup %q; backup_list shows the ones there are", ref)
		}

		return &all[i], nil
	}

	type backupsOut struct {
		pageOut
		Backups []backupRow `json:"backups" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "backup_list",
		Description: "The backups Sonarr holds of its database and settings, newest first: the scheduled ones, the ones made by hand, and the ones it makes before an update.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, backupsOut, error) {
		all, err := backups(ctx)
		if err != nil {
			return nil, backupsOut{}, err
		}
		rows, paged := pageRows(all, in)
		out := backupsOut{pageOut: paged, Backups: make([]backupRow, 0, len(rows))}
		for i := range rows {
			out.Backups = append(out.Backups, projectBackup(&rows[i]))
		}

		return nil, out, nil
	})

	type backupCreateIn struct {
		Wait int `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the backup, default 60"`
	}
	type backupCreateOut struct {
		Command commandOut `json:"command"`
		Backup  *backupRow `json:"backup,omitempty" jsonschema:"the backup made, once it is finished"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "backup_create",
		Description: "Back up Sonarr's database and settings now, before a change worth being able to undo. The backup is kept with the scheduled ones, under Sonarr's own retention.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backupCreateIn) (*mcp.CallToolResult, backupCreateOut, error) {
		cmd, err := r.runCommand(ctx, "Backup", nil, waitFor(in.Wait))
		out := backupCreateOut{Command: cmd}
		if err != nil || !commandDone(sonarr.CommandStatus(cmd.Status)) {
			return nil, out, err
		}
		all, err := backups(ctx)
		if err != nil {
			return nil, out, err
		}
		if i := slices.IndexFunc(all, func(b sonarr.BackupResource) bool { return b.Type == sonarr.BackupTypeManual }); i >= 0 {
			out.Backup = new(projectBackup(&all[i]))
		}

		return nil, out, nil
	})

	type backupRefIn struct {
		Backup  string `json:"backup"            jsonschema:"the backup, by its id or its file name as backup_list shows it"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to go ahead; without it nothing changes and the answer says what a confirmed call would do"`
	}
	type backupDeleteOut struct {
		Deleted     string `json:"deleted,omitempty"      jsonschema:"the backup deleted"`
		WouldDelete string `json:"would_delete,omitempty" jsonschema:"without confirm: the backup a confirmed call deletes; nothing has changed"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "backup_delete",
		Description: "Delete one of Sonarr's backups from disk. Nothing changes without confirm: the answer names the backup a confirmed call would delete.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backupRefIn) (*mcp.CallToolResult, backupDeleteOut, error) {
		b, err := findBackup(ctx, in.Backup)
		if err != nil {
			return nil, backupDeleteOut{}, err
		}
		if !in.Confirm {
			return nil, backupDeleteOut{WouldDelete: b.Name}, nil
		}
		if _, err := client.DeleteSystemBackupById(ctx, b.Id); err != nil {
			return nil, backupDeleteOut{}, err
		}

		return nil, backupDeleteOut{Deleted: b.Name}, nil
	})

	type restoreOut struct {
		Restored     string `json:"restored,omitempty"      jsonschema:"the backup Sonarr was restored from"`
		WouldRestore string `json:"would_restore,omitempty" jsonschema:"without confirm: the backup a confirmed call restores; nothing has changed"`
		Loses        string `json:"loses"                   jsonschema:"what a restore puts back, and so what is lost"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "backup_restore",
		Description: "Restore Sonarr's database and settings from one of its backups: everything it knows goes back to how it was when the backup was made, and Sonarr has to be restarted (server_restart) to load it. Nothing changes without confirm: the answer names the backup a confirmed call would restore.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backupRefIn) (*mcp.CallToolResult, restoreOut, error) {
		b, err := findBackup(ctx, in.Backup)
		if err != nil {
			return nil, restoreOut{}, err
		}
		out := restoreOut{Loses: "every series added, setting changed and download recorded since " + day(b.Time) + "; files on disk are not touched, and Sonarr must be restarted to load what was restored"}
		if !in.Confirm {
			out.WouldRestore = b.Name

			return nil, out, nil
		}
		if _, err := client.PostSystemBackupRestoreById(ctx, b.Id); err != nil {
			return nil, restoreOut{}, err
		}
		out.Restored = b.Name

		return nil, out, nil
	})

	type updateRow struct {
		Version     string   `json:"version"`
		Branch      string   `json:"branch,omitempty"`
		Released    string   `json:"released,omitempty"`
		Installed   bool     `json:"installed"          jsonschema:"this is, or has been, the version running"`
		Latest      bool     `json:"latest"`
		Installable bool     `json:"installable"        jsonschema:"Sonarr can install it itself; false in a container, which is updated by replacing its image"`
		New         []string `json:"new"                jsonschema:"what it adds"`
		Fixed       []string `json:"fixed"              jsonschema:"what it fixes"`
	}
	type updatesOut struct {
		pageOut
		Updates []updateRow `json:"updates" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_updates",
		Description: "The Sonarr releases on this server's branch, newest first: which one is running, which are newer, whether Sonarr can install them itself, and what each adds and fixes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, updatesOut, error) {
		res, err := client.GetUpdate(ctx)
		if err != nil {
			return nil, updatesOut{}, err
		}
		updates, paged := pageRows(res.Model, in)
		out := updatesOut{pageOut: paged, Updates: make([]updateRow, 0, len(updates))}
		for _, u := range updates {
			row := updateRow{Version: u.Version, Branch: u.Branch, Released: day(u.ReleaseDate), Installed: boolv(u.Installed) || u.InstalledOn != "", Latest: boolv(u.Latest), Installable: boolv(u.Installable)}
			if u.Changes != nil {
				row.New, row.Fixed = u.Changes.New, u.Changes.Fixed
			}
			out.Updates = append(out.Updates, row)
		}

		return nil, out, nil
	})

	type powerIn struct {
		Confirm bool `json:"confirm,omitempty" jsonschema:"true to go ahead; without it nothing changes"`
	}
	type powerOut struct {
		Done  string `json:"done,omitempty"  jsonschema:"what Sonarr was told to do"`
		Would string `json:"would,omitempty" jsonschema:"without confirm: what a confirmed call does; nothing has changed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_restart",
		Description: "Restart Sonarr, which some settings and a restored backup need to take effect. It stops answering for some seconds, and whatever it was doing - a search, an import, a rename - is cut short. Nothing changes without confirm.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerIn) (*mcp.CallToolResult, powerOut, error) {
		const what = "restart Sonarr: it stops answering for some seconds, and what it was doing is cut short"
		if !in.Confirm {
			return nil, powerOut{Would: what}, nil
		}
		if _, err := client.PostSystemRestart(ctx); err != nil {
			return nil, powerOut{}, err
		}

		return nil, powerOut{Done: "Sonarr is restarting; server_info answers again once it is back"}, nil
	})

	add(r, writeTool, &mcp.Tool{
		Name:        "server_shutdown",
		Description: "Shut Sonarr down. Nothing here can start it again: it stays down until whoever runs it starts it, and no tool answers until then. Nothing changes without confirm.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in powerIn) (*mcp.CallToolResult, powerOut, error) {
		const what = "shut Sonarr down: it stays down until whoever runs it starts it again, and no tool here can"
		if !in.Confirm {
			return nil, powerOut{Would: what}, nil
		}
		if _, err := client.PostSystemShutdown(ctx); err != nil {
			return nil, powerOut{}, err
		}

		return nil, powerOut{Done: "Sonarr is shutting down"}, nil
	})

	type diskRow struct {
		Path        string  `json:"path"`
		Label       string  `json:"label,omitempty"`
		Free        string  `json:"free"`
		Total       string  `json:"total"`
		FreePercent float64 `json:"free_percent"`
	}
	type diskOut struct {
		pageOut
		Disks []diskRow `json:"disks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_disk_space",
		Description: "The disks Sonarr can see - the ones its root folders and its own data are on - with the space free and in all on each.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, diskOut, error) {
		res, err := client.GetDiskSpace(ctx)
		if err != nil {
			return nil, diskOut{}, err
		}
		disks, paged := pageRows(res.Model, in)
		out := diskOut{pageOut: paged, Disks: make([]diskRow, 0, len(disks))}
		for _, d := range disks {
			row := diskRow{Path: d.Path, Label: d.Label, Free: humanSize(d.FreeSpace), Total: humanSize(d.TotalSpace)}
			if d.TotalSpace > 0 {
				row.FreePercent = float64(d.FreeSpace*1000/d.TotalSpace) / 10
			}
			out.Disks = append(out.Disks, row)
		}

		return nil, out, nil
	})

	type logFileRow struct {
		Filename string `json:"filename"`
		Written  string `json:"last_written"`
	}
	type logFilesOut struct {
		pageOut
		Files []logFileRow `json:"files" jsonschema:"newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "logfile_list",
		Description: "Sonarr's log files on disk, newest first. They hold everything, debug and trace lines included when those are turned on, and go back further than log_list, which reads the warnings and errors Sonarr keeps in its database.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, logFilesOut, error) {
		res, err := client.GetLogFile(ctx)
		if err != nil {
			return nil, logFilesOut{}, err
		}
		slices.SortStableFunc(res.Model, func(a, b sonarr.LogFileResource) int { return strings.Compare(b.LastWriteTime, a.LastWriteTime) })
		files, paged := pageRows(res.Model, in)
		out := logFilesOut{pageOut: paged, Files: make([]logFileRow, 0, len(files))}
		for _, f := range files {
			out.Files = append(out.Files, logFileRow{Filename: f.Filename, Written: f.LastWriteTime})
		}

		return nil, out, nil
	})

	type logReadIn struct {
		Filename string `json:"filename,omitempty" jsonschema:"the file to read, as logfile_list names it; default sonarr.txt, the current one"`
		Contains string `json:"contains,omitempty" jsonschema:"only the lines with this text in them, whatever its case: a release's name, a series, an exception"`
		Lines    int    `json:"lines,omitempty"    jsonschema:"how many lines to answer, from the end of the file, default 200, at most 2000"`
	}
	type logReadOut struct {
		Filename string   `json:"filename"`
		Matched  int      `json:"matched"  jsonschema:"lines in the file that matched, of which the last are answered"`
		Lines    []string `json:"lines"    jsonschema:"oldest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "logfile_read",
		Description: "Read the end of one of Sonarr's log files, or only its lines with some text in them: what Sonarr was doing around a failed import or a search that found nothing, in more detail than log_list keeps.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logReadIn) (*mcp.CallToolResult, logReadOut, error) {
		name := strings.TrimSpace(in.Filename)
		if name == "" {
			name = "sonarr.txt"
		}
		res, err := client.GetLogFileByFilename(ctx, name)
		switch {
		case apiclient.IsNotFound(err):
			return nil, logReadOut{}, fmt.Errorf("no log file %q; logfile_list shows the ones there are", name)
		case err != nil:
			return nil, logReadOut{}, err
		}
		defer func() { _ = res.HttpResponse.Body.Close() }()

		keep := min(limitOr(in.Lines, 200), 2000)
		want := strings.ToLower(in.Contains)
		out := logReadOut{Filename: name}
		lines := bufio.NewScanner(res.HttpResponse.Body)
		lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for lines.Scan() {
			line := lines.Text()
			if want != "" && !strings.Contains(strings.ToLower(line), want) {
				continue
			}
			out.Matched++
			// the last lines are kept, the ones before them let go
			if out.Lines = append(out.Lines, line); len(out.Lines) > keep {
				out.Lines = out.Lines[1:]
			}
		}
		if err := lines.Err(); err != nil && !errors.Is(err, io.EOF) {
			return nil, out, fmt.Errorf("reading %s: %w", name, err)
		}

		return nil, out, nil
	})

	type cancelIn struct {
		CommandID int `json:"command_id" jsonschema:"the command to cancel, as command_list or the tool that started it names it"`
	}
	type cancelOut struct {
		Cancelled commandOut `json:"cancelled" jsonschema:"the command as it stood when it was cancelled"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "command_cancel",
		Description: "Cancel a command Sonarr has queued and not yet started - a search or a rename waiting behind another - by its command_id. Sonarr cannot stop a command once it is running, and one that has finished is not undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelIn) (*mcp.CallToolResult, cancelOut, error) {
		cur, err := client.GetCommandById(ctx, in.CommandID)
		switch {
		case apiclient.IsNotFound(err):
			return nil, cancelOut{}, fmt.Errorf("sonarr has no command %d", in.CommandID)
		case err != nil:
			return nil, cancelOut{}, err
		}
		switch {
		case commandDone(cur.Model.Status):
			return nil, cancelOut{}, fmt.Errorf("command %d (%s) has already %s: there is nothing left to cancel", in.CommandID, cur.Model.Name, cur.Model.Status)
		case cur.Model.Status == sonarr.CommandStatusStarted:
			return nil, cancelOut{}, fmt.Errorf("command %d (%s) has already started, and Sonarr cannot stop a command once it is running", in.CommandID, cur.Model.Name)
		}
		// it may start between the look and the call, which Sonarr answers
		// with a conflict
		if _, err := client.DeleteCommandById(ctx, in.CommandID); err != nil {
			if apiclient.StatusCode(err) == http.StatusConflict {
				return nil, cancelOut{}, fmt.Errorf("command %d (%s) has already started, and Sonarr cannot stop a command once it is running", in.CommandID, cur.Model.Name)
			}
			return nil, cancelOut{}, err
		}

		return nil, cancelOut{Cancelled: projectCommand(cur.Model)}, nil
	})

	type browseIn struct {
		Path  string `json:"path,omitempty"  jsonschema:"the folder to look in, as Sonarr sees it, e.g. /tv; default the top"`
		Files bool   `json:"files,omitempty" jsonschema:"list the files in it as well as the folders"`
		pageIn
	}
	type browseRow struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Kind string `json:"kind"           jsonschema:"folder or file"`
		Size string `json:"size,omitempty"`
	}
	type browseOut struct {
		Path   string `json:"path"`
		Parent string `json:"parent,omitempty"`
		pageOut
		Entries []browseRow `json:"entries" jsonschema:"folders first, then files"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "filesystem_list",
		Description: "The folders (and with files, the files) in a folder as Sonarr sees it: for finding the path a root folder, a remote path mapping or an import takes, which is the path inside Sonarr's container and not the host's.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in browseIn) (*mcp.CallToolResult, browseOut, error) {
		path := strings.TrimSpace(in.Path)
		// Sonarr lists the contents of a path ending in a separator, and the
		// parent's contents otherwise
		if path != "" && !strings.HasSuffix(path, "/") {
			path += "/"
		}
		res, err := client.GetFileSystem(ctx, sonarr.GetFileSystemOperationOptions{Path: path, IncludeFiles: new(in.Files)})
		if err != nil {
			return nil, browseOut{}, err
		}
		var listing struct {
			Parent      string `json:"parent"`
			Directories []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"directories"`
			Files []struct {
				Name string `json:"name"`
				Path string `json:"path"`
				Size int64  `json:"size"`
			} `json:"files"`
		}
		if err := unmarshalRaw(res.Model, &listing); err != nil {
			return nil, browseOut{}, fmt.Errorf("reading %s: %w", in.Path, err)
		}
		rows := make([]browseRow, 0, len(listing.Directories)+len(listing.Files))
		for _, d := range listing.Directories {
			rows = append(rows, browseRow{Name: d.Name, Path: d.Path, Kind: "folder"})
		}
		for _, f := range listing.Files {
			rows = append(rows, browseRow{Name: f.Name, Path: f.Path, Kind: "file", Size: humanSize(f.Size)})
		}
		out := browseOut{Path: strings.TrimSuffix(path, "/"), Parent: listing.Parent}
		out.Entries, out.pageOut = pageRows(rows, in.pageIn)

		return nil, out, nil
	})
}
