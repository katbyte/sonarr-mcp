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

	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// episodeRow is an episode as a list reads it.
type episodeRow struct {
	ID        sonarr.EpisodeID `json:"id"`
	Episode   string           `json:"episode"            jsonschema:"S01E02"`
	Absolute  int              `json:"absolute,omitempty" jsonschema:"the absolute number, for anime"`
	Title     string           `json:"title"`
	AirDate   string           `json:"air_date,omitempty"`
	Aired     bool             `json:"aired"`
	Monitored bool             `json:"monitored"`
	HasFile   bool             `json:"has_file"`
	File      *fileRef         `json:"file,omitempty"`
}

// fileRef is the file an episode has, in brief.
type fileRef struct {
	ID           sonarr.EpisodeFileID `json:"id"`
	Quality      string               `json:"quality"`
	Size         string               `json:"size"`
	Languages    []string             `json:"languages"`
	CutoffNotMet bool                 `json:"below_cutoff"  jsonschema:"the file's quality is below what the profile wants, so Sonarr would upgrade it"`
	RelativePath string               `json:"relative_path"`
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
		Title: e.Title, AirDate: day(e.AirDate.Time), Aired: aired(e, now), Monitored: boolv(e.Monitored), HasFile: boolv(e.HasFile),
		File: projectFileRef(e.EpisodeFile),
	}
}

// episodesOf reads a series' episodes, every season or one.
func (r *registry) episodesOf(ctx context.Context, seriesID sonarr.SeriesID, season *int, files bool) ([]sonarr.EpisodeResource, error) {
	// season 0 is the specials, and is asked for like any other
	res, err := r.client.GetEpisode(ctx, sonarr.GetEpisodeOptions{SeriesId: seriesID, SeasonNumber: season, IncludeEpisodeFile: new(files)})
	if err != nil {
		return nil, err
	}
	eps := res.Answer
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
			i = slices.IndexFunc(eps, func(e sonarr.EpisodeResource) bool { return e.Id == sonarr.EpisodeID(id) })
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

// noSeason refuses a season a series does not have, naming the ones it does.
func noSeason(s *sonarr.SeriesResource, season int) error {
	have := make([]string, 0, len(s.Seasons))
	for _, sn := range s.Seasons {
		have = append(have, strconv.Itoa(sn.SeasonNumber))
	}

	return fmt.Errorf("%s has no season %d (it has: %s)", s.Title, season, strings.Join(have, ", "))
}

func episodeIDs(eps []sonarr.EpisodeResource) []sonarr.EpisodeID {
	out := make([]sonarr.EpisodeID, 0, len(eps))
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
		pageIn
	}
	type listOut struct {
		Series string `json:"series"`
		pageOut
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
		rows := make([]episodeRow, 0, len(eps))
		for i := range eps {
			row := projectEpisode(&eps[i], now)
			if in.Missing && (row.HasFile || !row.Monitored || !row.Aired) {
				continue
			}
			rows = append(rows, row)
		}
		out := listOut{Series: s.Title}
		out.Episodes, out.pageOut = pageRows(rows, in.pageIn)

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
		res, err := client.PutEpisodeMonitor(ctx, sonarr.EpisodesMonitoredResource{EpisodeIds: episodeIDs(picked), Monitored: new(in.Monitored)})
		if err != nil {
			return nil, monitorOut{}, err
		}
		now := time.Now()
		out := monitorOut{}
		for i := range res.Answer {
			out.Episodes = append(out.Episodes, projectEpisode(&res.Answer[i], now))
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

		// the whole series is sent back with the seasons changed, so another
		// edit of it in the same turn waits, and the record is read once the
		// series is this call's alone
		unlock := r.locking().By(seriesLock(s.Id))
		defer unlock()

		cur, err := client.GetSeriesById(ctx, s.Id)
		if err != nil {
			return nil, seasonMonitorOut{}, err
		}
		body, err := answered(cur.Answer, s.Title)
		if err != nil {
			return nil, seasonMonitorOut{}, err
		}
		out := seasonMonitorOut{Series: body.Title}
		var settled []int
		write := false
		for _, want := range in.Seasons {
			i := slices.IndexFunc(body.Seasons, func(season sonarr.SeasonResource) bool { return season.SeasonNumber == want })
			if i < 0 {
				return nil, seasonMonitorOut{}, noSeason(&body, want)
			}
			if boolv(body.Seasons[i].Monitored) == in.Monitored {
				settled = append(settled, want)
				continue
			}
			body.Seasons[i].Monitored = new(in.Monitored)
			out.Changed = append(out.Changed, fmt.Sprintf("season %d: monitored %v", want, in.Monitored))
			write = true
		}

		// a season's flag is its own, not a count of its episodes: episode_monitor
		// changes episodes and leaves it be, so a season can say unmonitored
		// with monitored episodes in it, which Sonarr goes on searching for.
		// A season whose flag is already as asked is not therefore done, and
		// Sonarr only sets the episodes of a season whose flag changed, so
		// those episodes are set here.
		var stragglers []sonarr.EpisodeID
		if len(settled) > 0 {
			eps, err := r.episodesOf(ctx, s.Id, nil, false)
			if err != nil {
				return nil, seasonMonitorOut{}, err
			}
			for _, season := range settled {
				n := len(stragglers)
				for _, e := range eps {
					if e.SeasonNumber == season && boolv(e.Monitored) != in.Monitored {
						stragglers = append(stragglers, e.Id)
					}
				}
				if n = len(stragglers) - n; n > 0 {
					out.Changed = append(out.Changed, fmt.Sprintf("season %d: %s monitored %v, which the season already said it was", season, countOf(n, "episode"), in.Monitored))
				}
			}
		}

		// the series' own flag follows: monitoring a season of an unmonitored
		// series would otherwise do nothing
		if in.Monitored && !boolv(body.Monitored) {
			body.Monitored = new(true)
			out.Changed = append(out.Changed, "series: monitored true")
			write = true
		}

		// Sonarr sets every episode of a season whose flag changed to match
		// it (SeriesService.UpdateSeries), as its own season toggle does
		if write {
			if _, err := client.PutSeriesById(ctx, body.Id, body); err != nil {
				return nil, seasonMonitorOut{}, err
			}
		}
		if len(stragglers) > 0 {
			if _, err := client.PutEpisodeMonitor(ctx, sonarr.EpisodesMonitoredResource{EpisodeIds: stragglers, Monitored: new(in.Monitored)}); err != nil {
				return nil, out, err
			}
		}

		return nil, out, nil
	})

	type seasonBulkIn struct {
		Series    []string `json:"series"    jsonschema:"the series, each a title, Sonarr id or tvdb:<id>"`
		Seasons   []int    `json:"seasons"   jsonschema:"the season numbers, the same for every series; 0 is the specials. A series without one of them is refused"`
		Monitored bool     `json:"monitored" jsonschema:"true to monitor the seasons and every episode in them, false to stop"`
	}
	type seasonBulkRow struct {
		Series  string `json:"series"`
		Seasons []int  `json:"seasons" jsonschema:"the seasons set as asked"`
	}
	type seasonBulkOut struct {
		Series []seasonBulkRow `json:"series"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "season_bulk_monitor",
		Description: "Monitor or unmonitor the same seasons across many series in one call, the way Sonarr's Season Pass page does: season 1 of every show listed, or the specials of all of them. season_monitor does one series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seasonBulkIn) (*mcp.CallToolResult, seasonBulkOut, error) {
		if len(in.Series) == 0 || len(in.Seasons) == 0 {
			return nil, seasonBulkOut{}, errors.New("name the series and the seasons")
		}
		pass := sonarr.SeasonPassResource{}
		out := seasonBulkOut{Series: make([]seasonBulkRow, 0, len(in.Series))}
		for _, ref := range in.Series {
			s, err := r.resolveSeries(ctx, ref)
			if err != nil {
				return nil, seasonBulkOut{}, err
			}
			one := sonarr.SeasonPassSeriesResource{Id: s.Id}
			for _, n := range in.Seasons {
				if !slices.ContainsFunc(s.Seasons, func(season sonarr.SeasonResource) bool { return season.SeasonNumber == n }) {
					return nil, seasonBulkOut{}, noSeason(s, n)
				}
				one.Seasons = append(one.Seasons, sonarr.SeasonResource{SeasonNumber: n, Monitored: new(in.Monitored)})
			}
			pass.Series = append(pass.Series, one)
			out.Series = append(out.Series, seasonBulkRow{Series: s.Title, Seasons: in.Seasons})
		}
		if _, err := client.PostSeasonPass(ctx, pass); err != nil {
			return nil, seasonBulkOut{}, err
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
		sent, err := client.EpisodeSearch(ctx, sonarr.EpisodeSearchCommand{EpisodeIds: ids})
		cmd, err := r.settleCommand(ctx, "EpisodeSearch", mcpregistry.WaitFor(in.Wait, commandWait), sent, err)
		if err != nil {
			return nil, searchOut{}, err
		}
		queued, err := r.queueFor(ctx, s.Id, ids, in.Wait >= 0)
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
		// Sonarr takes a search for a season the series does not have and
		// reports it done, having searched for nothing
		if !slices.ContainsFunc(s.Seasons, func(season sonarr.SeasonResource) bool { return season.SeasonNumber == in.Season }) {
			return nil, searchOut{}, noSeason(s, in.Season)
		}
		sent, err := client.SeasonSearch(ctx, sonarr.SeasonSearchCommand{SeriesId: s.Id, SeasonNumber: new(in.Season)})
		cmd, err := r.settleCommand(ctx, "SeasonSearch", mcpregistry.WaitFor(in.Wait, commandWait), sent, err)
		if err != nil {
			return nil, searchOut{}, err
		}
		queued, err := r.queueFor(ctx, s.Id, nil, in.Wait >= 0)
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
		pageIn
	}
	type calendarRow struct {
		AirDateUTC string          `json:"air_date_utc"`
		Series     string          `json:"series"`
		SeriesID   sonarr.SeriesID `json:"series_id"`
		Episode    string          `json:"episode"`
		Title      string          `json:"title"`
		Network    string          `json:"network,omitempty"`
		Monitored  bool            `json:"monitored"`
		HasFile    bool            `json:"has_file"`
	}
	type calendarOut struct {
		From string `json:"from"`
		To   string `json:"to"`
		pageOut
		Episodes []calendarRow `json:"episodes"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "calendar_list",
		Description: "What airs soon: the episodes of the library's series airing in the next days (7 by default), optionally the last few days too, in air order, each saying whether Sonarr is monitoring it and has it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in calendarIn) (*mcp.CallToolResult, calendarOut, error) {
		now := time.Now().UTC()
		from := now.AddDate(0, 0, -in.PastDays)
		to := now.AddDate(0, 0, mcpregistry.LimitOr(in.Days, 7))
		res, err := client.GetCalendar(ctx, sonarr.GetCalendarOptions{
			Start: sonarr.Time{Time: from}, End: sonarr.Time{Time: to},
			Unmonitored: new(in.Unmonitored), IncludeSeries: new(true),
		})
		if err != nil {
			return nil, calendarOut{}, err
		}
		rows := make([]calendarRow, 0, len(res.Answer))
		for _, e := range res.Answer {
			row := calendarRow{
				AirDateUTC: stamp(e.AirDateUtc.Time), SeriesID: e.SeriesId, Episode: episodeLabel(e.SeasonNumber, e.EpisodeNumber),
				Title: e.Title, Monitored: boolv(e.Monitored), HasFile: boolv(e.HasFile),
			}
			if e.Series != nil {
				row.Series, row.Network = e.Series.Title, e.Series.Network
			}
			rows = append(rows, row)
		}
		slices.SortStableFunc(rows, func(a, b calendarRow) int { return strings.Compare(a.AirDateUTC, b.AirDateUTC) })
		out := calendarOut{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
		out.Episodes, out.pageOut = pageRows(rows, in.pageIn)

		return nil, out, nil
	})
}
