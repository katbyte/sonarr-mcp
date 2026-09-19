package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// episodeRow is an episode as a list reads it.
type episodeRow struct {
	ID        int      `json:"id"`
	Episode   string   `json:"episode"            jsonschema:"S01E02"`
	Absolute  int      `json:"absolute,omitempty" jsonschema:"the absolute number, for anime"`
	Title     string   `json:"title"`
	AirDate   string   `json:"air_date,omitempty"`
	Aired     bool     `json:"aired"`
	Monitored bool     `json:"monitored"`
	HasFile   bool     `json:"has_file"`
	File      *fileRef `json:"file,omitempty"`
}

// fileRef is the file an episode has, in brief.
type fileRef struct {
	ID           int      `json:"id"`
	Quality      string   `json:"quality"`
	Size         string   `json:"size"`
	Languages    []string `json:"languages"`
	CutoffNotMet bool     `json:"below_cutoff"  jsonschema:"the file's quality is below what the profile wants, so Sonarr would upgrade it"`
	RelativePath string   `json:"relative_path"`
}

func projectFileRef(f *sonarr.EpisodeFileResource) *fileRef {
	if f == nil || f.Id == 0 {
		return nil
	}

	return &fileRef{
		ID: f.Id, Quality: qualityName(f.Quality), Size: humanSize(f.Size), Languages: languageNames(f.Languages),
		CutoffNotMet: boolv(f.QualityCutoffNotMet), RelativePath: f.RelativePath,
	}
}

func projectEpisode(e *sonarr.EpisodeResource, now time.Time) episodeRow {
	return episodeRow{
		ID: e.Id, Episode: episodeLabel(e.SeasonNumber, e.EpisodeNumber), Absolute: e.AbsoluteEpisodeNumber,
		Title: e.Title, AirDate: e.AirDate, Aired: aired(e, now), Monitored: boolv(e.Monitored), HasFile: boolv(e.HasFile),
		File: projectFileRef(e.EpisodeFile),
	}
}

// episodesOf reads a series' episodes, every season or one.
func (r *registry) episodesOf(ctx context.Context, seriesID int, season *int, files bool) ([]sonarr.EpisodeResource, error) {
	opts := sonarr.GetEpisodeOperationOptions{SeriesId: seriesID, IncludeEpisodeFile: new(files)}
	if season != nil {
		// season 0 is the specials, which the option cannot send: its zero
		// value is left out, so they are filtered here instead
		opts.SeasonNumber = *season
	}
	res, err := r.client.GetEpisode(ctx, opts)
	if err != nil {
		return nil, err
	}
	eps := res.Model
	if season != nil {
		eps = slices.DeleteFunc(eps, func(e sonarr.EpisodeResource) bool { return e.SeasonNumber != *season })
	}
	slices.SortFunc(eps, func(a, b sonarr.EpisodeResource) int {
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber - b.SeasonNumber
		}
		return a.EpisodeNumber - b.EpisodeNumber
	})

	return eps, nil
}

var episodeRefRe = regexp.MustCompile(`(?i)^s?(\d{1,4})\s*[ex]\s*(\d{1,4})$`)

// pickEpisodes resolves episode references - S01E02, 1x02, or an episode id -
// against a series' episodes.
func pickEpisodes(eps []sonarr.EpisodeResource, refs []string) ([]sonarr.EpisodeResource, error) {
	if len(refs) == 0 {
		return nil, errors.New("name the episodes: S01E02, 1x02, or episode ids")
	}
	var out []sonarr.EpisodeResource
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		i := -1
		if m := episodeRefRe.FindStringSubmatch(ref); m != nil {
			season, _ := strconv.Atoi(m[1])
			ep, _ := strconv.Atoi(m[2])
			i = slices.IndexFunc(eps, func(e sonarr.EpisodeResource) bool { return e.SeasonNumber == season && e.EpisodeNumber == ep })
		} else if id, err := strconv.Atoi(ref); err == nil {
			i = slices.IndexFunc(eps, func(e sonarr.EpisodeResource) bool { return e.Id == id })
		} else {
			return nil, fmt.Errorf("%q is not an episode: use S01E02, 1x02 or an episode id", ref)
		}
		if i < 0 {
			return nil, fmt.Errorf("the series has no episode %s", ref)
		}
		if !slices.ContainsFunc(out, func(e sonarr.EpisodeResource) bool { return e.Id == eps[i].Id }) {
			out = append(out, eps[i])
		}
	}

	return out, nil
}

func episodeIDs(eps []sonarr.EpisodeResource) []int {
	out := make([]int, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.Id)
	}

	return out
}

func registerEpisodeTools(r *registry) {
	client := r.client

	type listIn struct {
		Series  string `json:"series"            jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Season  *int   `json:"season,omitempty"  jsonschema:"only this season; 0 is the specials"`
		Missing bool   `json:"missing,omitempty" jsonschema:"only monitored episodes that have aired and have no file"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"episodes to return, default 300"`
	}
	type listOut struct {
		Series   string       `json:"series"`
		Total    int          `json:"total"`
		Episodes []episodeRow `json:"episodes"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "episode_list",
		Description: "A series' episodes in order, each with its air date, whether it is monitored and on disk, and its file's quality, size and languages. Filter to one season, or to the missing ones.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, listOut{}, err
		}
		eps, err := r.episodesOf(ctx, s.Id, in.Season, true)
		if err != nil {
			return nil, listOut{}, err
		}
		now := time.Now()
		out := listOut{Series: s.Title}
		for i := range eps {
			row := projectEpisode(&eps[i], now)
			if in.Missing && (row.HasFile || !row.Monitored || !row.Aired) {
				continue
			}
			out.Total++
			if len(out.Episodes) < limitOr(in.Limit, 300) {
				out.Episodes = append(out.Episodes, row)
			}
		}

		return nil, out, nil
	})

	type monitorIn struct {
		Series    string   `json:"series"    jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Episodes  []string `json:"episodes"  jsonschema:"the episodes: S01E02, 1x02 or episode ids"`
		Monitored bool     `json:"monitored" jsonschema:"true to monitor them, false to stop"`
	}
	type monitorOut struct {
		Episodes []episodeRow `json:"episodes" jsonschema:"the episodes as they now stand"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "episode_monitor",
		Description: "Monitor or unmonitor episodes of a series. Sonarr searches for and upgrades only monitored episodes, and counts only those as missing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in monitorIn) (*mcp.CallToolResult, monitorOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, monitorOut{}, err
		}
		eps, err := r.episodesOf(ctx, s.Id, nil, false)
		if err != nil {
			return nil, monitorOut{}, err
		}
		picked, err := pickEpisodes(eps, in.Episodes)
		if err != nil {
			return nil, monitorOut{}, err
		}
		res, err := client.PutEpisodeMonitor(ctx, sonarr.EpisodesMonitoredResource{EpisodeIds: episodeIDs(picked), Monitored: new(in.Monitored)}, sonarr.PutEpisodeMonitorOperationOptions{})
		if err != nil {
			return nil, monitorOut{}, err
		}
		now := time.Now()
		out := monitorOut{}
		for i := range res.Model {
			out.Episodes = append(out.Episodes, projectEpisode(&res.Model[i], now))
		}

		return nil, out, nil
	})

	type seasonMonitorIn struct {
		Series    string `json:"series"    jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Seasons   []int  `json:"seasons"   jsonschema:"the season numbers; 0 is the specials"`
		Monitored bool   `json:"monitored" jsonschema:"true to monitor the seasons and every episode in them, false to stop"`
	}
	type seasonMonitorOut struct {
		Series  string   `json:"series"`
		Changed []string `json:"changed" jsonschema:"the seasons whose monitoring changed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "season_monitor",
		Description: "Monitor or unmonitor whole seasons of a series, and every episode in them, the way Sonarr's season toggle does.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seasonMonitorIn) (*mcp.CallToolResult, seasonMonitorOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, seasonMonitorOut{}, err
		}
		if len(in.Seasons) == 0 {
			return nil, seasonMonitorOut{}, errors.New("name the seasons to change")
		}
		cur, err := client.GetSeriesById(ctx, s.Id, sonarr.GetSeriesByIdOperationOptions{})
		if err != nil {
			return nil, seasonMonitorOut{}, err
		}
		body := *cur.Model
		out := seasonMonitorOut{Series: body.Title}
		for _, want := range in.Seasons {
			i := slices.IndexFunc(body.Seasons, func(season sonarr.SeasonResource) bool { return season.SeasonNumber == want })
			if i < 0 {
				return nil, seasonMonitorOut{}, fmt.Errorf("%s has no season %d", body.Title, want)
			}
			if boolv(body.Seasons[i].Monitored) != in.Monitored {
				body.Seasons[i].Monitored = new(in.Monitored)
				out.Changed = append(out.Changed, fmt.Sprintf("season %d: monitored %v", want, in.Monitored))
			}
		}
		if len(out.Changed) == 0 {
			return nil, out, nil
		}
		// the series' own flag follows: monitoring a season of an unmonitored
		// series would otherwise do nothing
		if in.Monitored && !boolv(body.Monitored) {
			body.Monitored = new(true)
			out.Changed = append(out.Changed, "series: monitored true")
		}
		// Sonarr sets every episode of a season whose flag changed to match
		// it (SeriesService.UpdateSeries), as its own season toggle does
		if _, err := client.PutSeriesById(ctx, strconv.Itoa(body.Id), body, sonarr.PutSeriesByIdOperationOptions{}); err != nil {
			return nil, seasonMonitorOut{}, err
		}

		return nil, out, nil
	})

	type searchIn struct {
		Series   string   `json:"series"                 jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Episodes []string `json:"episodes"               jsonschema:"the episodes: S01E02, 1x02 or episode ids"`
		Wait     int      `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the search, default 60; -1 queues it and returns at once"`
	}
	type searchOut struct {
		Command commandOut `json:"command"`
		Queued  []queueRow `json:"queued"  jsonschema:"what is in the download queue for these episodes after the search"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "episode_search",
		Description: "Search the indexers for particular episodes and grab the best release Sonarr accepts for each, then report what is in the download queue for them. release_search shows every release and why each was rejected, to choose by hand instead.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, searchOut{}, err
		}
		eps, err := r.episodesOf(ctx, s.Id, nil, false)
		if err != nil {
			return nil, searchOut{}, err
		}
		picked, err := pickEpisodes(eps, in.Episodes)
		if err != nil {
			return nil, searchOut{}, err
		}
		ids := episodeIDs(picked)
		cmd, err := r.runCommand(ctx, "EpisodeSearch", map[string]any{"episodeIds": ids}, waitFor(in.Wait))
		if err != nil {
			return nil, searchOut{}, err
		}
		queued, err := r.queueFor(ctx, s.Id, ids)
		if err != nil {
			return nil, searchOut{}, err
		}

		return nil, searchOut{Command: cmd, Queued: queued}, nil
	})

	type seasonSearchIn struct {
		Series string `json:"series"                 jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Season int    `json:"season"                 jsonschema:"the season number"`
		Wait   int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the search, default 60; -1 queues it and returns at once"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "season_search",
		Description: "Search the indexers for a whole season - season packs first, then single episodes - for its monitored episodes that are missing or below cutoff, then report what is in the download queue for it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seasonSearchIn) (*mcp.CallToolResult, searchOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, searchOut{}, err
		}
		if !slices.ContainsFunc(s.Seasons, func(season sonarr.SeasonResource) bool { return season.SeasonNumber == in.Season }) {
			return nil, searchOut{}, fmt.Errorf("%s has no season %d", s.Title, in.Season)
		}
		cmd, err := r.runCommand(ctx, "SeasonSearch", map[string]any{"seriesId": s.Id, "seasonNumber": in.Season}, waitFor(in.Wait))
		if err != nil {
			return nil, searchOut{}, err
		}
		queued, err := r.queueFor(ctx, s.Id, nil)
		if err != nil {
			return nil, searchOut{}, err
		}
		queued = slices.DeleteFunc(queued, func(q queueRow) bool { return q.Season != in.Season })

		return nil, searchOut{Command: cmd, Queued: queued}, nil
	})

	type calendarIn struct {
		Days        int  `json:"days,omitempty"                jsonschema:"how many days ahead, default 7"`
		PastDays    int  `json:"past_days,omitempty"           jsonschema:"how many days back to include as well, default 0"`
		Unmonitored bool `json:"include_unmonitored,omitempty" jsonschema:"include episodes Sonarr is not monitoring"`
	}
	type calendarRow struct {
		AirDateUTC string `json:"air_date_utc"`
		Series     string `json:"series"`
		SeriesID   int    `json:"series_id"`
		Episode    string `json:"episode"`
		Title      string `json:"title"`
		Network    string `json:"network,omitempty"`
		Monitored  bool   `json:"monitored"`
		HasFile    bool   `json:"has_file"`
	}
	type calendarOut struct {
		From     string        `json:"from"`
		To       string        `json:"to"`
		Episodes []calendarRow `json:"episodes"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "calendar_list",
		Description: "What airs soon: the episodes of the library's series airing in the next days (7 by default), optionally the last few days too, in air order, each saying whether Sonarr is monitoring it and has it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in calendarIn) (*mcp.CallToolResult, calendarOut, error) {
		now := time.Now().UTC()
		from := now.AddDate(0, 0, -in.PastDays)
		to := now.AddDate(0, 0, limitOr(in.Days, 7))
		res, err := client.GetCalendar(ctx, sonarr.GetCalendarOperationOptions{
			Start: from.Format(time.RFC3339), End: to.Format(time.RFC3339),
			Unmonitored: new(in.Unmonitored), IncludeSeries: new(true),
		})
		if err != nil {
			return nil, calendarOut{}, err
		}
		out := calendarOut{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
		for _, e := range res.Model {
			row := calendarRow{
				AirDateUTC: e.AirDateUtc, SeriesID: e.SeriesId, Episode: episodeLabel(e.SeasonNumber, e.EpisodeNumber),
				Title: e.Title, Monitored: boolv(e.Monitored), HasFile: boolv(e.HasFile),
			}
			if e.Series != nil {
				row.Series, row.Network = e.Series.Title, e.Series.Network
			}
			out.Episodes = append(out.Episodes, row)
		}
		slices.SortStableFunc(out.Episodes, func(a, b calendarRow) int { return strings.Compare(a.AirDateUTC, b.AirDateUTC) })

		return nil, out, nil
	})
}
