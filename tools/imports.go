package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// importCandidate is a file Sonarr could import, and how it reads it.
type importCandidate struct {
	Path         string   `json:"path"`
	Size         string   `json:"size"`
	Series       string   `json:"series,omitempty"        jsonschema:"the series Sonarr matched the file to; empty when it could not"`
	SeriesID     int      `json:"series_id,omitempty"`
	Episodes     string   `json:"episodes,omitempty"      jsonschema:"the episodes Sonarr matched it to; empty when it could not"`
	Quality      string   `json:"quality"`
	Languages    []string `json:"languages"`
	ReleaseGroup string   `json:"release_group,omitempty"`
	Rejections   []string `json:"rejections"              jsonschema:"why Sonarr would not import it on its own: unparseable, a sample, not an upgrade, already imported"`
	Importable   bool     `json:"importable"              jsonschema:"Sonarr matched a series and episodes and has no permanent objection; anything else needs its series and episodes given to import_apply"`
}

func projectCandidate(m *sonarr.ManualImportResource) importCandidate {
	row := importCandidate{
		Path: m.Path, Size: humanSize(m.Size), Quality: qualityName(m.Quality), Languages: languageNames(m.Languages),
		ReleaseGroup: m.ReleaseGroup,
	}
	if m.Series != nil && m.Series.Id > 0 {
		row.Series, row.SeriesID = m.Series.Title, m.Series.Id
	}
	numbers := make([]int, 0, len(m.Episodes))
	season := m.SeasonNumber
	for _, e := range m.Episodes {
		numbers = append(numbers, e.EpisodeNumber)
		season = e.SeasonNumber
	}
	if len(numbers) > 0 {
		row.Episodes = episodeLabel(season, numbers...)
	}
	permanent := false
	for _, rej := range m.Rejections {
		row.Rejections = append(row.Rejections, rej.Reason)
		if rej.Type != sonarr.RejectionTypeTemporary {
			permanent = true
		}
	}
	row.Importable = row.SeriesID > 0 && len(numbers) > 0 && !permanent

	return row
}

// scanFolder asks Sonarr what it makes of the video files in a folder that it
// is not already tracking: the series and episodes it reads from each name,
// the quality, and anything that would stop it importing the file.
//
// Sonarr leaves out the files it tracks only when it recognises the folder as
// a series' own; a season folder inside one is not, and it lists every file
// there, tracked or not. So the tracked files of any series the folder is in
// are left out here.
func (r *registry) scanFolder(ctx context.Context, folder string) ([]sonarr.ManualImportResource, error) {
	res, err := r.client.GetManualImport(ctx, sonarr.GetManualImportOperationOptions{Folder: folder, FilterExistingFiles: new(true)})
	if err != nil {
		return nil, err
	}
	series, err := r.allSeries(ctx)
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{}
	for i := range series {
		s := &series[i]
		if !inFolder(folder, s.Path) && !inFolder(s.Path, folder) {
			continue
		}
		files, err := r.client.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOperationOptions{SeriesId: s.Id})
		if err != nil {
			return nil, err
		}
		for _, f := range files.Model {
			tracked[f.Path] = true
		}
	}

	return slices.DeleteFunc(res.Model, func(m sonarr.ManualImportResource) bool { return tracked[m.Path] }), nil
}

func registerImportTools(r *registry) {
	type scanIn struct {
		Folder string `json:"folder,omitempty" jsonschema:"a folder as Sonarr sees it, e.g. a download folder; or give series"`
		Series string `json:"series,omitempty" jsonschema:"scan this series' own folder for files Sonarr is not tracking: title, Sonarr id or tvdb:<id>"`
	}
	type scanOut struct {
		Folder string            `json:"folder"`
		Files  []importCandidate `json:"files"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "import_scan",
		Description: "Ask Sonarr what it makes of the video files in a folder that it is not tracking - a download folder, or a series' own folder - reading the series, episodes and quality from each name, and saying why it would not import a file on its own. import_apply imports them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scanIn) (*mcp.CallToolResult, scanOut, error) {
		folder, err := r.scanTarget(ctx, in.Folder, in.Series)
		if err != nil {
			return nil, scanOut{}, err
		}
		items, err := r.scanFolder(ctx, folder)
		if err != nil {
			return nil, scanOut{}, err
		}
		out := scanOut{Folder: folder}
		for i := range items {
			out.Files = append(out.Files, projectCandidate(&items[i]))
		}
		slices.SortFunc(out.Files, func(a, b importCandidate) int { return strings.Compare(a.Path, b.Path) })

		return nil, out, nil
	})

	type applyFile struct {
		Path      string   `json:"path"                jsonschema:"the file, as import_scan lists it"`
		Series    string   `json:"series,omitempty"    jsonschema:"the series it belongs to, when Sonarr could not tell: title, Sonarr id or tvdb:<id>"`
		Episodes  []string `json:"episodes,omitempty"  jsonschema:"the episodes it holds, when Sonarr could not tell: S01E02"`
		Quality   string   `json:"quality,omitempty"   jsonschema:"override the quality Sonarr read, by name, e.g. WEBDL-1080p"`
		Languages []string `json:"languages,omitempty" jsonschema:"override the languages Sonarr read"`
	}
	type applyIn struct {
		Files []applyFile `json:"files"                  jsonschema:"the files to import, each with what Sonarr cannot tell on its own"`
		Mode  string      `json:"mode,omitempty"         jsonschema:"move or copy the file into the series folder; default move (Sonarr's auto: hardlink or copy for a download it is seeding)"`
		Wait  int         `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the import, default 60"`
	}
	type appliedRow struct {
		Path     string `json:"path"`
		Series   string `json:"series"`
		Episodes string `json:"episodes"`
		Quality  string `json:"quality"`
		Imported bool   `json:"imported" jsonschema:"the episodes have a file now"`
	}
	type applyOut struct {
		Command commandOut   `json:"command"`
		Files   []appliedRow `json:"files"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "import_apply",
		Description: "Import video files into the library as the episodes they are - the files import_scan and audit_untracked_files list. A file from elsewhere is moved (or copied) into the series folder and named to the format; one already in the series folder is taken in where it is. Give the series and episodes for a file Sonarr could not read; the rest take what Sonarr read. Sonarr's own objections (not an upgrade, a sample) are overridden, as its manual import does.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		if len(in.Files) == 0 {
			return nil, applyOut{}, errors.New("name the files to import")
		}
		var mode string
		switch strings.ToLower(strings.TrimSpace(in.Mode)) {
		case "", "move", "auto":
			mode = "auto"
		case "copy":
			mode = "copy"
		default:
			return nil, applyOut{}, fmt.Errorf("mode must be move or copy, got %q", in.Mode)
		}

		var files []map[string]any
		out := applyOut{}
		type planned struct {
			seriesID int
			episodes []int
		}
		var plans []planned
		for _, f := range in.Files {
			items, err := r.scanFolder(ctx, f.Path)
			if err != nil {
				return nil, applyOut{}, err
			}
			i := slices.IndexFunc(items, func(m sonarr.ManualImportResource) bool { return m.Path == f.Path })
			if i < 0 {
				return nil, applyOut{}, fmt.Errorf("sonarr sees no importable video file at %s (already imported, or not a video file)", f.Path)
			}
			item := items[i]

			var series *sonarr.SeriesResource
			switch {
			case f.Series != "":
				if series, err = r.resolveSeries(ctx, f.Series); err != nil {
					return nil, applyOut{}, err
				}
			case item.Series != nil && item.Series.Id > 0:
				series = item.Series
			default:
				return nil, applyOut{}, fmt.Errorf("sonarr cannot tell which series %s belongs to; give its series", path.Base(f.Path))
			}
			var eps []sonarr.EpisodeResource
			if len(f.Episodes) > 0 {
				var all []sonarr.EpisodeResource
				if all, err = r.episodesOf(ctx, series.Id, nil, false); err != nil {
					return nil, applyOut{}, err
				}
				if eps, err = pickEpisodes(all, f.Episodes); err != nil {
					return nil, applyOut{}, err
				}
			} else if item.Series != nil && item.Series.Id == series.Id {
				eps = item.Episodes
			}
			if len(eps) == 0 {
				return nil, applyOut{}, fmt.Errorf("sonarr cannot tell which episodes %s holds; give its episodes", path.Base(f.Path))
			}

			quality := item.Quality
			if f.Quality != "" {
				var q *sonarr.Quality
				if q, err = r.resolveQuality(ctx, f.Quality); err != nil {
					return nil, applyOut{}, err
				}
				quality = &sonarr.QualityModel{Quality: q, Revision: &sonarr.Revision{Version: 1}}
			}
			langs := item.Languages
			if len(f.Languages) > 0 {
				if langs, err = r.resolveLanguages(ctx, f.Languages); err != nil {
					return nil, applyOut{}, err
				}
			}
			ids := episodeIDs(eps)
			files = append(files, map[string]any{
				"path": item.Path, "folderName": item.FolderName, "seriesId": series.Id, "episodeIds": ids,
				"quality": quality, "languages": langs, "releaseGroup": item.ReleaseGroup,
				"indexerFlags": item.IndexerFlags, "releaseType": item.ReleaseType, "downloadId": item.DownloadId,
			})
			var numbers []int
			for _, e := range eps {
				numbers = append(numbers, e.EpisodeNumber)
			}
			out.Files = append(out.Files, appliedRow{
				Path: item.Path, Series: series.Title, Episodes: episodeLabel(eps[0].SeasonNumber, numbers...), Quality: qualityName(quality),
			})
			plans = append(plans, planned{seriesID: series.Id, episodes: ids})
		}

		cmd, err := r.runCommand(ctx, "ManualImport", map[string]any{"files": files, "importMode": mode}, waitFor(in.Wait))
		out.Command = cmd
		if err != nil {
			return nil, out, err
		}
		// the command reports one outcome for every file, so each file's
		// episodes are looked at to say which of them landed
		for i, p := range plans {
			eps, err := r.episodesOf(ctx, p.seriesID, nil, false)
			if err != nil {
				return nil, out, err
			}
			out.Files[i].Imported = !slices.ContainsFunc(eps, func(e sonarr.EpisodeResource) bool {
				return slices.Contains(p.episodes, e.Id) && !boolv(e.HasFile)
			})
		}

		return nil, out, nil
	})
}

// scanTarget is the folder an import scan looks in: the one given, or a
// series' own.
func (r *registry) scanTarget(ctx context.Context, folder, series string) (string, error) {
	switch {
	case folder != "" && series != "":
		return "", errors.New("give a folder or a series, not both")
	case folder != "":
		return folder, nil
	case series != "":
		s, err := r.resolveSeries(ctx, series)
		if err != nil {
			return "", err
		}
		return s.Path, nil
	default:
		return "", errors.New("give a folder to scan, or a series whose folder to scan")
	}
}
