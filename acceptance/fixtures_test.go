//go:build integration

// The fixtures: the library scripts/testenv.sh lays out, what Sonarr should
// make of it once imported, the releases the fake indexer offers, and the
// seeding that brings it all into Sonarr through the tools.
package acceptance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/fakes/newznab"
	"github.com/katbyte/sonarr-mcp/internal/fakes/sabnzbd"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// seriesFixture is a series in the library and what Sonarr should hold for
// it once seeded.
type seriesFixture struct {
	Title  string
	Year   int
	TvdbID int
	// Folder is its folder under /tv; "" for a series added with no files.
	Folder string
	// Files is how many episode files Sonarr should import from the folder.
	Files int
}

// The series, as testenv.sh lays them out (see the table there for what each
// file is for).
var (
	firefly      = seriesFixture{Title: "Firefly", Year: 2002, TvdbID: 78874, Folder: "Firefly", Files: 6}
	chernobyl    = seriesFixture{Title: "Chernobyl", Year: 2019, TvdbID: 360893, Folder: "Chernobyl (2019)", Files: 5}
	cowboyBebop  = seriesFixture{Title: "Cowboy Bebop", Year: 1998, TvdbID: 76885, Folder: "Cowboy Bebop", Files: 2}
	severance    = seriesFixture{Title: "Severance", Year: 2022, TvdbID: 371980, Folder: "Severance", Files: 2}
	breakingBad  = seriesFixture{Title: "Breaking Bad", Year: 2008, TvdbID: 81189}
	theExpanse   = seriesFixture{Title: "The Expanse", Year: 2015, TvdbID: 280619, Folder: "The Expanse", Files: 2}
	importedSeed = []seriesFixture{firefly, chernobyl, cowboyBebop, severance}
	// bandOfBrothers is added and deleted again by TestSeriesAddEditDelete:
	// a miniseries nothing else uses.
	bandOfBrothers = seriesFixture{Title: "Band of Brothers", Year: 2001, TvdbID: 74205}
)

// everySeries is every series the suite adds, which is all the providers'
// lists of every show they know need to keep when recorded (internal/cassettes).
var everySeries = []seriesFixture{firefly, chernobyl, cowboyBebop, severance, breakingBad, theExpanse, bandOfBrothers}

// tvdbIDs are the series' TheTVDB ids.
func tvdbIDs(series []seriesFixture) []int {
	ids := make([]int, 0, len(series))
	for _, s := range series {
		ids = append(ids, s.TvdbID)
	}

	return ids
}

// profile is the quality profile every seeded series is on.
const profile = "HD-1080p"

// unusedTag is a tag the seed creates and nothing carries.
const unusedTag = "4k"

// The fakes the container searches and downloads with.
var (
	indexer *newznab.Server
	sab     *sabnzbd.Server
)

// fakeKey is the API key both fakes demand, so a Sonarr that sent none
// would be caught.
const fakeKey = "sonarr-mcp-fake"

// release builds a catalogue entry for one episode (episode 0 for a season
// pack).
func release(title string, tvdb, season, episode int, sizeGB float64, category int) newznab.Release {
	return newznab.Release{
		Title: title, TVDBID: tvdb, Season: season, Episode: episode, Size: int64(sizeGB * (1 << 30)),
		Category: category, PubDate: time.Now().Add(-48 * time.Hour), Grabs: 12, Group: "FAKE",
	}
}

// catalogue is what the fake indexer offers: releases for the episodes the
// library is missing, at the qualities the profile wants and some it does
// not, so a search has something to grab and something to reject.
func catalogue() []newznab.Release {
	bb := breakingBad.TvdbID
	out := []newznab.Release{
		release("Breaking.Bad.S01E01.Pilot.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 1, 1.6, newznab.CategoryHD),
		release("Breaking.Bad.S01E01.Pilot.720p.HDTV.x264-FAKE", bb, 1, 1, 0.9, newznab.CategoryHD),
		release("Breaking.Bad.S01E01.Pilot.2160p.WEB-DL.DDP5.1.HDR.HEVC-FAKE", bb, 1, 1, 7.5, newznab.CategoryUHD),
		release("Breaking.Bad.S01E02.Cats.in.the.Bag.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 2, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E03.And.the.Bags.in.the.River.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 3, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E04.Cancer.Man.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 4, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E05.Gray.Matter.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 5, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E06.Crazy.Handful.of.Nothin.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 6, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E07.A.No.Rough.Stuff.Type.Deal.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 7, 1.4, newznab.CategoryHD),
		release("Firefly.S01E07.Safe.1080p.WEB-DL.DD5.1.H.264-FAKE", firefly.TvdbID, 1, 7, 1.3, newznab.CategoryHD),
		release("Firefly.S01E08.Ariel.1080p.WEB-DL.DD5.1.H.264-FAKE", firefly.TvdbID, 1, 8, 1.3, newznab.CategoryHD),
		release("Chernobyl.S01E05.Vichnaya.Pamyat.1080p.WEB-DL.DD5.1.H.264-FAKE", chernobyl.TvdbID, 1, 5, 2.1, newznab.CategoryHD),
	}

	return out
}

// startFakes starts the indexer and the download client the container is
// configured with, on the ports testenv.sh told it about.
func startFakes() error {
	iport, err := envPort("SONARR_TEST_INDEXER_PORT", 18081)
	if err != nil {
		return err
	}
	sport, err := envPort("SONARR_TEST_SAB_PORT", 18082)
	if err != nil {
		return err
	}
	if indexer, err = newznab.New(newznab.Options{
		Addr: ":" + strconv.Itoa(iport), APIKey: fakeKey, PublicHost: containerHost(), Releases: catalogue(),
	}); err != nil {
		return err
	}
	complete := filepath.Join(dataDir(), "downloads", "complete")
	if sab, err = sabnzbd.New(sabnzbd.Options{
		Addr: ":" + strconv.Itoa(sport), APIKey: fakeKey, PublicHost: containerHost(),
		CompleteDir: "/downloads/complete", HostCompleteDir: complete,
	}); err != nil {
		return err
	}
	if err := waitHTTP(indexer.LocalURL() + "/api?t=caps"); err != nil {
		return fmt.Errorf("the fake indexer never answered: %w", err)
	}

	return waitHTTP(sab.LocalURL() + "/api?mode=version")
}

func stopFakes() {
	for _, c := range []interface{ Close() error }{indexer, sab} {
		if c != nil {
			_ = c.Close()
		}
	}
}

// fakeVideo writes a video of black frames and a silent audio track (Sonarr
// will not import a file without one): minutes long, at frame size (e.g.
// 1920x1080) - the real resolution, since Sonarr reads the quality's
// resolution from the video rather than the name. testenv.sh's video() makes
// the library's files the same way.
func fakeVideo(path string, minutes int, frame string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil { //nolint:gosec // the container reads it as another user
		return err
	}
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", //nolint:gosec // fixed arguments and a path under the test's data dir
		"-f", "lavfi", "-i", "color=c=black:s="+frame+":r=1", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono",
		"-t", strconv.Itoa(minutes*60), "-c:v", "libx264", "-preset", "ultrafast", "-crf", "51", "-g", "3600",
		"-pix_fmt", "yuv420p", "-c:a", "flac", "-compression_level", "0", "-metadata:s:a:0", "language=und", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", path, err, out)
	}

	return os.Chmod(path, 0o666) //nolint:gosec // the container reads it as another user
}

// providerFields fills a provider schema's fields from values by name.
func providerFields(fields []sonarr.Field, values map[string]any) []sonarr.Field {
	out := slices.Clone(fields)
	for i := range out {
		if v, ok := values[out[i].Name]; ok {
			out[i].Value = v
		}
	}

	return out
}

// seed builds the library through the tools. It is idempotent: a Sonarr
// that already holds the series is left alone, so a suite can run again
// against a container that is still up.
func seed() error {
	// the fakes are new every run, so the providers pointing at them are too:
	// a failure Sonarr recorded against the last run's fakes would otherwise
	// still be backing it off them
	if err := seedDownloads(); err != nil {
		return err
	}
	existing, err := invoke("series_list", nil)
	if err != nil {
		return err
	}
	if numOr0(existing["total"]) > 0 {
		return nil
	}

	if _, err := invoke("rootfolder_add", map[string]any{"path": "/tv"}); err != nil {
		return err
	}

	folders := make([]any, 0, len(importedSeed))
	for _, s := range importedSeed {
		folders = append(folders, s.Folder)
	}
	imported, err := invoke("series_import", map[string]any{"folders": folders, "quality_profile": profile})
	if err != nil {
		return err
	}
	for _, row := range rowsOf(imported["folders"]) {
		if str(row["status"]) != "imported" {
			return fmt.Errorf("series_import did not import %s: %v", str(row["folder"]), row)
		}
	}
	if _, err := invoke("series_add", map[string]any{"series": "tvdb:" + strconv.Itoa(breakingBad.TvdbID), "quality_profile": profile}); err != nil {
		return err
	}
	for _, s := range importedSeed {
		if err := waitForFiles(s.Title, s.Files); err != nil {
			return err
		}
	}
	if err := waitIdle(); err != nil {
		return err
	}

	for _, edit := range []map[string]any{
		{"series": severance.Title, "monitor_new_items": "none"},
		{"series": cowboyBebop.Title, "add_tags": []any{"anime"}},
	} {
		if _, err := invoke("series_edit", edit); err != nil {
			return err
		}
	}
	if _, err := invoke("tag_create", map[string]any{"label": unusedTag}); err != nil {
		return err
	}

	// renaming on, now the files are in: Sonarr renames only what it
	// imports from here, so the files already there keep their names and
	// the rename preview has them to list
	return setRenaming(true)
}

// setRenaming turns Sonarr's Rename Episodes setting on or off.
func setRenaming(on bool) error {
	naming, err := api.GetConfigNaming(ctx)
	if err != nil {
		return err
	}
	body := *naming.Model
	body.RenameEpisodes = new(on)
	_, err = api.PutConfigNamingById(ctx, strconv.Itoa(body.Id), body)

	return err
}

// seedDownloads points Sonarr at the fake indexer and download client, and
// turns off the automatic re-search after a failure, so a failure a test
// causes stays one failure rather than a fresh grab of the next release.
func seedDownloads() error {
	cfg, err := api.GetConfigDownloadClient(ctx)
	if err != nil {
		return err
	}
	body := *cfg.Model
	body.AutoRedownloadFailed = new(false)
	body.AutoRedownloadFailedFromInteractiveSearch = new(false)
	if _, err := api.PutConfigDownloadClientById(ctx, strconv.Itoa(body.Id), body); err != nil {
		return fmt.Errorf("download client settings: %w", err)
	}

	if err := removeFakeProviders(); err != nil {
		return err
	}
	schemas, err := api.GetIndexerSchema(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(schemas.Model, func(s sonarr.IndexerResource) bool { return s.Implementation == "Newznab" })
	if i < 0 {
		return errors.New("sonarr has no Newznab indexer schema")
	}
	idx := schemas.Model[i]
	// RSS off: Sonarr's scheduled RSS sync would otherwise grab releases for
	// every missing episode the catalogue covers, whenever it happened to
	// run, and the queue would never hold only what a test put there
	idx.Name, idx.EnableRss, idx.EnableAutomaticSearch, idx.EnableInteractiveSearch = fakeIndexerName, new(false), new(true), new(true)
	idx.Fields = providerFields(idx.Fields, map[string]any{
		"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": fakeKey, "categories": []int{5030, 5040, 5045},
	})
	if _, err := api.PostIndexer(ctx, idx, sonarr.PostIndexerOperationOptions{}); err != nil {
		return fmt.Errorf("adding the fake indexer: %w", err)
	}

	clients, err := api.GetDownloadClientSchema(ctx)
	if err != nil {
		return err
	}
	j := slices.IndexFunc(clients.Model, func(s sonarr.DownloadClientResource) bool { return s.Implementation == "Sabnzbd" })
	if j < 0 {
		return errors.New("sonarr has no Sabnzbd download client schema")
	}
	dc := clients.Model[j]
	dc.Name, dc.Enable = fakeClientName, new(true)
	dc.Fields = providerFields(dc.Fields, map[string]any{
		"host": sab.Host(), "port": sab.Port(), "apiKey": fakeKey, "tvCategory": sab.Category(), "useSsl": false, "urlBase": "",
	})
	if _, err := api.PostDownloadClient(ctx, dc, sonarr.PostDownloadClientOperationOptions{}); err != nil {
		return fmt.Errorf("adding the fake download client: %w", err)
	}

	return nil
}

// The names of the providers the suite adds, so a run can find the last
// run's.
const (
	fakeIndexerName   = "Fake Indexer"
	brokenIndexerName = "Broken Indexer"
	fakeClientName    = "Fake SABnzbd"
)

// removeFakeProviders deletes the indexers and download clients an earlier
// run added.
func removeFakeProviders() error {
	indexers, err := api.GetIndexer(ctx)
	if err != nil {
		return err
	}
	for _, i := range indexers.Model {
		if i.Name == fakeIndexerName || i.Name == brokenIndexerName {
			if _, err := api.DeleteIndexerById(ctx, i.Id); err != nil {
				return err
			}
		}
	}
	clients, err := api.GetDownloadClient(ctx)
	if err != nil {
		return err
	}
	for _, c := range clients.Model {
		if c.Name == fakeClientName {
			if _, err := api.DeleteDownloadClientById(ctx, c.Id); err != nil {
				return err
			}
		}
	}

	return nil
}

// waitForFiles waits for Sonarr to finish importing a series' folder: the
// refresh that follows an import reads TheTVDB, then the scan imports the
// files.
func waitForFiles(title string, want int) error {
	var last string
	for range 120 {
		out, err := invoke("series_get", map[string]any{"series": title})
		switch {
		case err != nil:
			last = err.Error()
		case numOr0(out["episodes_have"]) == want && numOr0(out["episodes_total"]) > 0:
			return nil
		default:
			last = fmt.Sprintf("%d of %d files", numOr0(out["episodes_have"]), want)
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("%s never reached %d files (%s)", title, want, last)
}

// waitIdle waits for Sonarr to finish every command it has queued or
// running. A series just added is refreshed and scanned, and then saved
// whole once more as the add finishes; a series_edit that lands before that
// save is quietly overwritten by it, so the seed edits only once Sonarr is
// done.
func waitIdle() error {
	var busy []string
	for range 120 {
		res, err := api.GetCommand(ctx)
		if err != nil {
			return err
		}
		busy = busy[:0]
		for _, c := range res.Model {
			if c.Status == sonarr.CommandStatusQueued || c.Status == sonarr.CommandStatusStarted {
				busy = append(busy, c.Name)
			}
		}
		if len(busy) == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("sonarr never finished %v", busy)
}

// hostPath maps a path as Sonarr sees it (/tv/...) to this machine.
func hostPath(containerPath string) string {
	rel, ok := strings.CutPrefix(containerPath, "/tv/")
	if !ok {
		return containerPath
	}

	return filepath.Join(tvDir(), filepath.FromSlash(rel))
}
