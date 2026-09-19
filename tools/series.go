package tools

import (
	"context"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// seriesSummary is a series as a list reads it: enough to pick one out and
// see how complete it is, never the whole SeriesResource (fifty fields, with
// every season, image and alternate title).
type seriesSummary struct {
	ID             int      `json:"id"`
	Title          string   `json:"title"`
	Year           int      `json:"year,omitempty"`
	Status         string   `json:"status"          jsonschema:"continuing, ended or upcoming"`
	Monitored      bool     `json:"monitored"`
	QualityProfile string   `json:"quality_profile"`
	SeriesType     string   `json:"series_type"     jsonschema:"standard, daily or anime: how Sonarr numbers and names the episodes"`
	Path           string   `json:"path"`
	Tags           []string `json:"tags"`
	EpisodesHave   int      `json:"episodes_have"   jsonschema:"episodes with a file"`
	EpisodesWanted int      `json:"episodes_wanted" jsonschema:"the episodes Sonarr counts toward complete: aired and monitored, or with a file"`
	EpisodesTotal  int      `json:"episodes_total"  jsonschema:"every episode the series has, aired or not, monitored or not"`
	Size           string   `json:"size_on_disk"`
}

// summariseSeries projects a series, naming its profile and tags from the
// maps the caller read once.
func summariseSeries(s *sonarr.SeriesResource, profiles, tags map[int]string) seriesSummary {
	out := seriesSummary{
		ID: s.Id, Title: s.Title, Year: s.Year, Status: string(s.Status), Monitored: boolv(s.Monitored),
		QualityProfile: profiles[s.QualityProfileId], SeriesType: string(s.SeriesType), Path: s.Path,
		Tags: labels(s.Tags, tags),
	}
	if st := s.Statistics; st != nil {
		out.EpisodesHave, out.EpisodesWanted, out.EpisodesTotal = st.EpisodeFileCount, st.EpisodeCount, st.TotalEpisodeCount
		out.Size = humanSize(st.SizeOnDisk)
	}

	return out
}

// lookupNames reads the profile and tag names a series summary needs.
func (r *registry) lookupNames(ctx context.Context) (profiles, tags map[int]string, err error) {
	if profiles, err = r.profileNames(ctx); err != nil {
		return nil, nil, err
	}
	if tags, err = r.tagLabels(ctx); err != nil {
		return nil, nil, err
	}

	return profiles, tags, nil
}

func registerSeriesTools(r *registry) {
	type listIn struct {
		Query          string `json:"query,omitempty"           jsonschema:"only series whose title (or an alternate title) contains this"`
		Monitored      *bool  `json:"monitored,omitempty"       jsonschema:"only monitored (true) or unmonitored (false) series"`
		Status         string `json:"status,omitempty"          jsonschema:"only continuing, ended or upcoming series"`
		Tag            string `json:"tag,omitempty"             jsonschema:"only series with this tag"`
		QualityProfile string `json:"quality_profile,omitempty" jsonschema:"only series on this quality profile"`
		Incomplete     bool   `json:"incomplete,omitempty"      jsonschema:"only series missing episodes Sonarr wants"`
		Limit          int    `json:"limit,omitempty"           jsonschema:"series to return, default 100"`
		Offset         int    `json:"offset,omitempty"          jsonschema:"series to skip, to page through a long list"`
	}
	type listOut struct {
		Total  int             `json:"total"  jsonschema:"series matching the filters, of which this page is a slice"`
		Series []seriesSummary `json:"series"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_list",
		Description: "List the series in Sonarr, sorted by title, filtered by title, monitoring, status, tag, quality profile, or only those missing episodes. Each row says how many episodes are on disk out of those Sonarr wants.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		all, err := r.allSeries(ctx)
		if err != nil {
			return nil, listOut{}, err
		}
		profiles, tags, err := r.lookupNames(ctx)
		if err != nil {
			return nil, listOut{}, err
		}
		query := normaliseTitle(in.Query)
		out := listOut{}
		for i := range all {
			s := &all[i]
			if query != "" && !slices.ContainsFunc(append([]string{s.Title}, alternateTitles(s)...), func(t string) bool {
				return strings.Contains(normaliseTitle(t), query)
			}) {
				continue
			}
			sum := summariseSeries(s, profiles, tags)
			switch {
			case in.Monitored != nil && *in.Monitored != sum.Monitored:
			case in.Status != "" && !strings.EqualFold(in.Status, sum.Status):
			case in.Tag != "" && !slices.ContainsFunc(sum.Tags, func(t string) bool { return strings.EqualFold(t, in.Tag) }):
			case in.QualityProfile != "" && !strings.EqualFold(in.QualityProfile, sum.QualityProfile):
			case in.Incomplete && sum.EpisodesHave >= sum.EpisodesWanted:
			default:
				out.Total++
				if out.Total > in.Offset && len(out.Series) < limitOr(in.Limit, 100) {
					out.Series = append(out.Series, sum)
				}
			}
		}

		return nil, out, nil
	})

	type seriesIn struct {
		Series string `json:"series" jsonschema:"the series: its title (with the year if two share it), Sonarr id, or tvdb:<id>"`
	}
	type seasonRow struct {
		Season         int    `json:"season"`
		Monitored      bool   `json:"monitored"`
		EpisodesHave   int    `json:"episodes_have"`
		EpisodesWanted int    `json:"episodes_wanted"`
		EpisodesTotal  int    `json:"episodes_total"`
		Size           string `json:"size_on_disk"`
		NextAiring     string `json:"next_airing,omitempty"`
	}
	type ids struct {
		Tvdb   int    `json:"tvdb,omitempty"`
		Imdb   string `json:"imdb,omitempty"`
		Tmdb   int    `json:"tmdb,omitempty"`
		TvMaze int    `json:"tvmaze,omitempty"`
	}
	type getOut struct {
		seriesSummary
		Overview         string      `json:"overview,omitempty"`
		Network          string      `json:"network,omitempty"`
		Runtime          int         `json:"runtime_minutes,omitempty"`
		Genres           []string    `json:"genres"`
		OriginalLanguage string      `json:"original_language,omitempty"`
		FirstAired       string      `json:"first_aired,omitempty"`
		PreviousAiring   string      `json:"previous_airing,omitempty"`
		NextAiring       string      `json:"next_airing,omitempty"`
		Added            string      `json:"added"`
		RootFolder       string      `json:"root_folder"`
		SeasonFolder     bool        `json:"season_folder"`
		MonitorNewItems  string      `json:"monitor_new_items"           jsonschema:"all: new seasons are monitored as they appear; none: they are not"`
		AlternateTitles  []string    `json:"alternate_titles"`
		IDs              ids         `json:"ids"`
		Seasons          []seasonRow `json:"seasons"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "series_get",
		Description: "One series in full: overview, network, runtime, genres, the ids, where it lives, its profile and tags, how Sonarr treats new seasons, and each season's monitoring and episode counts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seriesIn) (*mcp.CallToolResult, getOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, getOut{}, err
		}
		profiles, tags, err := r.lookupNames(ctx)
		if err != nil {
			return nil, getOut{}, err
		}
		out := getOut{
			seriesSummary: summariseSeries(s, profiles, tags),
			Overview:      s.Overview, Network: s.Network, Runtime: s.Runtime, Genres: s.Genres,
			FirstAired: day(s.FirstAired), PreviousAiring: day(s.PreviousAiring), NextAiring: day(s.NextAiring),
			Added: day(s.Added), RootFolder: s.RootFolderPath, SeasonFolder: boolv(s.SeasonFolder),
			MonitorNewItems: string(s.MonitorNewItems), AlternateTitles: alternateTitles(s),
			IDs: ids{Tvdb: s.TvdbId, Imdb: s.ImdbId, Tmdb: s.TmdbId, TvMaze: s.TvMazeId},
		}
		if s.OriginalLanguage != nil {
			out.OriginalLanguage = s.OriginalLanguage.Name
		}
		for _, season := range s.Seasons {
			row := seasonRow{Season: season.SeasonNumber, Monitored: boolv(season.Monitored)}
			if st := season.Statistics; st != nil {
				row.EpisodesHave, row.EpisodesWanted, row.EpisodesTotal = st.EpisodeFileCount, st.EpisodeCount, st.TotalEpisodeCount
				row.Size, row.NextAiring = humanSize(st.SizeOnDisk), day(st.NextAiring)
			}
			out.Seasons = append(out.Seasons, row)
		}
		slices.SortFunc(out.Seasons, func(a, b seasonRow) int { return a.Season - b.Season })

		return nil, out, nil
	})
}
