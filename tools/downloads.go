package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// queueRow is one download Sonarr is tracking.
type queueRow struct {
	ID             int      `json:"id"                        jsonschema:"the queue id, for taking it out of the queue or grabbing it early"`
	Series         string   `json:"series,omitempty"`
	SeriesID       int      `json:"series_id,omitempty"`
	Season         int      `json:"season"`
	Episode        string   `json:"episode,omitempty"`
	Title          string   `json:"title"                     jsonschema:"the release name"`
	Status         string   `json:"status"                    jsonschema:"what the download client says: queued, paused, downloading, completed, failed, warning, delay (held by a delay profile), downloadClientUnavailable"`
	State          string   `json:"state"                     jsonschema:"where Sonarr is with it: downloading, importPending, importBlocked, importing, imported, failedPending, failed, ignored"`
	Health         string   `json:"health"                    jsonschema:"ok, warning or error: whether Sonarr thinks it needs attention"`
	Messages       []string `json:"messages"                  jsonschema:"why it needs attention, as Sonarr words it"`
	Error          string   `json:"error,omitempty"`
	Progress       float64  `json:"progress_percent"`
	Size           string   `json:"size"`
	TimeLeft       string   `json:"time_left,omitempty"`
	Quality        string   `json:"quality"`
	Protocol       string   `json:"protocol"`
	DownloadClient string   `json:"download_client,omitempty"`
	Indexer        string   `json:"indexer,omitempty"`
	Added          string   `json:"added,omitempty"`
	OutputPath     string   `json:"output_path,omitempty"`
	DownloadID     string   `json:"download_id,omitempty"`
}

func projectQueue(q *sonarr.QueueResource) queueRow {
	row := queueRow{
		ID: q.Id, SeriesID: q.SeriesId, Season: q.SeasonNumber, Title: q.Title,
		Status: string(q.Status), State: string(q.TrackedDownloadState), Health: string(q.TrackedDownloadStatus),
		Error: q.ErrorMessage, Size: humanSize(int64(q.Size)), TimeLeft: q.Timeleft, Quality: qualityName(q.Quality),
		Protocol: string(q.Protocol), DownloadClient: q.DownloadClient, Indexer: q.Indexer, Added: q.Added,
		OutputPath: q.OutputPath, DownloadID: q.DownloadId,
	}
	if q.Size > 0 {
		row.Progress = math.Round((q.Size-q.Sizeleft)/q.Size*1000) / 10
	}
	if q.Series != nil {
		row.Series = q.Series.Title
	}
	if q.Episode != nil {
		row.Episode = episodeLabel(q.Episode.SeasonNumber, q.Episode.EpisodeNumber)
	}
	for _, m := range q.StatusMessages {
		for _, text := range m.Messages {
			if m.Title != "" && m.Title != q.Title {
				text = m.Title + ": " + text
			}
			row.Messages = append(row.Messages, text)
		}
		if len(m.Messages) == 0 && m.Title != "" {
			row.Messages = append(row.Messages, m.Title)
		}
	}

	return row
}

// queueAll reads the whole queue, with each item's series and episode.
func (r *registry) queueAll(ctx context.Context) ([]sonarr.QueueResource, error) {
	res, err := r.client.GetQueueComplete(ctx, sonarr.GetQueueOperationOptions{
		IncludeSeries: new(true), IncludeEpisode: new(true), IncludeUnknownSeriesItems: new(true),
	})
	if err != nil {
		return nil, err
	}

	return res.Items, nil
}

// queueWait is how long a tool that has just grabbed something waits for
// Sonarr to check the download clients.
const queueWait = 30 * time.Second

// refreshQueue has Sonarr check its download clients now. A grab reaches
// the queue only when Sonarr next checks them - a few seconds after the
// grab, or a minute on its own schedule - so a tool that has just grabbed
// and is about to report the queue asks for the check first.
func (r *registry) refreshQueue(ctx context.Context) error {
	_, err := r.runCommand(ctx, "RefreshMonitoredDownloads", nil, queueWait)

	return err
}

// queueFor is what the queue holds for a series, or for some of its
// episodes, once Sonarr has checked the download clients for what was just
// grabbed.
func (r *registry) queueFor(ctx context.Context, seriesID int, episodeIDs []int) ([]queueRow, error) {
	if err := r.refreshQueue(ctx); err != nil {
		return nil, err
	}
	res, err := r.client.GetQueueDetails(ctx, sonarr.GetQueueDetailsOperationOptions{
		SeriesId: seriesID, IncludeSeries: new(true), IncludeEpisode: new(true),
	})
	if err != nil {
		return nil, err
	}
	var out []queueRow
	for i := range res.Model {
		q := &res.Model[i]
		if len(episodeIDs) > 0 && !slices.Contains(episodeIDs, q.EpisodeId) {
			continue
		}
		out = append(out, projectQueue(q))
	}

	return out, nil
}

// historyEvents are the history event names a caller filters by, and the
// ids the history endpoint takes for them.
var historyEvents = map[string]int{
	"grabbed": 1, "seriesfolderimported": 2, "downloadfolderimported": 3, "downloadfailed": 4,
	"episodefiledeleted": 5, "episodefilerenamed": 6, "downloadignored": 7,
}

// historyAliases are the shorter names a caller is likely to use.
var historyAliases = map[string][]int{
	"imported": {2, 3}, "failed": {4}, "deleted": {5}, "renamed": {6}, "ignored": {7},
}

func historyEventIDs(names []string) ([]int, error) {
	var out []int
	for _, n := range names {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(n), "_", ""))
		if id, ok := historyEvents[key]; ok {
			out = append(out, id)
			continue
		}
		if ids, ok := historyAliases[key]; ok {
			out = append(out, ids...)
			continue
		}
		return nil, fmt.Errorf("unknown event %q: use grabbed, imported, failed, deleted, renamed or ignored", n)
	}

	return out, nil
}

// historyRow is one thing that happened to an episode.
type historyRow struct {
	ID          int    `json:"id"                        jsonschema:"the history id history_mark_failed takes"`
	Date        string `json:"date"`
	Event       string `json:"event"`
	Series      string `json:"series,omitempty"`
	Episode     string `json:"episode,omitempty"`
	SourceTitle string `json:"source_title"              jsonschema:"the release or file name"`
	Quality     string `json:"quality"`
	Detail      string `json:"detail,omitempty"          jsonschema:"why it failed or was deleted, or where it was imported from and to"`
	Indexer     string `json:"indexer,omitempty"`
	Client      string `json:"download_client,omitempty"`
	DownloadID  string `json:"download_id,omitempty"`
}

func projectHistory(h *sonarr.HistoryResource) historyRow {
	row := historyRow{
		ID: h.Id, Date: h.Date, Event: string(h.EventType), SourceTitle: h.SourceTitle, Quality: qualityName(h.Quality),
		Indexer: h.Data["indexer"], Client: h.Data["downloadClientName"], DownloadID: h.DownloadId,
	}
	// downloadClient is the client's kind (SABnzbd), downloadClientName the
	// one its owner named; older entries have only the kind
	if row.Client == "" {
		row.Client = h.Data["downloadClient"]
	}
	if h.Series != nil {
		row.Series = h.Series.Title
	}
	if h.Episode != nil {
		row.Episode = episodeLabel(h.Episode.SeasonNumber, h.Episode.EpisodeNumber)
	}
	switch {
	case h.Data["message"] != "":
		row.Detail = h.Data["message"]
	case h.Data["reason"] != "":
		row.Detail = h.Data["reason"]
	case h.Data["importedPath"] != "":
		row.Detail = strings.TrimSpace(h.Data["droppedPath"] + " -> " + h.Data["importedPath"])
	}

	return row
}

// blocklistRow is a release Sonarr will not grab again.
type blocklistRow struct {
	ID          int    `json:"id"                jsonschema:"the blocklist id blocklist_remove takes"`
	Date        string `json:"date"`
	Series      string `json:"series,omitempty"`
	SourceTitle string `json:"source_title"`
	Quality     string `json:"quality"`
	Indexer     string `json:"indexer,omitempty"`
	Protocol    string `json:"protocol"`
	Message     string `json:"message,omitempty" jsonschema:"why it was blocklisted"`
}

func registerQueueTools(r *registry) {
	client := r.client

	type listIn struct {
		Series string `json:"series,omitempty"      jsonschema:"only this series: title, Sonarr id or tvdb:<id>"`
		Issues bool   `json:"issues_only,omitempty" jsonschema:"only the downloads that need a person: failed, paused, unable to import, warned about, or waiting too long"`
		Limit  int    `json:"limit,omitempty"       jsonschema:"downloads to return, default 100"`
	}
	type listOut struct {
		Total     int        `json:"total"`
		Downloads []queueRow `json:"downloads"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "queue_list",
		Description: "The download queue: every download Sonarr is tracking, its progress and time left, what the download client says, and where Sonarr is with it - downloading, waiting to import, blocked from importing, failed - with Sonarr's own words on any that need attention.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		seriesID := 0
		if in.Series != "" {
			s, err := r.resolveSeries(ctx, in.Series)
			if err != nil {
				return nil, listOut{}, err
			}
			seriesID = s.Id
		}
		all, err := r.queueAll(ctx)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{}
		now := time.Now()
		for i := range all {
			q := &all[i]
			if seriesID != 0 && q.SeriesId != seriesID {
				continue
			}
			// needing attention is what the stuck downloads audit says it is,
			// so the two never disagree about a download
			if problem, _ := stuckProblem(q, now.Sub(parseTime(q.Added))); in.Issues && problem == "" {
				continue
			}
			out.Total++
			if len(out.Downloads) < limitOr(in.Limit, 100) {
				out.Downloads = append(out.Downloads, projectQueue(q))
			}
		}

		return nil, out, nil
	})

	type removeIn struct {
		IDs              []int `json:"ids"                          jsonschema:"the queue ids to remove"`
		RemoveFromClient *bool `json:"remove_from_client,omitempty" jsonschema:"also remove the download, and its data, from the download client; default true"`
		Blocklist        bool  `json:"blocklist,omitempty"          jsonschema:"blocklist the release so Sonarr never grabs it again, and search for another"`
		SkipRedownload   bool  `json:"skip_redownload,omitempty"    jsonschema:"with blocklist: do not search for a replacement"`
	}
	type removeOut struct {
		Removed []string `json:"removed" jsonschema:"the releases taken out of the queue"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "queue_remove",
		Description: "Take downloads out of Sonarr's queue - the stuck, failed or unwanted ones audit_stuck_downloads finds - removing them from the download client too unless told not to, and optionally blocklisting the release so Sonarr searches for a different one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		if len(in.IDs) == 0 {
			return nil, removeOut{}, errors.New("name the queue ids to remove")
		}
		all, err := r.queueAll(ctx)
		if err != nil {
			return nil, removeOut{}, err
		}
		titles := map[int]string{}
		for _, q := range all {
			titles[q.Id] = q.Title
		}
		fromClient := in.RemoveFromClient == nil || *in.RemoveFromClient
		out := removeOut{}
		for _, id := range in.IDs {
			title, ok := titles[id]
			if !ok {
				return nil, out, fmt.Errorf("no download %d in the queue", id)
			}
			if _, err := client.DeleteQueueById(ctx, id, sonarr.DeleteQueueByIdOperationOptions{
				RemoveFromClient: new(fromClient), Blocklist: new(in.Blocklist), SkipRedownload: new(in.SkipRedownload),
			}); err != nil {
				return nil, out, err
			}
			out.Removed = append(out.Removed, title)
		}

		return nil, out, nil
	})

	type grabIn struct {
		IDs []int `json:"ids" jsonschema:"the queue ids of downloads a delay profile is holding (status delay)"`
	}
	type grabOut struct {
		Grabbed []string `json:"grabbed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "queue_grab",
		Description: "Send downloads a delay profile is holding back (queue status delay) to the download client now, rather than when the delay runs out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in grabIn) (*mcp.CallToolResult, grabOut, error) {
		if len(in.IDs) == 0 {
			return nil, grabOut{}, errors.New("name the queue ids to grab")
		}
		all, err := r.queueAll(ctx)
		if err != nil {
			return nil, grabOut{}, err
		}
		out := grabOut{}
		for _, id := range in.IDs {
			i := slices.IndexFunc(all, func(q sonarr.QueueResource) bool { return q.Id == id })
			switch {
			case i < 0:
				return nil, out, fmt.Errorf("no download %d in the queue", id)
			case all[i].Status != sonarr.QueueStatusDelay:
				return nil, out, fmt.Errorf("%s is %s, not held by a delay profile; only a delayed download can be grabbed early", all[i].Title, all[i].Status)
			}
			if _, err := client.PostQueueGrabById(ctx, id); err != nil {
				return nil, out, err
			}
			out.Grabbed = append(out.Grabbed, all[i].Title)
		}

		return nil, out, nil
	})
}

func registerHistoryTools(r *registry) {
	client := r.client

	type listIn struct {
		Series  string   `json:"series,omitempty"  jsonschema:"only this series: title, Sonarr id or tvdb:<id>"`
		Episode string   `json:"episode,omitempty" jsonschema:"with series: only this episode, S01E02"`
		Events  []string `json:"events,omitempty"  jsonschema:"only these events: grabbed, imported, failed, deleted, renamed, ignored"`
		Days    int      `json:"days,omitempty"    jsonschema:"only the last this many days"`
		Limit   int      `json:"limit,omitempty"   jsonschema:"entries to return, newest first, default 50"`
	}
	type listOut struct {
		Total   int          `json:"total"   jsonschema:"entries matching, of which the newest are returned"`
		Entries []historyRow `json:"entries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "history_list",
		Description: "What has happened to the library's episodes, newest first: releases grabbed, imported, failed, deleted, renamed or ignored, with the release name, quality, indexer, and why a failure failed. Filter by series, episode, event and age.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		events, err := historyEventIDs(in.Events)
		if err != nil {
			return nil, listOut{}, err
		}
		opts := sonarr.GetHistoryOperationOptions{
			SortKey: "date", SortDirection: sonarr.SortDirectionDescending, IncludeSeries: new(true), IncludeEpisode: new(true),
			EventType: events,
		}
		if in.Series != "" {
			s, err := r.resolveSeries(ctx, in.Series)
			if err != nil {
				return nil, listOut{}, err
			}
			opts.SeriesIds = []int{s.Id}
			if in.Episode != "" {
				eps, err := r.episodesOf(ctx, s.Id, nil, false)
				if err != nil {
					return nil, listOut{}, err
				}
				picked, err := pickEpisodes(eps, []string{in.Episode})
				if err != nil {
					return nil, listOut{}, err
				}
				opts.EpisodeId = picked[0].Id
			}
		} else if in.Episode != "" {
			return nil, listOut{}, errors.New("an episode needs its series")
		}
		limit := limitOr(in.Limit, 50)
		var cutoff time.Time
		if in.Days > 0 {
			cutoff = time.Now().AddDate(0, 0, -in.Days)
		}
		out := listOut{}
		// newest first, so paging stops at the first entry older than the cutoff
		for page := 1; ; page++ {
			opts.Page, opts.PageSize = page, max(limit, 100)
			res, err := client.GetHistory(ctx, opts)
			if err != nil {
				return nil, listOut{}, err
			}
			done := len(res.Model.Records) < opts.PageSize
			for i := range res.Model.Records {
				h := &res.Model.Records[i]
				if !cutoff.IsZero() && parseTime(h.Date).Before(cutoff) {
					done = true
					break
				}
				out.Total++
				if len(out.Entries) < limit {
					out.Entries = append(out.Entries, projectHistory(h))
				}
			}
			if done || cutoff.IsZero() {
				if cutoff.IsZero() {
					out.Total = res.Model.TotalRecords
				}
				break
			}
		}

		return nil, out, nil
	})

	type failIn struct {
		ID int `json:"history_id" jsonschema:"the id of the grabbed event, from history_list"`
	}
	type failOut struct {
		Failed historyRow `json:"marked_failed" jsonschema:"the history entry marked"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "history_mark_failed",
		Description: "Mark a grabbed release as failed: Sonarr blocklists it and, with failed download handling on, searches for another. For a download that completed but is bad - wrong episode, fake, unplayable - that the download client never failed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in failIn) (*mcp.CallToolResult, failOut, error) {
		if in.ID <= 0 {
			return nil, failOut{}, errors.New("history_id is required")
		}
		entry, err := r.historyEntry(ctx, in.ID)
		if err != nil {
			return nil, failOut{}, err
		}
		if _, err := client.PostHistoryFailedById(ctx, in.ID); err != nil {
			return nil, failOut{}, err
		}

		return nil, failOut{Failed: projectHistory(entry)}, nil
	})

	type blockIn struct {
		Series string `json:"series,omitempty" jsonschema:"only this series: title, Sonarr id or tvdb:<id>"`
		Limit  int    `json:"limit,omitempty"  jsonschema:"entries to return, newest first, default 100"`
	}
	type blockOut struct {
		Total   int            `json:"total"`
		Entries []blocklistRow `json:"entries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "blocklist_list",
		Description: "The releases Sonarr will not grab again - failed downloads and releases removed with blocklisting - newest first, with why each was blocklisted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in blockIn) (*mcp.CallToolResult, blockOut, error) {
		opts := sonarr.GetBlocklistOperationOptions{SortKey: "date", SortDirection: sonarr.SortDirectionDescending}
		if in.Series != "" {
			s, err := r.resolveSeries(ctx, in.Series)
			if err != nil {
				return nil, blockOut{}, err
			}
			opts.SeriesIds = []int{s.Id}
		}
		res, err := client.GetBlocklistComplete(ctx, opts)
		if err != nil {
			return nil, blockOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, blockOut{}, err
		}
		titles := map[int]string{}
		for i := range series {
			titles[series[i].Id] = series[i].Title
		}
		out := blockOut{Total: len(res.Items)}
		for _, b := range res.Items {
			if len(out.Entries) >= limitOr(in.Limit, 100) {
				break
			}
			out.Entries = append(out.Entries, blocklistRow{
				ID: b.Id, Date: b.Date, Series: titles[b.SeriesId], SourceTitle: b.SourceTitle, Quality: qualityName(b.Quality),
				Indexer: b.Indexer, Protocol: string(b.Protocol), Message: b.Message,
			})
		}

		return nil, out, nil
	})

	type unblockIn struct {
		IDs []int `json:"ids" jsonschema:"the blocklist ids to remove"`
	}
	type unblockOut struct {
		Removed int `json:"removed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "blocklist_remove",
		Description: "Take releases off the blocklist, so Sonarr may grab them again: for a release blocklisted by mistake, or one whose failure was the download client's, not the release's.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unblockIn) (*mcp.CallToolResult, unblockOut, error) {
		if len(in.IDs) == 0 {
			return nil, unblockOut{}, errors.New("name the blocklist ids to remove")
		}
		out := unblockOut{}
		for _, id := range in.IDs {
			if _, err := client.DeleteBlocklistById(ctx, id); err != nil {
				return nil, out, err
			}
			out.Removed++
		}

		return nil, out, nil
	})
}

// historyScan is how many recent history entries historyEntry looks through:
// Sonarr has no read of one entry by id, and the entries a caller has just
// been shown are the recent ones.
const historyScan = 2000

// historyEntry finds a history entry by id among the recent ones.
func (r *registry) historyEntry(ctx context.Context, id int) (*sonarr.HistoryResource, error) {
	const size = 250
	for page := 1; page*size <= historyScan; page++ {
		res, err := r.client.GetHistory(ctx, sonarr.GetHistoryOperationOptions{
			Page: page, PageSize: size, SortKey: "date", SortDirection: sonarr.SortDirectionDescending,
			IncludeSeries: new(true), IncludeEpisode: new(true),
		})
		if err != nil {
			return nil, err
		}
		for i := range res.Model.Records {
			if res.Model.Records[i].Id == id {
				return &res.Model.Records[i], nil
			}
		}
		if len(res.Model.Records) < size {
			break
		}
	}

	return nil, fmt.Errorf("no history entry %d among the last %d", id, historyScan)
}
