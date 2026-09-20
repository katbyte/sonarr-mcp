package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

const (
	// importWaitHours is how long a finished download may wait to import
	// before it counts as stuck: Sonarr checks the clients every minute, so
	// an hour is sixty chances.
	importWaitHours = 1
	// notStartingHours is how long a download may sit queued in the client
	// without a byte arriving before it counts as stuck.
	notStartingHours = 24
	// failedDays is how far back audit_failed_downloads looks.
	failedDays = 30
	// lostGrabHours is how long a grab may go without Sonarr hearing of it
	// again before it counts as lost.
	lostGrabHours = 6
)

// auditStuckDownloads looks at every download in the queue for the ones a
// person has to deal with.
func (r *registry) auditStuckDownloads(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	queue, err := r.queueAll(ctx)
	if err != nil {
		return auditOut{}, err
	}
	now := time.Now()
	out := auditOut{}
	for i := range queue {
		q := &queue[i]
		if s.only() != 0 && q.SeriesId != s.only() {
			continue
		}
		out.Scanned++
		row := projectQueue(q)
		age := now.Sub(parseTime(q.Added))
		problem, fix := stuckProblem(q, age)
		if problem == "" {
			continue
		}
		subject := row.Title
		if row.Episode != "" {
			subject = row.Episode + " " + row.Title
		}
		detail := fmt.Sprintf("queue id %d, %s/%s", q.Id, q.Status, q.TrackedDownloadState)
		if age > 0 && !parseTime(q.Added).IsZero() {
			detail += fmt.Sprintf(", added %s ago", roundAge(age))
		}
		if len(row.Messages) > 0 {
			detail += ": " + strings.Join(row.Messages, "; ")
		} else if q.ErrorMessage != "" {
			detail += ": " + q.ErrorMessage
		}
		out.report(limit, finding{Series: row.Series, SeriesID: q.SeriesId, Subject: subject, Problem: problem, Detail: detail, Fix: fix})
	}

	return out, nil
}

// stuckProblem says what, if anything, is wrong with a download, and what to
// do about it.
func stuckProblem(q *sonarr.QueueResource, age time.Duration) (problem, fix string) {
	remove := "queue_remove with blocklist, so Sonarr grabs a different release"
	switch {
	// a finished download Sonarr has tried to import and could not: blocked
	// outright, or left pending with a warning (a sample, nothing it can
	// match), which is where Sonarr 4 leaves most of them
	case q.TrackedDownloadState == sonarr.TrackedDownloadStateImportBlocked,
		q.TrackedDownloadState == sonarr.TrackedDownloadStateImportPending && q.TrackedDownloadStatus != sonarr.TrackedDownloadStatusOk:
		return problemCannotImport, "import_scan its output path to see why, then import_apply naming the series and episodes; or " + remove
	case q.TrackedDownloadState == sonarr.TrackedDownloadStateFailedPending, q.TrackedDownloadState == sonarr.TrackedDownloadStateFailed,
		q.Status == sonarr.QueueStatusFailed:
		return problemDownloadFailed, remove
	case q.TrackedDownloadState == sonarr.TrackedDownloadStateImportPending && age > importWaitHours*time.Hour:
		return problemWaitingToImport, "import_scan its output path, then import_apply"
	case q.Status == sonarr.QueueStatusDownloadClientUnavailable:
		return problemClientUnreachable, "downloadclient_test"
	case q.Status == sonarr.QueueStatusPaused:
		return problemPausedInClient, "resume it in the client, or " + remove
	case q.TrackedDownloadStatus == sonarr.TrackedDownloadStatusError:
		return problemDownloadError, remove
	case q.TrackedDownloadStatus == sonarr.TrackedDownloadStatusWarning, q.Status == sonarr.QueueStatusWarning:
		return problemDownloadWarning, "read the messages; " + remove
	case q.Status == sonarr.QueueStatusQueued && q.Size > 0 && q.Sizeleft >= q.Size && age > notStartingHours*time.Hour:
		return problemNotStarting, "check the download client, or " + remove
	}

	return "", ""
}

// roundAge renders a duration the way a person says how long ago something
// was.
func roundAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// auditFailedDownloads reads the last month of history for failures that
// repeat, and for grabs Sonarr never heard of again.
func (r *registry) auditFailedDownloads(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	since := time.Now().AddDate(0, 0, -failedDays)
	res, err := r.client.GetHistorySince(ctx, sonarr.GetHistorySinceOperationOptions{
		Date: since.UTC().Format(time.RFC3339), IncludeSeries: new(true), IncludeEpisode: new(true),
	})
	if err != nil {
		return auditOut{}, err
	}
	queue, err := r.queueAll(ctx)
	if err != nil {
		return auditOut{}, err
	}
	inQueue := map[string]bool{}
	for _, q := range queue {
		if q.DownloadId != "" {
			inQueue[strings.ToUpper(q.DownloadId)] = true
		}
	}

	type failures struct {
		series, episode string
		seriesID        int
		count           int
		titles          []string
		last            string
	}
	byEpisode := map[int]*failures{}
	var order []int
	heard := map[string]bool{} // download ids Sonarr imported, failed or ignored
	var grabs []sonarr.HistoryResource
	for _, h := range res.Model {
		if !s.covers(h.SeriesId) {
			continue
		}
		id := strings.ToUpper(h.DownloadId)
		switch h.EventType {
		case sonarr.EpisodeHistoryEventTypeDownloadFailed:
			heard[id] = true
			f := byEpisode[h.EpisodeId]
			if f == nil {
				f = &failures{seriesID: h.SeriesId}
				if h.Series != nil {
					f.series = h.Series.Title
				}
				if h.Episode != nil {
					f.episode = episodeLabel(h.Episode.SeasonNumber, h.Episode.EpisodeNumber)
				}
				byEpisode[h.EpisodeId] = f
				order = append(order, h.EpisodeId)
			}
			f.count++
			if !slices.Contains(f.titles, h.SourceTitle) {
				f.titles = append(f.titles, h.SourceTitle)
			}
			if msg := h.Data["message"]; msg != "" {
				f.last = msg
			}
		case sonarr.EpisodeHistoryEventTypeDownloadFolderImported, sonarr.EpisodeHistoryEventTypeDownloadIgnored:
			heard[id] = true
		case sonarr.EpisodeHistoryEventTypeGrabbed:
			grabs = append(grabs, h)
		default:
		}
	}

	out := auditOut{Scanned: len(res.Model)}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := byEpisode[b].count - byEpisode[a].count; c != 0 {
			return c
		}
		return strings.Compare(byEpisode[a].series+byEpisode[a].episode, byEpisode[b].series+byEpisode[b].episode)
	})
	for _, id := range order {
		f := byEpisode[id]
		problem := problemDownloadFailed
		if f.count > 1 {
			problem = problemRepeatedFailures
		}
		detail := fmt.Sprintf("%d failed in %d days: %s", f.count, failedDays, shortList(f.titles, 4))
		if f.last != "" {
			detail += "; last: " + f.last
		}
		out.report(limit, finding{
			Series: f.series, SeriesID: f.seriesID, Subject: f.episode, Problem: problem, Detail: detail,
			Fix: "release_search for the episode and grab a release from a different group or indexer",
		})
	}

	// a grab is lost when nothing more was heard of its download: no import,
	// no failure, and not in the queue - the download client dropped it, or
	// someone removed it there, and Sonarr stopped tracking it
	cutoff := time.Now().Add(-lostGrabHours * time.Hour)
	seen := map[string]bool{}
	for i := range grabs {
		h := &grabs[i]
		id := strings.ToUpper(h.DownloadId)
		if id == "" || heard[id] || inQueue[id] || seen[id] || parseTime(h.Date).After(cutoff) {
			continue
		}
		seen[id] = true
		row := projectHistory(h)
		out.report(limit, finding{
			Series: row.Series, SeriesID: h.SeriesId, Subject: strings.TrimSpace(row.Episode + " " + h.SourceTitle), Problem: problemGrabWentNowhere,
			Detail: fmt.Sprintf("grabbed %s from %s, sent to %s, and never imported or failed; it is not in the queue", day(h.Date), row.Indexer, row.Client),
			Fix:    fmt.Sprintf("history_mark_failed %d to blocklist it and search again, or episode_search", h.Id),
		})
	}

	return out, nil
}
