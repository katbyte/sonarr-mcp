package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apiclient "github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mediaRow is what the file's own streams say, which is not always what its
// name says.
type mediaRow struct {
	Resolution     string  `json:"resolution,omitempty"`
	VideoCodec     string  `json:"video_codec,omitempty"`
	VideoDynamic   string  `json:"video_dynamic_range,omitempty"`
	AudioCodec     string  `json:"audio_codec,omitempty"`
	AudioChannels  float64 `json:"audio_channels,omitempty"`
	AudioLanguages string  `json:"audio_languages,omitempty"`
	Subtitles      string  `json:"subtitles,omitempty"`
	Runtime        string  `json:"runtime,omitempty"`
}

func projectMedia(m *sonarr.MediaInfoResource) *mediaRow {
	if m == nil {
		return nil
	}

	return &mediaRow{
		Resolution: m.Resolution, VideoCodec: m.VideoCodec, VideoDynamic: m.VideoDynamicRangeType,
		AudioCodec: m.AudioCodec, AudioChannels: m.AudioChannels, AudioLanguages: m.AudioLanguages,
		Subtitles: m.Subtitles, Runtime: m.RunTime,
	}
}

// fileRow is an episode file in full.
type fileRow struct {
	ID                int       `json:"id"`
	Episodes          string    `json:"episodes"                jsonschema:"the episodes the file holds, S01E02 or S01E02-E03"`
	RelativePath      string    `json:"relative_path"`
	Size              string    `json:"size"`
	Quality           string    `json:"quality"`
	CutoffNotMet      bool      `json:"below_cutoff"`
	Languages         []string  `json:"languages"`
	ReleaseGroup      string    `json:"release_group,omitempty"`
	CustomFormats     []string  `json:"custom_formats"`
	CustomFormatScore int       `json:"custom_format_score"`
	Added             string    `json:"added"`
	Media             *mediaRow `json:"media,omitempty"`
}

// fileEpisodes maps each file of a series to the episode numbers it holds:
// the file resource has the season, the episodes are on the episode side.
func (r *registry) fileEpisodes(ctx context.Context, seriesID int) (map[int][]int, error) {
	eps, err := r.episodesOf(ctx, seriesID, nil, false)
	if err != nil {
		return nil, err
	}
	out := map[int][]int{}
	for _, e := range eps {
		if e.EpisodeFileId > 0 {
			out[e.EpisodeFileId] = append(out[e.EpisodeFileId], e.EpisodeNumber)
		}
	}

	return out, nil
}

func projectFile(f *sonarr.EpisodeFileResource, episodes []int) fileRow {
	row := fileRow{
		ID: f.Id, Episodes: episodeLabel(f.SeasonNumber, episodes...), RelativePath: f.RelativePath, Size: humanSize(f.Size),
		Quality: qualityName(f.Quality), CutoffNotMet: boolv(f.QualityCutoffNotMet), Languages: languageNames(f.Languages),
		ReleaseGroup: f.ReleaseGroup, CustomFormatScore: f.CustomFormatScore, Added: day(f.DateAdded), Media: projectMedia(f.MediaInfo),
	}
	for _, cf := range f.CustomFormats {
		row.CustomFormats = append(row.CustomFormats, cf.Name)
	}

	return row
}

// seriesFiles reads a series' files, with their episodes, in episode order.
func (r *registry) seriesFiles(ctx context.Context, seriesID int) ([]sonarr.EpisodeFileResource, map[int][]int, error) {
	res, err := r.client.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOperationOptions{SeriesId: seriesID})
	if err != nil {
		return nil, nil, err
	}
	byFile, err := r.fileEpisodes(ctx, seriesID)
	if err != nil {
		return nil, nil, err
	}
	files := res.Model
	first := func(f *sonarr.EpisodeFileResource) int {
		if eps := byFile[f.Id]; len(eps) > 0 {
			return slices.Min(eps)
		}
		return 0
	}
	slices.SortFunc(files, func(a, b sonarr.EpisodeFileResource) int {
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber - b.SeasonNumber
		}
		return first(&a) - first(&b)
	})

	return files, byFile, nil
}

func registerFileTools(r *registry) {
	client := r.client

	type listIn struct {
		Series string `json:"series"           jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Season *int   `json:"season,omitempty" jsonschema:"only this season; 0 is the specials"`
	}
	type listOut struct {
		Series string    `json:"series"`
		Path   string    `json:"path"`
		Files  []fileRow `json:"files"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "file_list",
		Description: "A series' episode files: which episodes each holds, its path, size, quality and whether that is below the profile's cutoff, languages, release group, custom formats and score, and what its streams say (resolution, codecs, audio languages, runtime).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, listOut{}, err
		}
		files, byFile, err := r.seriesFiles(ctx, s.Id)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{Series: s.Title, Path: s.Path}
		for i := range files {
			if in.Season != nil && files[i].SeasonNumber != *in.Season {
				continue
			}
			out.Files = append(out.Files, projectFile(&files[i], byFile[files[i].Id]))
		}

		return nil, out, nil
	})

	type editIn struct {
		FileIDs      []int    `json:"file_ids"                jsonschema:"the episode files to change, as file_list numbers them"`
		Quality      string   `json:"quality,omitempty"       jsonschema:"the quality to record, by Sonarr's name for it, e.g. HDTV-720p, WEBDL-1080p, Bluray-2160p"`
		Languages    []string `json:"languages,omitempty"     jsonschema:"the languages to record, by name, e.g. English, Japanese"`
		ReleaseGroup string   `json:"release_group,omitempty"`
	}
	type editOut struct {
		Files []fileRow `json:"files" jsonschema:"the files as Sonarr now records them"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "file_edit",
		Description: "Correct what Sonarr records about episode files - their quality, languages or release group - when the name misled it: a file named 1080p that is really 720p, a dub recorded as the original language. Upgrades and cutoff are judged on what is recorded.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if len(in.FileIDs) == 0 {
			return nil, editOut{}, errors.New("name the file_ids to change")
		}
		if in.Quality == "" && len(in.Languages) == 0 && in.ReleaseGroup == "" {
			return nil, editOut{}, errors.New("nothing to change: give a quality, languages or a release group")
		}
		var quality *sonarr.QualityModel
		if in.Quality != "" {
			q, err := r.resolveQuality(ctx, in.Quality)
			if err != nil {
				return nil, editOut{}, err
			}
			quality = &sonarr.QualityModel{Quality: q, Revision: &sonarr.Revision{Version: 1}}
		}
		var langs []sonarr.Language
		if len(in.Languages) > 0 {
			var err error
			if langs, err = r.resolveLanguages(ctx, in.Languages); err != nil {
				return nil, editOut{}, err
			}
		}
		body := make([]sonarr.EpisodeFileResource, 0, len(in.FileIDs))
		for _, id := range in.FileIDs {
			body = append(body, sonarr.EpisodeFileResource{Id: id, Quality: quality, Languages: langs, ReleaseGroup: in.ReleaseGroup})
		}
		res, err := client.PutEpisodeFileBulk(ctx, body)
		if err != nil {
			return nil, editOut{}, err
		}
		out := editOut{}
		if len(res.Model) == 0 {
			return nil, out, nil
		}
		byFile, err := r.fileEpisodes(ctx, res.Model[0].SeriesId)
		if err != nil {
			return nil, editOut{}, err
		}
		for i := range res.Model {
			out.Files = append(out.Files, projectFile(&res.Model[i], byFile[res.Model[i].Id]))
		}

		return nil, out, nil
	})

	type deleteIn struct {
		FileIDs []int `json:"file_ids" jsonschema:"the ids of the episode files to delete from disk"`
	}
	type deleteOut struct {
		Deleted []string `json:"deleted" jsonschema:"the paths deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "file_delete",
		Description: "Delete episode files from disk (to Sonarr's recycling bin, when one is set). Their episodes become missing, and are searched for again if monitored.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		if len(in.FileIDs) == 0 {
			return nil, deleteOut{}, errors.New("name the file_ids to delete")
		}
		// each file is read by its id first: asked for a list, Sonarr answers
		// 500 when any id is unknown, and the path is what the answer reports
		paths := make([]string, 0, len(in.FileIDs))
		for _, id := range in.FileIDs {
			res, err := client.GetEpisodeFileById(ctx, id)
			switch {
			case apiclient.IsNotFound(err):
				return nil, deleteOut{}, fmt.Errorf("no episode file %d", id)
			case err != nil:
				return nil, deleteOut{}, err
			}
			paths = append(paths, res.Model.Path)
		}
		out := deleteOut{}
		for i, id := range in.FileIDs {
			if _, err := client.DeleteEpisodeFileById(ctx, id); err != nil {
				return nil, out, err
			}
			out.Deleted = append(out.Deleted, paths[i])
		}

		return nil, out, nil
	})
}

// resolveQuality finds one of Sonarr's qualities by name.
func (r *registry) resolveQuality(ctx context.Context, name string) (*sonarr.Quality, error) {
	res, err := r.client.GetQualityDefinition(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range res.Model {
		if d.Quality == nil {
			continue
		}
		if strings.EqualFold(d.Quality.Name, strings.TrimSpace(name)) {
			return d.Quality, nil
		}
		names = append(names, d.Quality.Name)
	}

	return nil, fmt.Errorf("no quality %q (have: %s)", name, strings.Join(names, ", "))
}

// resolveLanguages finds Sonarr's languages by name.
func (r *registry) resolveLanguages(ctx context.Context, names []string) ([]sonarr.Language, error) {
	res, err := r.client.GetLanguage(ctx)
	if err != nil {
		return nil, err
	}
	var out []sonarr.Language
	for _, name := range names {
		i := slices.IndexFunc(res.Model, func(l sonarr.LanguageResource) bool { return strings.EqualFold(l.Name, strings.TrimSpace(name)) })
		if i < 0 {
			var have []string
			for _, l := range res.Model {
				have = append(have, l.Name)
			}
			return nil, fmt.Errorf("no language %q (have: %s)", name, strings.Join(have, ", "))
		}
		out = append(out, sonarr.Language{Id: res.Model[i].Id, Name: res.Model[i].Name})
	}

	return out, nil
}
