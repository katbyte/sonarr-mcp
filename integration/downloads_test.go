//go:build integration

package integration

// The downloads: an interactive search and a grab into the fake SABnzbd, the
// queue as the download moves, its import, a failure and the blocklist entry
// it leaves, releases pushed the way an RSS tool would and held back by a
// delay profile, and the history of all of it. Every download is Breaking
// Bad's, which only these tests touch, and each test leaves the episodes it
// used as it found them, so the suite can run again on the same container.

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/fakes/sabnzbd"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// approved searches for an episode's releases the way a person does, and
// returns the one Sonarr approves, with everything it found.
func approved(ctx context.Context, t *testing.T, e *sonarr.EpisodeResource) (best sonarr.ReleaseResource, found []sonarr.ReleaseResource) {
	t.Helper()

	found = must(sc.GetRelease(ctx, sonarr.GetReleaseOperationOptions{EpisodeId: e.Id})).Model
	i := slices.IndexFunc(found, func(r sonarr.ReleaseResource) bool { return boolValue(r.Approved) })
	if i < 0 {
		t.Fatalf("no approved release for S%02dE%02d: %+v", e.SeasonNumber, e.EpisodeNumber, found)
	}

	return found[i], found
}

// grab sends a release found by a search to the download client, and returns
// the job the fake client received for it.
func grab(ctx context.Context, t *testing.T, r *sonarr.ReleaseResource) sabnzbd.Job {
	t.Helper()

	res := must(sc.PostRelease(ctx, sonarr.ReleaseResource{Guid: r.Guid, IndexerId: r.IndexerId}))
	status(t, res.HttpResponse, http.StatusOK)

	return sabJob(t, r.Title)
}

// sabJob is the fake client's job for a release: SABnzbd names it after the
// NZB, which Sonarr names after the release.
func sabJob(t *testing.T, title string) sabnzbd.Job {
	t.Helper()

	job, ok := sab.FindJob(title)
	if !ok {
		t.Fatalf("the fake client has no job for %s: %+v", title, sab.Jobs())
	}
	t.Cleanup(func() { _ = sab.Remove(job.ID) })

	return job
}

// refreshDownloads has Sonarr read the download client now, rather than at
// its next minute.
func refreshDownloads(ctx context.Context, t *testing.T) {
	t.Helper()

	runCommand(ctx, t, "RefreshMonitoredDownloads", nil)
}

// downloadWait is how long a test waits for Sonarr to see what the fake
// client did. It is usually a second or two, but a client Sonarr failed to
// read is skipped for a minute (DownloadClientFactory.DownloadHandlingEnabled
// leaves out a client in back-off), and a run has been seen to wait that out.
const downloadWait = 2 * time.Minute

// waitQueued waits for Sonarr's queue to show a download in the state check
// wants, having it read the client again between looks.
func waitQueued(ctx context.Context, t *testing.T, downloadID string, check func(sonarr.QueueResource) bool) sonarr.QueueResource {
	t.Helper()

	var q sonarr.QueueResource
	if !poll(downloadWait, func() bool {
		refreshDownloads(ctx, t)
		all := must(sc.GetQueueComplete(ctx, sonarr.GetQueueOperationOptions{
			SeriesIds: []int{seriesIDs[breakingBad.Title]}, IncludeEpisode: new(true), PageSize: 5,
		})).Items
		i := slices.IndexFunc(all, func(x sonarr.QueueResource) bool { return strings.EqualFold(x.DownloadId, downloadID) })
		if i < 0 {
			return false
		}
		q = all[i]
		return check(q)
	}) {
		t.Fatalf("download %s never reached the state wanted: %+v", downloadID, q)
	}

	return q
}

// historyOf is the series' history for one download.
func historyOf(ctx context.Context, downloadID string, event sonarr.EpisodeHistoryEventType) []sonarr.HistoryResource {
	all := must(sc.GetHistorySeries(ctx, sonarr.GetHistorySeriesOperationOptions{
		SeriesId: seriesIDs[breakingBad.Title], EventType: event, IncludeSeries: new(true), IncludeEpisode: new(true),
	})).Model

	return slices.DeleteFunc(all, func(h sonarr.HistoryResource) bool { return !strings.EqualFold(h.DownloadId, downloadID) })
}

// blocklisted is the series' blocklist entry for a release title, if any.
func blocklisted(ctx context.Context, title string) (sonarr.BlocklistResource, bool) {
	all := must(sc.GetBlocklistComplete(ctx, sonarr.GetBlocklistOperationOptions{
		SeriesIds: []int{seriesIDs[breakingBad.Title]}, Protocols: []sonarr.DownloadProtocol{sonarr.DownloadProtocolUsenet},
	})).Items
	i := slices.IndexFunc(all, func(b sonarr.BlocklistResource) bool { return b.SourceTitle == title })
	if i < 0 {
		return sonarr.BlocklistResource{}, false
	}

	return all[i], true
}

// A grab, downloading, then complete: the queue follows it, Sonarr imports
// it, and the history has both. Marking the grab failed afterwards
// blocklists the release.
//
//nolint:paralleltest // the tests share one Sonarr
func TestDownloadImport(t *testing.T) {
	ctx := skipUnlessUp(t)
	started := time.Now().Add(-time.Minute)

	bb := seriesIDs[breakingBad.Title]
	e1 := episode(ctx, t, bb, 1)
	best, found := approved(ctx, t, &e1)
	// the profile wants 1080p: the 720p and 2160p releases are rejected, with
	// Sonarr's reasons
	if len(found) != 3 || !strings.Contains(best.Title, "1080p.WEB-DL") || best.Quality.Quality.Name != "WEBDL-1080p" ||
		best.Protocol != sonarr.DownloadProtocolUsenet || best.Indexer != sdkIndexer || best.DownloadUrl == "" || !slices.Equal(best.EpisodeNumbers, []int{1}) {
		t.Errorf("the search found %d releases, the approved one %+v", len(found), best)
	}
	for _, r := range found {
		if r.Guid != best.Guid && (boolValue(r.Approved) || len(r.Rejections) == 0) {
			t.Errorf("a release the profile does not want = %+v", r)
		}
	}

	job := grab(ctx, t, &best)
	t.Cleanup(func() {
		// the episode's file, so a search for it approves a release again
		bg := context.WithoutCancel(ctx)
		if e := episode(bg, t, bb, 1); e.EpisodeFileId != 0 {
			_, _ = sc.DeleteEpisodeFileById(bg, e.EpisodeFileId)
		}
	})
	if err := sab.SetProgress(job.ID, 0.4); err != nil {
		t.Fatal(err)
	}
	q := waitQueued(ctx, t, job.ID, func(q sonarr.QueueResource) bool {
		return q.Status == sonarr.QueueStatusDownloading && q.Sizeleft < q.Size
	})
	if q.EpisodeId != e1.Id || q.Episode == nil || q.Title != best.Title || q.Quality.Quality.Name != "WEBDL-1080p" || q.Indexer != sdkIndexer ||
		q.DownloadClient != sdkClient || q.Protocol != sonarr.DownloadProtocolUsenet || q.TrackedDownloadState != sonarr.TrackedDownloadStateDownloading || q.Timeleft == "" {
		t.Errorf("the download in the queue = %+v", q)
	}
	details := must(sc.GetQueueDetails(ctx, sonarr.GetQueueDetailsOperationOptions{EpisodeIds: []int{e1.Id}, IncludeSeries: new(true)})).Model
	if len(details) != 1 || details[0].Id != q.Id || details[0].Series == nil || details[0].Series.Id != bb {
		t.Errorf("GetQueueDetails for the episode = %+v", details)
	}
	if st := must(sc.GetQueueStatus(ctx)).Model; st.TotalCount < 1 || st.Count < 1 {
		t.Errorf("GetQueueStatus = %+v", st)
	}

	// finished: an hour of video, which Sonarr moves into the series' folder
	if err := sab.CompleteWith(job.ID, func(dir string) error {
		return fakeVideo(ctx, filepath.Join(dir, job.Name+".mkv"), 58, "1920x1080")
	}); err != nil {
		t.Fatal(err)
	}
	if !poll(downloadWait, func() bool {
		refreshDownloads(ctx, t)
		return boolValue(episode(ctx, t, bb, 1).HasFile)
	}) {
		t.Fatal("the finished download was never imported")
	}
	grabbed := historyOf(ctx, job.ID, sonarr.EpisodeHistoryEventTypeGrabbed)
	imported := historyOf(ctx, job.ID, sonarr.EpisodeHistoryEventTypeDownloadFolderImported)
	if len(grabbed) != 1 || grabbed[0].EpisodeId != e1.Id || grabbed[0].Data["indexer"] != sdkIndexer || grabbed[0].Series == nil {
		t.Fatalf("the grab in the history = %+v", grabbed)
	}
	// the series history never carries the episode, asked to or not:
	// HistoryRepository.GetBySeries joins the episodes but maps only the
	// history rows, where Since and the paged history map both
	if grabbed[0].Episode != nil {
		t.Errorf("GetHistorySeries includes the episode now: %+v", grabbed[0].Episode)
	}
	if len(imported) != 1 || !strings.HasPrefix(imported[0].Data["importedPath"], "/tv/Breaking Bad/") ||
		!strings.HasPrefix(imported[0].Data["droppedPath"], "/downloads/complete/") {
		t.Errorf("the import in the history = %+v", imported)
	}
	since := must(sc.GetHistorySince(ctx, sonarr.GetHistorySinceOperationOptions{
		Date: started.UTC().Format(time.RFC3339), EventType: sonarr.EpisodeHistoryEventTypeGrabbed, IncludeSeries: new(true),
	})).Model
	if !slices.ContainsFunc(since, func(h sonarr.HistoryResource) bool { return h.Id == grabbed[0].Id && h.Series != nil }) {
		t.Errorf("GetHistorySince the test started = %d entries, without the grab", len(since))
	}
	// the paged history, filtered to the episode and to grabs and imports
	paged := must(sc.GetHistoryComplete(ctx, sonarr.GetHistoryOperationOptions{
		EpisodeId: e1.Id, EventType: []int{1, 3}, DownloadId: job.ID, IncludeEpisode: new(true), PageSize: 1,
	})).Items
	if len(paged) != 2 || paged[0].Episode == nil || paged[0].Episode.Id != e1.Id {
		t.Errorf("GetHistoryComplete one entry a page = %+v", paged)
	}
	if q, _ := sc.GetQueueDetails(ctx, sonarr.GetQueueDetailsOperationOptions{EpisodeIds: []int{e1.Id}}); len(q.Model) != 0 {
		t.Errorf("the imported download is still queued: %+v", q.Model)
	}

	// a grab marked failed after the fact blocklists its release, and leaves
	// the imported file alone
	status(t, must(sc.PostHistoryFailedById(ctx, grabbed[0].Id)).HttpResponse, http.StatusOK)
	if failed := historyOf(ctx, job.ID, sonarr.EpisodeHistoryEventTypeDownloadFailed); len(failed) != 1 || failed[0].Data["message"] != "Manually marked as failed" {
		t.Errorf("the failure in the history = %+v", failed)
	}
	entry, ok := blocklisted(ctx, best.Title)
	if !ok || entry.SeriesId != bb || !slices.Equal(entry.EpisodeIds, []int{e1.Id}) || entry.Indexer != sdkIndexer || entry.Quality == nil {
		t.Fatalf("the blocklist entry = %+v, %t", entry, ok)
	}
	if !boolValue(episode(ctx, t, bb, 1).HasFile) {
		t.Error("marking the grab failed took the episode's file")
	}
	status(t, must(sc.DeleteBlocklistById(ctx, entry.Id)).HttpResponse, http.StatusOK)
	if _, ok := blocklisted(ctx, best.Title); ok {
		t.Error("the deleted blocklist entry is still listed")
	}
}

// A download the client fails: Sonarr records the failure and blocklists the
// release, which a search then rejects.
//
//nolint:paralleltest // the tests share one Sonarr
func TestDownloadFailure(t *testing.T) {
	ctx := skipUnlessUp(t)

	bb := seriesIDs[breakingBad.Title]
	e2 := episode(ctx, t, bb, 2)
	best, _ := approved(ctx, t, &e2)
	job := grab(ctx, t, &best)
	if err := sab.Fail(job.ID, "Unpacking failed, CRC error"); err != nil {
		t.Fatal(err)
	}

	var failed []sonarr.HistoryResource
	waited := time.Now() //nolint:azproviderlint // taken before the wait it times; inlined, it would time nothing
	if !poll(downloadWait, func() bool {
		refreshDownloads(ctx, t)
		failed = historyOf(ctx, job.ID, sonarr.EpisodeHistoryEventTypeDownloadFailed)
		return len(failed) > 0
	}) {
		t.Fatalf("the failed download was never recorded; Sonarr's health: %+v", must(sc.GetHealth(ctx)).Model)
	}
	if d := time.Since(waited); d > 15*time.Second {
		t.Logf("Sonarr took %s to see the failure; its health: %+v", d.Round(time.Second), must(sc.GetHealth(ctx)).Model)
	}
	if failed[0].EpisodeId != e2.Id || !strings.Contains(failed[0].Data["message"], "CRC error") || failed[0].SourceTitle != best.Title {
		t.Errorf("the failure in the history = %+v", failed[0])
	}

	entry, ok := blocklisted(ctx, best.Title)
	if !ok || entry.Protocol != sonarr.DownloadProtocolUsenet || !strings.Contains(entry.Message, "CRC error") || entry.Series == nil {
		t.Fatalf("the blocklist entry = %+v, %t", entry, ok)
	}
	// blocklisted, the release is rejected by a search
	again := must(sc.GetRelease(ctx, sonarr.GetReleaseOperationOptions{EpisodeId: e2.Id})).Model
	i := slices.IndexFunc(again, func(r sonarr.ReleaseResource) bool { return r.Guid == best.Guid })
	if i < 0 || boolValue(again[i].Approved) || !slices.ContainsFunc(again[i].Rejections, func(r string) bool { return strings.Contains(r, "blocklisted") }) {
		t.Errorf("the blocklisted release in a search = %+v", again)
	}

	status(t, must(sc.DeleteBlocklistBulk(ctx, sonarr.BlocklistBulkResource{Ids: []int{entry.Id}})).HttpResponse, http.StatusOK)
	if _, ok := blocklisted(ctx, best.Title); ok {
		t.Error("the bulk-deleted blocklist entry is still listed")
	}
}

// Releases pushed the way an RSS tool pushes them take the automatic path,
// where a delay profile holds them back; the queue lists them held, grabs
// them on request, and removes what it grabbed.
//
//nolint:paralleltest // the tests share one Sonarr
func TestDelayedDownloads(t *testing.T) {
	ctx := skipUnlessUp(t)

	bb := seriesIDs[breakingBad.Title]
	tag := newTag(ctx, t, "sdk-delay")
	tagSeries(ctx, t, bb, tag)
	delay := must(sc.PostDelayProfile(ctx, sonarr.DelayProfileResource{
		EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: sonarr.DownloadProtocolUsenet,
		UsenetDelay: 24 * 60, Tags: []int{tag}, BypassIfHighestQuality: new(false),
	}))
	status(t, delay.HttpResponse, http.StatusCreated)
	t.Cleanup(func() { _, _ = sc.DeleteDelayProfileById(context.WithoutCancel(ctx), delay.Model.Id) })

	// E03 and E04, pushed as published this minute: a day's delay holds both
	held := make([]sonarr.QueueResource, 0, 2)
	titles := make([]string, 0, 2)
	for _, n := range []int{3, 4} {
		e := episode(ctx, t, bb, n)
		best, _ := approved(ctx, t, &e)
		titles = append(titles, best.Title)
		pushed := must(sc.PostReleasePush(ctx, sonarr.ReleaseResource{
			Title: best.Title, DownloadUrl: best.DownloadUrl, Protocol: sonarr.DownloadProtocolUsenet, IndexerId: best.IndexerId,
			PublishDate: time.Now().UTC().Format(time.RFC3339), Size: best.Size,
		}))
		status(t, pushed.HttpResponse, http.StatusOK)
		// what the release was taken for is in the Mapped fields: SeriesId and
		// EpisodeId are what a grab can name to override that
		if len(pushed.Model) != 1 || !boolValue(pushed.Model[0].TemporarilyRejected) || boolValue(pushed.Model[0].Approved) ||
			pushed.Model[0].MappedSeriesId != bb || len(pushed.Model[0].MappedEpisodeInfo) != 1 || pushed.Model[0].MappedEpisodeInfo[0].Id != e.Id {
			t.Fatalf("PostReleasePush(%s) = %+v", best.Title, pushed.Model)
		}
		queue := must(sc.GetQueueComplete(ctx, sonarr.GetQueueOperationOptions{SeriesIds: []int{bb}})).Items
		i := slices.IndexFunc(queue, func(q sonarr.QueueResource) bool { return q.EpisodeId == e.Id })
		if i < 0 || queue[i].Status != sonarr.QueueStatusDelay || queue[i].Title != best.Title || queue[i].DownloadId != "" {
			t.Fatalf("the held release in the queue = %+v", queue)
		}
		held = append(held, queue[i])
	}

	// a held release is grabbed now on request, one or many
	status(t, must(sc.PostQueueGrabById(ctx, held[0].Id)).HttpResponse, http.StatusOK)
	status(t, must(sc.PostQueueGrabBulk(ctx, sonarr.QueueBulkResource{Ids: []int{held[1].Id}})).HttpResponse, http.StatusOK)
	queued := make([]sonarr.QueueResource, 0, len(titles))
	for _, title := range titles {
		job := sabJob(t, title)
		queued = append(queued, waitQueued(ctx, t, job.ID, func(q sonarr.QueueResource) bool { return q.Status == sonarr.QueueStatusQueued }))
	}

	// and removed from the client, blocklisted so that a push of the same
	// release is not approved again, one or many
	remove := sonarr.DeleteQueueByIdOperationOptions{RemoveFromClient: new(true), Blocklist: new(true), SkipRedownload: new(true)}
	status(t, must(sc.DeleteQueueById(ctx, queued[0].Id, remove)).HttpResponse, http.StatusOK)
	status(t, must(sc.DeleteQueueBulk(ctx, sonarr.QueueBulkResource{Ids: []int{queued[1].Id}}, sonarr.DeleteQueueBulkOperationOptions(remove))).HttpResponse, http.StatusOK)
	for i, q := range queued {
		if _, ok := sab.Job(q.DownloadId); ok {
			t.Errorf("the removed download %s is still in the client", q.DownloadId)
		}
		entry, ok := blocklisted(ctx, titles[i])
		if !ok {
			t.Errorf("removing %s with blocklist blocklisted nothing", titles[i])
			continue
		}
		_, _ = sc.DeleteBlocklistBulk(ctx, sonarr.BlocklistBulkResource{Ids: []int{entry.Id}})
	}
	if left := must(sc.GetQueueComplete(ctx, sonarr.GetQueueOperationOptions{SeriesIds: []int{bb}})).Items; len(left) != 0 {
		t.Errorf("the queue after removing everything = %+v", left)
	}
}

// tagSeries adds a tag to a series for the rest of a test.
func tagSeries(ctx context.Context, t *testing.T, id, tag int) {
	t.Helper()

	s := *must(sc.GetSeriesById(ctx, id, sonarr.GetSeriesByIdOperationOptions{})).Model
	s.Tags = append(s.Tags, tag)
	must(sc.PutSeriesById(ctx, strconv.Itoa(id), s, sonarr.PutSeriesByIdOperationOptions{}))
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		s := *must(sc.GetSeriesById(bg, id, sonarr.GetSeriesByIdOperationOptions{})).Model
		s.Tags = slices.DeleteFunc(s.Tags, func(x int) bool { return x == tag })
		_, _ = sc.PutSeriesById(bg, strconv.Itoa(id), s, sonarr.PutSeriesByIdOperationOptions{})
	})
}
