package tools

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The audits: each a sweep over the whole library (or one series) for one
// thing that goes wrong, answering a worklist rather than a dump, and naming
// the tool that fixes what it finds.
//
// Detection is code, correction is judgment: the sweeps are cheap and
// deterministic, and the AI reasons only about what they turn up.

// auditIn is what most audits take.
type auditIn struct {
	Series string `json:"series,omitempty" jsonschema:"audit only this series: title, Sonarr id or tvdb:<id>"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"findings to return, default 100; total_findings is the real count"`
}

// finding is one row of an audit's worklist.
type finding struct {
	Series   string `json:"series,omitempty"`
	SeriesID int    `json:"series_id,omitempty"`
	Subject  string `json:"subject,omitempty"   jsonschema:"what the finding is about: an episode, a file, a folder, a download, a setting"`
	Problem  string `json:"problem"             jsonschema:"the kind of problem, a short fixed phrase an audit's findings can be grouped by"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"       jsonschema:"how to fix it, naming the tool"`
}

// auditOut is what most audits answer.
type auditOut struct {
	Scanned  int       `json:"scanned"        jsonschema:"how many series (or downloads, or settings) were looked at"`
	Found    int       `json:"total_findings"`
	Findings []finding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
}

// report adds a finding, counting it whether or not it fits under the limit.
func (o *auditOut) report(limit int, f finding) {
	o.Found++
	if len(o.Findings) < limitOr(limit, 100) {
		o.Findings = append(o.Findings, f)
	}
}

// snapshot is the library as the audits read it, loaded once per call and
// shared: audit_all runs every audit over one snapshot rather than reading
// every series' files once an audit.
type snapshot struct {
	r      *registry
	series []sonarr.SeriesResource
	// named is set when the audit was asked about one series, rather than
	// covering a library that happens to hold only one
	named bool

	mu          sync.Mutex
	files       map[int][]sonarr.EpisodeFileResource
	episodes    map[int][]sonarr.EpisodeResource
	disk        map[int][]string
	folders     map[string]map[string]string
	folderState *folders
	profiles    []sonarr.QualityProfileResource
}

// snap reads the series an audit covers: one, when named, or every one.
func (r *registry) snap(ctx context.Context, series string) (*snapshot, error) {
	s := &snapshot{
		r: r, files: map[int][]sonarr.EpisodeFileResource{}, episodes: map[int][]sonarr.EpisodeResource{}, disk: map[int][]string{},
		folders: map[string]map[string]string{},
	}
	if series != "" {
		one, err := r.resolveSeries(ctx, series)
		if err != nil {
			return nil, err
		}
		s.series, s.named = []sonarr.SeriesResource{*one}, true
		return s, nil
	}
	all, err := r.allSeries(ctx)
	if err != nil {
		return nil, err
	}
	s.series = all

	return s, nil
}

// only is the id of the one series the audit was asked about, or 0 when it
// covers the whole library.
func (s *snapshot) only() int {
	if s.named {
		return s.series[0].Id
	}

	return 0
}

// covers reports whether a series is in the snapshot.
func (s *snapshot) covers(seriesID int) bool {
	return slices.ContainsFunc(s.series, func(x sonarr.SeriesResource) bool { return x.Id == seriesID })
}

func (s *snapshot) filesOf(ctx context.Context, seriesID int) ([]sonarr.EpisodeFileResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if f, ok := s.files[seriesID]; ok {
		return f, nil
	}
	res, err := s.r.client.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOperationOptions{SeriesId: seriesID})
	if err != nil {
		return nil, err
	}
	s.files[seriesID] = res.Model

	return res.Model, nil
}

func (s *snapshot) episodesOf(ctx context.Context, seriesID int) ([]sonarr.EpisodeResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.episodes[seriesID]; ok {
		return e, nil
	}
	eps, err := s.r.episodesOf(ctx, seriesID, nil, false)
	if err != nil {
		return nil, err
	}
	s.episodes[seriesID] = eps

	return eps, nil
}

// foldersIn lists the folders really in a parent folder, as Sonarr sees them,
// by lower case name: one call answers for every series in a root folder, and
// tells a folder that is gone from one that is merely empty.
func (s *snapshot) foldersIn(ctx context.Context, parent string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent = strings.TrimRight(parent, "/")
	if f, ok := s.folders[parent]; ok {
		return f, nil
	}
	// Sonarr lists the contents of a path ending in a separator, and the
	// parent's contents otherwise
	res, err := s.r.client.GetFileSystem(ctx, sonarr.GetFileSystemOperationOptions{Path: parent + "/"})
	if err != nil {
		return nil, err
	}
	var listing struct {
		Directories []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"directories"`
	}
	if err := unmarshalRaw(res.Model, &listing); err != nil {
		return nil, fmt.Errorf("reading the folders in %s: %w", parent, err)
	}
	out := make(map[string]string, len(listing.Directories))
	for _, d := range listing.Directories {
		out[strings.ToLower(d.Name)] = strings.TrimRight(d.Path, "/")
	}
	s.folders[parent] = out

	return out, nil
}

// diskOf lists the video files really in a series' folder, as Sonarr sees
// them: empty when the folder is gone.
func (s *snapshot) diskOf(ctx context.Context, series *sonarr.SeriesResource) ([]string, error) {
	s.mu.Lock()
	if d, ok := s.disk[series.Id]; ok {
		defer s.mu.Unlock()
		return d, nil
	}
	s.mu.Unlock()

	out, err := s.videoFiles(ctx, series.Path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.disk[series.Id] = out
	s.mu.Unlock()

	return out, nil
}

// videoFiles are the video files in a folder, as Sonarr sees them, without
// the extras and specials folders it skips itself. A folder that is not
// there has none.
func (s *snapshot) videoFiles(ctx context.Context, folder string) ([]string, error) {
	res, err := s.r.client.GetFileSystemMediaFiles(ctx, sonarr.GetFileSystemMediaFilesOperationOptions{Path: folder})
	if err != nil {
		return nil, err
	}
	var files []struct {
		Path string `json:"path"`
	}
	if err := unmarshalRaw(res.Model, &files); err != nil {
		return nil, fmt.Errorf("reading the files in %s: %w", folder, err)
	}
	var out []string
	for _, f := range files {
		if !excludedPath(folder, f.Path) {
			out = append(out, f.Path)
		}
	}

	return out, nil
}

func (s *snapshot) qualityProfiles(ctx context.Context) ([]sonarr.QualityProfileResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.profiles == nil {
		res, err := s.r.client.GetQualityProfile(ctx)
		if err != nil {
			return nil, err
		}
		s.profiles = res.Model
	}

	return s.profiles, nil
}

// episodeNumbers maps each file of a series to the episodes it holds.
func episodeNumbers(eps []sonarr.EpisodeResource) map[int][]int {
	out := map[int][]int{}
	for _, e := range eps {
		if e.EpisodeFileId > 0 {
			out[e.EpisodeFileId] = append(out[e.EpisodeFileId], e.EpisodeNumber)
		}
	}

	return out
}

// Sonarr's disk scan skips these, so the audits do too: extras folders,
// thumbnail caches, hidden folders, and the files the OS or a client leaves
// beside the video (DiskScanService's ExcludedSubFoldersRegex and
// ExcludedFilesRegex).
var (
	excludedFolders = regexp.MustCompile(`(?i)(?:\\|/|^)(?:extras|@eadir|\.@__thumb|extrafanart|plex versions|\.[^\\/]+)(?:\\|/)`)
	excludedFiles   = regexp.MustCompile(`(?i)^\._|^Thumbs\.db$|^\.DS_store$|\.partial~$`)
)

func excludedPath(root, p string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(p, strings.TrimRight(root, "/\\")), "/")

	return excludedFolders.MatchString("/"+rel) || excludedFiles.MatchString(path.Base(p))
}

// The kinds of finding the audits report: a finding's Problem, a short fixed
// phrase a worklist can be grouped by. Each is named here and listed in
// AuditProblems, so the live suite can check the library it seeds produces
// every one of them (acceptance/coverage_test.go).
const (
	// audit_missing_episodes
	problemEpisodesMissing    = "episodes missing"
	problemWholeSeasonMissing = "whole season missing"
	// audit_cutoff_unmet
	problemBelowQualityCutoff = "below quality cutoff"
	problemBelowFormatCutoff  = "below custom format cutoff"
	// audit_stuck_downloads
	problemCannotImport      = "cannot import"
	problemDownloadFailed    = "download failed"
	problemWaitingToImport   = "waiting to import"
	problemClientUnreachable = "download client unreachable"
	problemPausedInClient    = "paused in the download client"
	problemDownloadError     = "error"
	problemDownloadWarning   = "warning"
	problemNotStarting       = "not starting"
	// audit_failed_downloads
	problemRepeatedFailures = "repeated failures"
	problemGrabWentNowhere  = "grab went nowhere"
	// audit_unmapped_folders
	problemFolderUnknown       = "folder not in Sonarr"
	problemFolderOfMovedSeries = "folder of a series that moved"
	// audit_missing_folders
	problemSeriesFolderRenamed = "series folder renamed"
	problemSeriesFolderMissing = "series folder missing"
	problemRootFolderEmpty     = "root folder holds none of its series"
	// audit_untracked_files
	problemUnreadableName = "cannot tell which episode"
	problemNotImported    = "not imported"
	problemImportRejected = "rejected"
	problemFileNotTracked = "file not tracked"
	// audit_missing_files
	problemFilesGone   = "files gone from disk"
	problemFolderEmpty = "folder holds no files"
	// audit_naming
	problemNamesOffFormat = "files not named to the format"
	problemRenamingOff    = "renaming is off"
	// audit_quality_mismatch
	problemLabelledBetter = "labelled better than it is"
	problemLabelledWorse  = "labelled worse than it is"
	// audit_runtime
	problemRuntimeShort = "shorter than it should be"
	problemRuntimeLong  = "longer than it should be"
	problemNoRuntime    = "no running time"
	// audit_language
	problemNoAudioInLanguage     = "no audio in the language"
	problemRecordedOtherLanguage = "recorded in another language"
	problemLanguageUnknown       = "language unknown"
	// audit_monitoring
	problemSeriesUnmonitored       = "continuing series not monitored"
	problemNothingMonitored        = "nothing monitored"
	problemNewSeasonsIgnored       = "new seasons will not be monitored"
	problemLatestSeasonUnmonitored = "latest season not monitored"
	// audit_series_settings
	problemNotTypedAnime      = "anime not typed anime"
	problemNotTypedDaily      = "daily show not typed daily"
	problemFolderOffFormat    = "folder not named to the format"
	problemOutsideRootFolders = "outside every root folder"
	// audit_profiles
	problemProfileUnused     = "quality profile unused"
	problemTagUnused         = "tag unused"
	problemFormatUnscored    = "custom format scored nowhere"
	problemReleaseProfileOff = "release profile disabled"
	problemTagScopesNothing  = "tag scopes settings but no series"
	// audit_health: the check's level follows, as "health error"
	problemHealthCheck     = "health"
	problemRootUnreachable = "root folder unreachable"
	problemRootLowOnSpace  = "root folder low on space"
)

// AuditProblems is every kind of finding each audit can report. A run of the
// live suite fails when one of them is never reported, so an audit's branch
// cannot go untested (acceptance/coverage_test.go); audit_health's kinds are
// the prefix of a finding that ends in the check's own level.
var AuditProblems = map[string][]string{
	"audit_missing_episodes": {problemEpisodesMissing, problemWholeSeasonMissing},
	"audit_cutoff_unmet":     {problemBelowQualityCutoff, problemBelowFormatCutoff},
	"audit_stuck_downloads": {
		problemCannotImport, problemDownloadFailed, problemWaitingToImport, problemClientUnreachable,
		problemPausedInClient, problemDownloadError, problemDownloadWarning, problemNotStarting,
	},
	"audit_failed_downloads": {problemRepeatedFailures, problemDownloadFailed, problemGrabWentNowhere},
	"audit_unmapped_folders": {problemFolderUnknown, problemFolderOfMovedSeries},
	"audit_missing_folders":  {problemSeriesFolderRenamed, problemSeriesFolderMissing, problemRootFolderEmpty},
	"audit_untracked_files":  {problemUnreadableName, problemNotImported, problemImportRejected, problemFileNotTracked},
	"audit_missing_files":    {problemFilesGone, problemFolderEmpty},
	"audit_naming":           {problemNamesOffFormat, problemRenamingOff},
	"audit_quality_mismatch": {problemLabelledBetter, problemLabelledWorse},
	"audit_runtime":          {problemRuntimeShort, problemRuntimeLong, problemNoRuntime},
	"audit_language":         {problemNoAudioInLanguage, problemRecordedOtherLanguage, problemLanguageUnknown},
	"audit_monitoring": {
		problemSeriesUnmonitored, problemNothingMonitored, problemNewSeasonsIgnored, problemLatestSeasonUnmonitored,
	},
	"audit_series_settings": {problemNotTypedAnime, problemNotTypedDaily, problemFolderOffFormat, problemOutsideRootFolders},
	"audit_profiles": {
		problemProfileUnused, problemTagUnused, problemFormatUnscored, problemReleaseProfileOff, problemTagScopesNothing,
	},
	"audit_health": {problemHealthCheck, problemRootUnreachable, problemRootLowOnSpace},
}

// auditSpec is one audit: its tool, and the sweep behind it that audit_all
// runs too.
type auditSpec struct {
	name        string
	description string
	run         func(ctx context.Context, s *snapshot, limit int) (auditOut, error)
	// note says what audit_all's count means, when that needs saying
	note string
}

// auditSpecs are the audits that take the common input, in the order audit_all
// reports them. The others (a threshold of their own to take) register
// themselves, and join audit_all with their defaults through
// mediaAuditSpecs.
func (r *registry) auditSpecs() []auditSpec {
	return []auditSpec{
		{
			name: "audit_missing_episodes", run: r.auditMissingEpisodes,
			description: "Monitored episodes that have aired and have no file, grouped by series and season, saying which are already downloading and which Sonarr has searched for and not found. Fix with episode_search, season_search or series_search, or wanted_search for the whole library.",
		},
		{
			name: "audit_cutoff_unmet", run: r.auditCutoffUnmet,
			description: "Monitored episode files below their quality profile's cutoff, grouped by series with each file's current quality: below the cutoff quality, or below the custom format score the profile upgrades until - which Sonarr's own Cutoff Unmet list does not count. Says when a profile has Upgrades Allowed off, which Sonarr's own default profiles do, so nothing is replaced by itself. Fix with series_search or episode_search, or wanted_search kind cutoff; profile_list shows the cutoffs.",
		},
		{
			name: "audit_stuck_downloads", run: r.auditStuckDownloads,
			description: "Downloads in the queue that need a person: blocked from importing (and why), waiting to import for hours, failed, paused, flagged with warnings or errors, or on a download client Sonarr cannot reach. Fix with queue_remove (with blocklist, to get a different release), or import_scan and import_apply for one Sonarr could not match.",
		},
		{
			name: "audit_failed_downloads", run: r.auditFailedDownloads,
			description: "Download failures over the last 30 days, by episode, flagging the episodes whose releases keep failing; and grabs that went nowhere - sent to the download client, never imported, never failed, and no longer in the queue. Fix with release_search to pick a release by hand, or history_mark_failed for a bad grab.",
		},
		{
			name: "audit_unmapped_folders", run: r.auditUnmappedFolders,
			description: "Folders in the root folders that Sonarr does not know as a series, and how many video files each holds: shows copied in by hand, or left behind when a series was deleted. A folder that goes by the name of a series whose own folder is gone is named as that series moved, to be pointed at rather than added again. Fix with series_import, which matches each folder to a show and adds it with its files.",
		},
		{
			name: "audit_untracked_files", run: r.auditUntrackedFiles,
			description: "Video files in series folders that Sonarr is not tracking - copied in by hand, a second copy of an episode, a name it cannot parse - with what Sonarr reads from each name and why it has not imported it. Fix with import_apply (import_scan shows one folder), or series_rescan after fixing a name.",
		},
		{
			name: "audit_missing_folders", run: r.auditMissingFolders,
			description: "Series whose folder is not on disk although Sonarr records files in it: renamed or moved outside Sonarr, or on a drive that is not mounted (reported once for the root folder, not once for each series under it). When a folder Sonarr does not know goes by the series' name, the finding says so and names it, so the series can be pointed at it rather than added again. A series with no files has no folder until Sonarr imports one, and is not a finding. Fix with series_edit path and series_rescan.",
		},
		{
			name: "audit_missing_files", run: r.auditMissingFiles,
			description: "Episode files Sonarr records that are no longer on disk - deleted or moved outside Sonarr - so the episodes show as downloaded when they are not. A series folder that is gone is audit_missing_folders' finding. Fix with series_rescan, which drops the records and makes the episodes missing again.",
		},
		{
			name: "audit_quality_mismatch", run: r.auditQualityMismatch,
			description: "Episode files recorded as one quality whose video is another: recorded 1080p but really 720p (Sonarr will never upgrade it), or an SD label on an HD file (Sonarr keeps trying to upgrade it). Sonarr reads the resolution from the video when it imports, so this is a quality set by hand in a manual import or an edit, or a file replaced on disk since. Fix with file_edit to record the real quality.",
		},
		{
			name: "audit_naming", run: r.auditNaming,
			description: "Series whose files are not named to Sonarr's naming format, with a sample of the renames. With Sonarr's Rename Episodes setting off it keeps the names files arrive with and cannot say which differ, and the audit says so instead. Fix with series_rename.",
		},
		{
			name: "audit_monitoring", run: r.auditMonitoring,
			description: "Monitoring that will quietly miss episodes: continuing series that will not monitor new seasons, unmonitored continuing series, monitored series with every season unmonitored, and a latest season left unmonitored. Fix with series_edit or season_monitor.",
		},
		{
			name: "audit_series_settings", run: r.auditSeriesSettings,
			description: "Series settings that look wrong: anime not typed as anime (so absolute numbering fails), daily shows not typed as daily, a folder name that does not match the naming format, and series outside every root folder. Fix with series_edit.",
		},
		{
			name: "audit_profiles", run: r.auditProfiles,
			description: "Settings that do nothing: quality profiles no series uses, tags nothing uses, custom formats no profile scores, and delay profiles, release profiles, indexers and download clients scoped to tags no series has. Fix with tag_delete and series_edit.",
		},
		{
			name: "audit_health", run: r.auditHealth,
			description: "Sonarr's own health checks - indexers and download clients failing, root folders missing, remote path mappings wrong - and root folders running out of space.",
		},
	}
}

func registerAuditTools(r *registry) {
	for _, spec := range r.auditSpecs() {
		add(r, readTool, &mcp.Tool{
			Name:        spec.name,
			Description: spec.description,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
			s, err := r.snap(ctx, in.Series)
			if err != nil {
				return nil, auditOut{}, err
			}
			out, err := spec.run(ctx, s, in.Limit)

			return nil, out, err
		})
	}
	registerMediaAudits(r)
	registerAuditAll(r)
}

// audit_all ----------------------------------------------------------------

type auditAllRow struct {
	Audit    string `json:"audit"`
	Findings int    `json:"findings"`
	Scanned  int    `json:"scanned"`
	Note     string `json:"note,omitempty"`
}

type auditAllOut struct {
	Series int           `json:"series"         jsonschema:"series audited"`
	Total  int           `json:"total_findings"`
	Audits []auditAllRow `json:"audits"`
}

func registerAuditAll(r *registry) {
	type allIn struct {
		Series string `json:"series,omitempty" jsonschema:"audit only this series: title, Sonarr id or tvdb:<id>"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_all",
		Description: "Run every audit and report only the counts, so one call says where the library needs work: missing episodes, files below cutoff, stuck and failing downloads, folders and files Sonarr has lost track of or thinks it has, naming, quality labels, runtimes, languages, monitoring gaps, dead settings, and Sonarr's health. " +
			"Start here, then call the audit whose count is not zero for its worklist.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in allIn) (*mcp.CallToolResult, auditAllOut, error) {
		s, err := r.snap(ctx, in.Series)
		if err != nil {
			return nil, auditAllOut{}, err
		}
		out := auditAllOut{Series: len(s.series)}
		specs := append(r.auditSpecs(), r.mediaAuditSpecs()...)
		for _, spec := range specs {
			res, err := spec.run(ctx, s, 1)
			if err != nil {
				return nil, auditAllOut{}, fmt.Errorf("%s: %w", spec.name, err)
			}
			out.Audits = append(out.Audits, auditAllRow{Audit: spec.name, Findings: res.Found, Scanned: res.Scanned, Note: spec.note})
			out.Total += res.Found
		}

		return nil, out, nil
	})
}
