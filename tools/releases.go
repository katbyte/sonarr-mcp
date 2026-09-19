package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// releaseRow is one release an interactive search found, with Sonarr's
// verdict on it.
type releaseRow struct {
	GUID              string   `json:"guid"                    jsonschema:"with indexer_id, what release_grab takes"`
	IndexerID         int      `json:"indexer_id"`
	Indexer           string   `json:"indexer"`
	Title             string   `json:"title"`
	Episodes          string   `json:"episodes,omitempty"      jsonschema:"the episodes Sonarr maps it to, or the season for a pack"`
	Quality           string   `json:"quality"`
	Size              string   `json:"size"`
	AgeDays           int      `json:"age_days"`
	Protocol          string   `json:"protocol"`
	Seeders           int      `json:"seeders,omitempty"`
	Languages         []string `json:"languages"`
	ReleaseGroup      string   `json:"release_group,omitempty"`
	CustomFormatScore int      `json:"custom_format_score"`
	Approved          bool     `json:"approved"                jsonschema:"Sonarr would grab it itself"`
	Rejections        []string `json:"rejections"              jsonschema:"why Sonarr would not grab it, when it would not"`
}

func projectRelease(rel *sonarr.ReleaseResource) releaseRow {
	row := releaseRow{
		GUID: rel.Guid, IndexerID: rel.IndexerId, Indexer: rel.Indexer, Title: rel.Title, Quality: qualityName(rel.Quality),
		Size: humanSize(rel.Size), AgeDays: rel.Age, Protocol: string(rel.Protocol), Seeders: rel.Seeders,
		Languages: languageNames(rel.Languages), ReleaseGroup: rel.ReleaseGroup, CustomFormatScore: rel.CustomFormatScore,
		Approved: boolv(rel.Approved), Rejections: rel.Rejections,
	}
	switch {
	case boolv(rel.FullSeason):
		row.Episodes = fmt.Sprintf("season %d", rel.MappedSeasonNumber)
	case len(rel.MappedEpisodeNumbers) > 0:
		row.Episodes = episodeLabel(rel.MappedSeasonNumber, rel.MappedEpisodeNumbers...)
	case len(rel.EpisodeNumbers) > 0:
		row.Episodes = episodeLabel(rel.SeasonNumber, rel.EpisodeNumbers...)
	}

	return row
}

func registerReleaseTools(r *registry) {
	client := r.client

	type searchIn struct {
		Series  string `json:"series"            jsonschema:"the series: title, Sonarr id or tvdb:<id>"`
		Episode string `json:"episode,omitempty" jsonschema:"one episode, S01E02; or give season instead"`
		Season  *int   `json:"season,omitempty"  jsonschema:"a whole season, for packs and every episode in it"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"releases to return, default 25"`
	}
	type searchOut struct {
		Total    int          `json:"total"`
		Approved int          `json:"approved" jsonschema:"how many Sonarr would grab itself"`
		Releases []releaseRow `json:"releases" jsonschema:"approved first, best first, as Sonarr ranks them"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "release_search",
		Description: "Search the indexers for an episode or a season and show every release found, the ones Sonarr would grab first, with the reasons it rejects the rest: quality not wanted, blocklisted, too big, a custom format score too low, already have better. Grab one with release_grab. Queries the indexers but changes nothing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		s, err := r.resolveSeries(ctx, in.Series)
		if err != nil {
			return nil, searchOut{}, err
		}
		var opts sonarr.GetReleaseOperationOptions
		switch {
		case in.Episode != "" && in.Season != nil:
			return nil, searchOut{}, errors.New("give an episode or a season, not both")
		case in.Episode != "":
			var eps, picked []sonarr.EpisodeResource
			if eps, err = r.episodesOf(ctx, s.Id, nil, false); err != nil {
				return nil, searchOut{}, err
			}
			if picked, err = pickEpisodes(eps, []string{in.Episode}); err != nil {
				return nil, searchOut{}, err
			}
			opts.EpisodeId = picked[0].Id
		case in.Season != nil:
			opts.SeriesId, opts.SeasonNumber = s.Id, *in.Season
			if *in.Season == 0 {
				return nil, searchOut{}, errors.New("sonarr does not search the specials as a season; search their episodes one at a time")
			}
		default:
			return nil, searchOut{}, errors.New("give an episode (S01E02) or a season to search for")
		}
		res, err := client.GetRelease(ctx, opts)
		if err != nil {
			return nil, searchOut{}, err
		}
		rows := make([]releaseRow, 0, len(res.Model))
		for i := range res.Model {
			rows = append(rows, projectRelease(&res.Model[i]))
		}
		// Sonarr answers in its own preference order; approved first keeps
		// that order within each half
		slices.SortStableFunc(rows, func(a, b releaseRow) int {
			switch {
			case a.Approved == b.Approved:
				return 0
			case a.Approved:
				return -1
			default:
				return 1
			}
		})
		out := searchOut{Total: len(rows)}
		for _, row := range rows {
			if row.Approved {
				out.Approved++
			}
			if len(out.Releases) < limitOr(in.Limit, 25) {
				out.Releases = append(out.Releases, row)
			}
		}

		return nil, out, nil
	})

	type grabIn struct {
		GUID      string `json:"guid"       jsonschema:"the release's guid, from release_search"`
		IndexerID int    `json:"indexer_id" jsonschema:"the release's indexer_id, from release_search"`
	}
	type grabOut struct {
		Grabbed historyRow `json:"grabbed" jsonschema:"the grab as Sonarr recorded it: the release, its series and episode, the indexer and the download client"`
		Queued  []queueRow `json:"queued"  jsonschema:"the download queue for the series after the grab"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "release_grab",
		Description: "Send a release from release_search to the download client, even one Sonarr rejected (the choice is yours). Sonarr keeps a search's results for about half an hour; after that, search again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in grabIn) (*mcp.CallToolResult, grabOut, error) {
		if in.GUID == "" || in.IndexerID == 0 {
			return nil, grabOut{}, errors.New("guid and indexer_id are both required, from release_search")
		}
		// Sonarr answers a grab with the request echoed back, so what was
		// grabbed is read from the history entry it records, which carries
		// the release's guid
		since := time.Now().Add(-time.Minute).UTC()
		if _, err := client.PostRelease(ctx, sonarr.ReleaseResource{Guid: in.GUID, IndexerId: in.IndexerID}); err != nil {
			return nil, grabOut{}, err
		}
		recent, err := client.GetHistorySince(ctx, sonarr.GetHistorySinceOperationOptions{
			Date: since.Format(time.RFC3339), EventType: sonarr.EpisodeHistoryEventTypeGrabbed, IncludeSeries: new(true), IncludeEpisode: new(true),
		})
		if err != nil {
			return nil, grabOut{}, err
		}
		out := grabOut{Grabbed: historyRow{SourceTitle: in.GUID}}
		for i := range recent.Model {
			h := &recent.Model[i]
			if h.Data["guid"] != in.GUID {
				continue
			}
			out.Grabbed = projectHistory(h)
			if out.Queued, err = r.queueFor(ctx, h.SeriesId, nil); err != nil {
				return nil, out, err
			}
			break
		}

		return nil, out, nil
	})

	type parseIn struct {
		Title string `json:"title" jsonschema:"a release name or a file name, e.g. Firefly.S01E07.Jaynestown.1080p.WEB-DL-GRP"`
	}
	type parseOut struct {
		Title             string   `json:"title"`
		Series            string   `json:"series,omitempty"              jsonschema:"the series in the library it matches; empty when it matches none"`
		SeriesID          int      `json:"series_id,omitempty"`
		ParsedTitle       string   `json:"parsed_series_title,omitempty" jsonschema:"the series title Sonarr read out of the name"`
		Episodes          string   `json:"episodes,omitempty"`
		FullSeason        bool     `json:"full_season"`
		Special           bool     `json:"special"`
		Quality           string   `json:"quality"`
		Languages         []string `json:"languages"`
		ReleaseGroup      string   `json:"release_group,omitempty"`
		CustomFormats     []string `json:"custom_formats"`
		CustomFormatScore int      `json:"custom_format_score"`
		Understood        bool     `json:"understood"                    jsonschema:"Sonarr could read a series and episodes out of it"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "release_parse",
		Description: "What Sonarr reads out of a release or file name: which series in the library, which episodes, the quality, languages, release group and custom formats. The first question about a download that will not import or a release Sonarr keeps ignoring.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in parseIn) (*mcp.CallToolResult, parseOut, error) {
		if strings.TrimSpace(in.Title) == "" {
			return nil, parseOut{}, errors.New("title is required")
		}
		res, err := client.GetParse(ctx, sonarr.GetParseOperationOptions{Title: in.Title})
		if err != nil {
			return nil, parseOut{}, err
		}
		p := res.Model
		out := parseOut{Title: in.Title, CustomFormatScore: p.CustomFormatScore, Languages: languageNames(p.Languages)}
		if p.Series != nil {
			out.Series, out.SeriesID = p.Series.Title, p.Series.Id
		}
		for _, cf := range p.CustomFormats {
			out.CustomFormats = append(out.CustomFormats, cf.Name)
		}
		if info := p.ParsedEpisodeInfo; info != nil {
			out.Understood = true
			out.ParsedTitle, out.FullSeason, out.Special = info.SeriesTitle, boolv(info.FullSeason), boolv(info.Special)
			out.Quality, out.ReleaseGroup = qualityName(info.Quality), info.ReleaseGroup
			if len(out.Languages) == 0 {
				out.Languages = languageNames(info.Languages)
			}
			switch {
			case out.FullSeason:
				out.Episodes = fmt.Sprintf("season %d", info.SeasonNumber)
			case len(info.EpisodeNumbers) > 0:
				out.Episodes = episodeLabel(info.SeasonNumber, info.EpisodeNumbers...)
			case len(info.AbsoluteEpisodeNumbers) > 0:
				out.Episodes = fmt.Sprintf("absolute %v", info.AbsoluteEpisodeNumbers)
			case info.AirDate != "":
				out.Episodes = "aired " + info.AirDate
			}
		}

		return nil, out, nil
	})

	type wantedIn struct {
		Kind string `json:"kind"                   jsonschema:"missing: every monitored episode that has aired without a file; cutoff: every monitored file below its profile's cutoff"`
		Wait int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the search, default 60; -1 queues it and returns at once"`
	}
	type wantedOut struct {
		Command commandOut `json:"command"`
		Queue   int        `json:"queue_size" jsonschema:"downloads in the queue after the search"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "wanted_search",
		Description: "Search for everything Sonarr wants at once: every missing monitored episode (kind missing) or every file below its cutoff (kind cutoff), across the whole library. On a large library this runs for a long time and hits the indexers hard; audit_missing_episodes and audit_cutoff_unmet size it first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in wantedIn) (*mcp.CallToolResult, wantedOut, error) {
		var name string
		switch strings.ToLower(strings.TrimSpace(in.Kind)) {
		case "missing":
			name = "MissingEpisodeSearch"
		case "cutoff", "cutoff_unmet", "cutoffunmet":
			name = "CutoffUnmetEpisodeSearch"
		default:
			return nil, wantedOut{}, fmt.Errorf("kind must be missing or cutoff, got %q", in.Kind)
		}
		cmd, err := r.runCommand(ctx, name, map[string]any{"monitored": true}, waitFor(in.Wait))
		if err != nil {
			return nil, wantedOut{}, err
		}
		if err := r.refreshQueue(ctx); err != nil {
			return nil, wantedOut{}, err
		}
		queue, err := r.queueAll(ctx)
		if err != nil {
			return nil, wantedOut{}, err
		}

		return nil, wantedOut{Command: cmd, Queue: len(queue)}, nil
	})
}
