package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// profileRow is a quality profile, as a person reads its settings page.
type profileRow struct {
	ID                int            `json:"id"`
	Name              string         `json:"name"`
	UpgradeAllowed    bool           `json:"upgrade_allowed"`
	Cutoff            string         `json:"cutoff"                     jsonschema:"the quality Sonarr stops upgrading at"`
	Allowed           []string       `json:"allowed_qualities"          jsonschema:"the qualities it will grab, best first"`
	MinFormatScore    int            `json:"min_custom_format_score"`
	CutoffFormatScore int            `json:"cutoff_custom_format_score"`
	FormatScores      map[string]int `json:"custom_format_scores"       jsonschema:"the custom formats the profile scores, with their scores; formats scored 0 are left out"`
	Series            int            `json:"series"                     jsonschema:"series on this profile"`
}

// allowedQualities flattens a profile's items, best first as Sonarr orders
// them (the items are stored worst first), into the qualities it allows.
func allowedQualities(items []sonarr.QualityProfileQualityItemResource) []string {
	var out []string
	for _, it := range slices.Backward(items) {
		switch {
		case !boolv(it.Allowed):
		case len(it.Items) > 0:
			out = append(out, allowedQualities(it.Items)...)
		case it.Quality != nil:
			out = append(out, it.Quality.Name)
		}
	}

	return out
}

// cutoffName names a profile's cutoff, which is a quality id or a group id.
func cutoffName(p *sonarr.QualityProfileResource) string {
	var find func(items []sonarr.QualityProfileQualityItemResource) string
	find = func(items []sonarr.QualityProfileQualityItemResource) string {
		for _, it := range items {
			if it.Quality != nil && it.Quality.Id == p.Cutoff {
				return it.Quality.Name
			}
			if len(it.Items) > 0 && it.Id == p.Cutoff {
				return it.Name
			}
			if n := find(it.Items); n != "" {
				return n
			}
		}
		return ""
	}

	return find(p.Items)
}

func projectProfile(p *sonarr.QualityProfileResource, series int) profileRow {
	row := profileRow{
		ID: p.Id, Name: p.Name, UpgradeAllowed: boolv(p.UpgradeAllowed), Cutoff: cutoffName(p), Allowed: allowedQualities(p.Items),
		MinFormatScore: p.MinFormatScore, CutoffFormatScore: p.CutoffFormatScore, FormatScores: map[string]int{}, Series: series,
	}
	for _, f := range p.FormatItems {
		if f.Score != 0 {
			row.FormatScores[f.Name] = f.Score
		}
	}

	return row
}

// tagRow is a tag and everything that uses it.
type tagRow struct {
	ID              int      `json:"id"`
	Label           string   `json:"label"`
	Series          []string `json:"series"           jsonschema:"the series with the tag"`
	Indexers        int      `json:"indexers"`
	DownloadClients int      `json:"download_clients"`
	DelayProfiles   int      `json:"delay_profiles"`
	ReleaseProfiles int      `json:"release_profiles"`
	ImportLists     int      `json:"import_lists"`
	Notifications   int      `json:"notifications"`
	AutoTags        int      `json:"auto_tags"`
	InUse           bool     `json:"in_use"`
}

// rootFolderRow is a root folder and what is in it.
type rootFolderRow struct {
	ID              int      `json:"id"`
	Path            string   `json:"path"`
	Accessible      bool     `json:"accessible"       jsonschema:"Sonarr can reach the folder"`
	FreeSpace       string   `json:"free_space"`
	Series          int      `json:"series"           jsonschema:"series whose folder is in it"`
	UnmappedFolders []string `json:"unmapped_folders" jsonschema:"folders in it Sonarr does not know as a series, first 50"`
	UnmappedTotal   int      `json:"unmapped_total"`
}

// inFolder reports whether a path is inside a folder.
func inFolder(p, folder string) bool {
	folder = strings.TrimRight(folder, "/\\")

	return p == folder || strings.HasPrefix(p, folder+"/") || strings.HasPrefix(p, folder+"\\")
}

func registerConfigTools(r *registry) {
	client := r.client

	type profilesOut struct {
		Profiles []profileRow `json:"profiles"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "profile_list",
		Description: "The quality profiles: the qualities each allows, best first, the cutoff it upgrades to, whether it upgrades at all, the custom formats it scores and the scores it needs, and how many series are on it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, profilesOut, error) {
		res, err := client.GetQualityProfile(ctx)
		if err != nil {
			return nil, profilesOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, profilesOut{}, err
		}
		counts := map[int]int{}
		for i := range series {
			counts[series[i].QualityProfileId]++
		}
		out := profilesOut{}
		for i := range res.Model {
			out.Profiles = append(out.Profiles, projectProfile(&res.Model[i], counts[res.Model[i].Id]))
		}

		return nil, out, nil
	})

	type tagsOut struct {
		Tags []tagRow `json:"tags"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "tag_list",
		Description: "Every tag and what uses it: which series carry it, and how many indexers, download clients, delay and release profiles, import lists, notifications and auto-tags are scoped by it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tagsOut, error) {
		details, err := client.GetTagDetail(ctx)
		if err != nil {
			return nil, tagsOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, tagsOut{}, err
		}
		titles := map[int]string{}
		for i := range series {
			titles[series[i].Id] = series[i].Title
		}
		out := tagsOut{}
		for _, d := range details.Model {
			row := tagRow{
				ID: d.Id, Label: d.Label, Indexers: len(d.IndexerIds), DownloadClients: len(d.DownloadClientIds),
				DelayProfiles: len(d.DelayProfileIds), ReleaseProfiles: len(d.RestrictionIds), ImportLists: len(d.ImportListIds),
				Notifications: len(d.NotificationIds), AutoTags: len(d.AutoTagIds),
			}
			for _, id := range d.SeriesIds {
				row.Series = append(row.Series, titles[id])
			}
			slices.Sort(row.Series)
			row.InUse = len(d.SeriesIds)+row.Indexers+row.DownloadClients+row.DelayProfiles+row.ReleaseProfiles+row.ImportLists+row.Notifications+row.AutoTags > 0
			out.Tags = append(out.Tags, row)
		}
		slices.SortFunc(out.Tags, func(a, b tagRow) int { return strings.Compare(a.Label, b.Label) })

		return nil, out, nil
	})

	type tagIn struct {
		Label string `json:"label"`
	}
	type tagOut struct {
		ID    int    `json:"id"`
		Label string `json:"label"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "tag_create",
		Description: "Create a tag. Sonarr stores labels in lower case. series_edit tags a series (creating the tag as it goes), so this is for a tag that scopes an indexer, download client or profile first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagIn) (*mcp.CallToolResult, tagOut, error) {
		label := strings.ToLower(strings.TrimSpace(in.Label))
		if label == "" {
			return nil, tagOut{}, errors.New("label is required")
		}
		existing, err := client.GetTag(ctx)
		if err != nil {
			return nil, tagOut{}, err
		}
		for _, t := range existing.Model {
			if strings.EqualFold(t.Label, label) {
				return nil, tagOut{}, fmt.Errorf("tag %q already exists (id %d)", t.Label, t.Id)
			}
		}
		res, err := client.PostTag(ctx, sonarr.TagResource{Label: label})
		if err != nil {
			return nil, tagOut{}, err
		}

		return nil, tagOut{ID: res.Model.Id, Label: res.Model.Label}, nil
	})

	type tagDeleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "tag_delete",
		Description: "Delete a tag nothing uses - the unused tags audit_profiles finds. Sonarr refuses to delete a tag still on a series, indexer, client or profile; take it off those first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagIn) (*mcp.CallToolResult, tagDeleteOut, error) {
		ids, err := r.resolveTags(ctx, []string{in.Label}, false)
		if err != nil {
			return nil, tagDeleteOut{}, err
		}
		if len(ids) != 1 {
			return nil, tagDeleteOut{}, errors.New("label is required")
		}
		if _, err := client.DeleteTagById(ctx, ids[0]); err != nil {
			return nil, tagDeleteOut{}, err
		}

		return nil, tagDeleteOut{Deleted: strings.ToLower(strings.TrimSpace(in.Label))}, nil
	})

	type formatRow struct {
		ID             int            `json:"id"`
		Name           string         `json:"name"`
		Specifications []string       `json:"specifications" jsonschema:"what a release must (or must not) have to match, e.g. ReleaseTitleSpecification: x265"`
		Scores         map[string]int `json:"scores"         jsonschema:"the quality profiles that score it, with the score"`
	}
	type formatsOut struct {
		Formats []formatRow `json:"custom_formats"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "customformat_list",
		Description: "The custom formats: what each matches in a release or file, and which quality profiles score it and how.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, formatsOut, error) {
		formats, err := client.GetCustomFormat(ctx)
		if err != nil {
			return nil, formatsOut{}, err
		}
		profiles, err := client.GetQualityProfile(ctx)
		if err != nil {
			return nil, formatsOut{}, err
		}
		out := formatsOut{}
		for _, f := range formats.Model {
			row := formatRow{ID: f.Id, Name: f.Name, Scores: map[string]int{}}
			for _, spec := range f.Specifications {
				desc := spec.Implementation + ": " + spec.Name
				if boolv(spec.Negate) {
					desc = "not " + desc
				}
				if boolv(spec.Required) {
					desc += " (required)"
				}
				row.Specifications = append(row.Specifications, desc)
			}
			for _, p := range profiles.Model {
				for _, item := range p.FormatItems {
					if item.Format == f.Id && item.Score != 0 {
						row.Scores[p.Name] = item.Score
					}
				}
			}
			out.Formats = append(out.Formats, row)
		}

		return nil, out, nil
	})

	type foldersOut struct {
		RootFolders []rootFolderRow `json:"root_folders"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "rootfolder_list",
		Description: "The root folders series live in: whether Sonarr can reach each, its free space, how many series are in it, and the folders in it Sonarr does not know as a series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, foldersOut, error) {
		res, err := client.GetRootFolder(ctx)
		if err != nil {
			return nil, foldersOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, foldersOut{}, err
		}
		out := foldersOut{}
		for _, f := range res.Model {
			out.RootFolders = append(out.RootFolders, projectRootFolder(&f, series))
		}

		return nil, out, nil
	})

	type folderIn struct {
		Path string `json:"path" jsonschema:"the folder as Sonarr sees it, e.g. /tv"`
	}
	type folderOut struct {
		RootFolder rootFolderRow `json:"root_folder"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "rootfolder_add",
		Description: "Add a root folder for series to live in. Sonarr checks it exists and that it can write there.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in folderIn) (*mcp.CallToolResult, folderOut, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, folderOut{}, errors.New("path is required")
		}
		res, err := client.PostRootFolder(ctx, sonarr.RootFolderResource{Path: strings.TrimSpace(in.Path)})
		if err != nil {
			return nil, folderOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, folderOut{}, err
		}

		return nil, folderOut{RootFolder: projectRootFolder(res.Model, series)}, nil
	})

	type removeOut struct {
		Removed string `json:"removed"`
		Series  int    `json:"series_still_in_it" jsonschema:"series whose folder is in it: Sonarr keeps them, but they have no root folder until one is added back"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "rootfolder_remove",
		Description: "Stop Sonarr using a root folder. Nothing on disk is touched, and series already in it stay in the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in folderIn) (*mcp.CallToolResult, removeOut, error) {
		// never the only root folder by default: removing one is named
		if strings.TrimSpace(in.Path) == "" {
			return nil, removeOut{}, errors.New("path is required")
		}
		f, err := r.resolveRootFolder(ctx, in.Path)
		if err != nil {
			return nil, removeOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, removeOut{}, err
		}
		if _, err := client.DeleteRootFolderById(ctx, f.Id); err != nil {
			return nil, removeOut{}, err
		}

		return nil, removeOut{Removed: f.Path, Series: projectRootFolder(f, series).Series}, nil
	})
}

func projectRootFolder(f *sonarr.RootFolderResource, series []sonarr.SeriesResource) rootFolderRow {
	row := rootFolderRow{
		ID: f.Id, Path: f.Path, Accessible: boolv(f.Accessible), FreeSpace: humanSize(f.FreeSpace), UnmappedTotal: len(f.UnmappedFolders),
	}
	for i := range series {
		if inFolder(series[i].Path, f.Path) {
			row.Series++
		}
	}
	for i, u := range f.UnmappedFolders {
		if i == 50 {
			break
		}
		row.UnmappedFolders = append(row.UnmappedFolders, u.Name)
	}

	return row
}
