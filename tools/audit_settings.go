package tools

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// auditMonitoring finds monitoring that will miss episodes without anyone
// noticing: nothing fails, the episodes are simply never looked for.
func (*registry) auditMonitoring(_ context.Context, s *snapshot, limit int) (auditOut, error) {
	out := auditOut{Scanned: len(s.series)}
	for i := range s.series {
		series := &s.series[i]
		continuing := series.Status == sonarr.SeriesStatusTypeContinuing || series.Status == sonarr.SeriesStatusTypeUpcoming
		f := finding{Series: series.Title, SeriesID: series.Id}

		var seasons []sonarr.SeasonResource
		for _, season := range series.Seasons {
			if season.SeasonNumber > 0 {
				seasons = append(seasons, season)
			}
		}
		slices.SortFunc(seasons, func(a, b sonarr.SeasonResource) int { return a.SeasonNumber - b.SeasonNumber })
		anyMonitored := slices.ContainsFunc(seasons, func(season sonarr.SeasonResource) bool { return boolv(season.Monitored) })

		switch {
		case continuing && !boolv(series.Monitored):
			f.Problem = problemSeriesUnmonitored
			f.Detail = fmt.Sprintf("%s is still %s, and Sonarr will not grab any new episode of it", series.Title, series.Status)
			f.Fix = "series_edit monitored true, if it is still wanted"
			out.report(limit, f)
		case boolv(series.Monitored) && len(seasons) > 0 && !anyMonitored:
			f.Problem = problemNothingMonitored
			f.Detail = "the series is monitored but every season is not, so Sonarr searches for nothing"
			f.Fix = "season_monitor the seasons wanted"
			out.report(limit, f)
		case continuing && boolv(series.Monitored) && series.MonitorNewItems == sonarr.NewItemMonitorTypesNone:
			f.Problem = problemNewSeasonsIgnored
			f.Detail = "the series is still airing, and a season that appears later will be added unmonitored and never searched for"
			f.Fix = "series_edit monitor_new_items all"
			out.report(limit, f)
		case continuing && boolv(series.Monitored) && anyMonitored && !boolv(seasons[len(seasons)-1].Monitored):
			latest := seasons[len(seasons)-1].SeasonNumber
			f.Subject = fmt.Sprintf("season %d", latest)
			f.Problem = problemLatestSeasonUnmonitored
			f.Detail = fmt.Sprintf("earlier seasons are monitored, the latest (season %d) is not", latest)
			f.Fix = fmt.Sprintf("season_monitor season %d", latest)
			out.report(limit, f)
		}
	}

	return out, nil
}

// hasGenre reports whether a series carries a genre, ignoring case.
func hasGenre(s *sonarr.SeriesResource, genres ...string) bool {
	return slices.ContainsFunc(s.Genres, func(g string) bool {
		return slices.ContainsFunc(genres, func(want string) bool { return strings.EqualFold(g, want) })
	})
}

// looksAnime reports whether a series is anime by its genres: tagged Anime,
// or animation in Japanese.
func looksAnime(s *sonarr.SeriesResource) bool {
	japanese := s.OriginalLanguage != nil && strings.EqualFold(s.OriginalLanguage.Name, "japanese")

	return hasGenre(s, "anime") || japanese && hasGenre(s, "animation")
}

// auditSeriesSettings finds series whose own settings look wrong for them.
func (r *registry) auditSeriesSettings(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	roots, err := r.client.GetRootFolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{Scanned: len(s.series)}
	for i := range s.series {
		series := &s.series[i]
		base := finding{Series: series.Title, SeriesID: series.Id}

		if looksAnime(series) && series.SeriesType != sonarr.SeriesTypesAnime {
			f := base
			f.Problem = problemNotTypedAnime
			f.Detail = fmt.Sprintf("its genres are %s and it is typed %s, so Sonarr will not read the absolute episode numbers anime releases use", strings.Join(series.Genres, ", "), series.SeriesType)
			f.Fix = "series_edit series_type anime"
			out.report(limit, f)
		}
		if hasGenre(series, "talk show", "news") && series.SeriesType != sonarr.SeriesTypesDaily {
			f := base
			f.Problem = problemNotTypedDaily
			f.Detail = fmt.Sprintf("its genres are %s and it is typed %s, so Sonarr will not match releases named by air date", strings.Join(series.Genres, ", "), series.SeriesType)
			f.Fix = "series_edit series_type daily"
			out.report(limit, f)
		}
		if !slices.ContainsFunc(roots.Answer, func(root sonarr.RootFolderResource) bool { return inFolder(series.Path, root.Path) }) {
			f := base
			f.Subject = series.Path
			f.Problem = problemOutsideRootFolders
			f.Detail = "the series' folder is in none of Sonarr's root folders, so it is missed by everything that works root folder by root folder"
			f.Fix = "series_edit path into a root folder, with move_files"
			out.report(limit, f)
		}

		res, err := r.client.GetSeriesByIdFolder(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		var want struct {
			Folder string `json:"folder"`
		}
		if err := unmarshalRaw(res.Answer, &want); err != nil {
			return auditOut{}, err
		}
		if have := path.Base(strings.TrimRight(series.Path, "/")); want.Folder != "" && have != want.Folder {
			f := base
			f.Subject = series.Path
			f.Problem = problemFolderOffFormat
			f.Detail = fmt.Sprintf("the folder is %q, the series folder format makes %q", have, want.Folder)
			f.Fix = fmt.Sprintf("series_edit path %s with move_files", path.Join(path.Dir(strings.TrimRight(series.Path, "/")), want.Folder))
			out.report(limit, f)
		}
	}

	return out, nil
}

// auditProfiles finds settings that apply to nothing.
func (r *registry) auditProfiles(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	all, err := r.allSeries(ctx)
	if err != nil {
		return auditOut{}, err
	}
	profiles, err := s.qualityProfiles(ctx)
	if err != nil {
		return auditOut{}, err
	}
	details, err := r.client.GetTagDetail(ctx)
	if err != nil {
		return auditOut{}, err
	}
	formats, err := r.client.GetCustomFormat(ctx)
	if err != nil {
		return auditOut{}, err
	}
	release, err := r.client.GetReleaseProfile(ctx)
	if err != nil {
		return auditOut{}, err
	}

	out := auditOut{Scanned: len(profiles) + len(details.Answer) + len(formats.Answer) + len(release.Answer)}
	onSeries := map[sonarr.QualityProfileID]int{}
	for i := range all {
		onSeries[all[i].QualityProfileId]++
	}
	for _, p := range profiles {
		if onSeries[p.Id] == 0 {
			out.report(limit, finding{
				Subject: p.Name, Problem: problemProfileUnused,
				Detail: fmt.Sprintf("no series is on the %s profile", p.Name), Fix: "leave it, or profile_delete it (in the settings toolset) if it will not be wanted",
			})
		}
	}

	for _, d := range details.Answer {
		scoped := map[string]int{
			"delay profiles": len(d.DelayProfileIds), "release profiles": len(d.RestrictionIds), "indexers": len(d.IndexerIds),
			"download clients": len(d.DownloadClientIds),
		}
		used := len(d.SeriesIds) + len(d.ImportListIds) + len(d.NotificationIds) + len(d.AutoTagIds)
		for _, n := range scoped {
			used += n
		}
		switch {
		case used == 0:
			out.report(limit, finding{
				Subject: d.Label, Problem: problemTagUnused,
				Detail: fmt.Sprintf("nothing carries the tag %q", d.Label), Fix: "tag_delete " + d.Label,
			})
		case len(d.SeriesIds) == 0:
			var what []string
			for kind, n := range scoped {
				if n > 0 {
					what = append(what, fmt.Sprintf("%d %s", n, kind))
				}
			}
			if len(what) == 0 {
				continue
			}
			slices.Sort(what)
			out.report(limit, finding{
				Subject: d.Label, Problem: problemTagScopesNothing,
				Detail: fmt.Sprintf("%s apply only to series tagged %q, and no series is, so they apply to nothing", strings.Join(what, " and "), d.Label),
				Fix:    fmt.Sprintf("series_edit add_tags %s on the series they are meant for", d.Label),
			})
		}
	}

	scored := map[int]bool{}
	for _, p := range profiles {
		for _, item := range p.FormatItems {
			if item.Score != 0 {
				scored[item.Format] = true
			}
		}
	}
	for _, cf := range formats.Answer {
		if !scored[cf.Id] {
			out.report(limit, finding{
				Subject: cf.Name, Problem: problemFormatUnscored,
				Detail: fmt.Sprintf("no quality profile gives %q a score, so matching it changes nothing", cf.Name),
				Fix:    "profile_edit a quality profile with custom_format_scores to score it, or customformat_delete it (both in the settings toolset)",
			})
		}
	}

	for _, p := range release.Answer {
		if p.Enabled != nil && !*p.Enabled {
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("release profile %d", p.Id)
			}
			out.report(limit, finding{
				Subject: name, Problem: problemReleaseProfileOff,
				Detail: "the release profile is switched off, so its required and ignored terms do nothing", Fix: "releaseprofile_edit it with enabled true, or releaseprofile_delete it (both in the settings toolset)",
			})
		}
	}

	return out, nil
}

// lowSpace is when a root folder counts as running out of room: under this
// many bytes free, or under lowSpacePercent of its disk.
const (
	lowSpace        = 10 << 30
	lowSpacePercent = 5
)

// auditHealth reports Sonarr's health checks and root folders short of
// space.
func (r *registry) auditHealth(ctx context.Context, _ *snapshot, limit int) (auditOut, error) {
	health, err := r.client.GetHealth(ctx)
	if err != nil {
		return auditOut{}, err
	}
	roots, err := r.client.GetRootFolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	disks, err := r.client.GetDiskSpace(ctx)
	if err != nil {
		return auditOut{}, err
	}

	checks := slices.Clone(health.Answer)
	slices.SortStableFunc(checks, func(a, b sonarr.HealthResource) int { return healthRank(string(a.Type)) - healthRank(string(b.Type)) })
	out := auditOut{Scanned: len(checks) + len(roots.Answer)}
	for _, h := range checks {
		f := finding{Subject: h.Source, Problem: problemHealthCheck + " " + string(h.Type), Detail: h.Message}
		if h.WikiUrl != "" {
			f.Fix = "see " + h.WikiUrl
		}
		out.report(limit, f)
	}

	for _, root := range roots.Answer {
		if !boolv(root.Accessible) {
			out.report(limit, finding{
				Subject: root.Path, Problem: problemRootUnreachable,
				Detail: "Sonarr cannot reach the root folder: a mount gone, or permissions", Fix: "check the mount or the permissions Sonarr runs with",
			})
			continue
		}
		// the disk a root folder is on is the longest disk path containing it
		var disk *sonarr.DiskSpaceResource
		for i := range disks.Answer {
			d := &disks.Answer[i]
			if inFolder(root.Path, d.Path) && (disk == nil || len(d.Path) > len(disk.Path)) {
				disk = d
			}
		}
		free, total := root.FreeSpace, int64(0)
		if disk != nil {
			total = disk.TotalSpace
		}
		if lowOnSpace(free, total) {
			detail := humanSize(free) + " free"
			if total > 0 {
				detail += fmt.Sprintf(" of %s (%d%%)", humanSize(total), free*100/total)
			}
			out.report(limit, finding{
				Subject: root.Path, Problem: problemRootLowOnSpace, Detail: detail + ": imports will start failing when it fills",
				Fix: "free space, or add another root folder with rootfolder_add and move series with series_edit",
			})
		}
	}

	return out, nil
}

// lowOnSpace reports whether free space is under the threshold: an absolute
// floor, or a share of the disk when its size is known.
func lowOnSpace(free, total int64) bool {
	if free < lowSpace {
		return true
	}

	return total > 0 && free*100/total < lowSpacePercent
}

// backupStale is how old the newest backup may be before the audit says
// so: Sonarr backs itself up every seven days unless told otherwise, so a
// newest backup older than that means the schedule is off or failing.
const backupStale = 8 * 24 * time.Hour

// logErrorsSince is how far back the audit reads the log for errors.
const logErrorsSince = 24 * time.Hour

// auditServer reports what is wrong with Sonarr itself rather than the
// library: no backup worth restoring, a release newer than the one running,
// and the errors it has logged in the last day, grouped by what logged them.
func (r *registry) auditServer(ctx context.Context, _ *snapshot, limit int) (auditOut, error) {
	backups, err := r.client.GetSystemBackup(ctx)
	if err != nil {
		return auditOut{}, err
	}
	updates, err := r.client.GetUpdate(ctx)
	if err != nil {
		return auditOut{}, err
	}
	errors, err := r.client.GetLog(ctx, sonarr.GetLogOptions{Page: new(1), PageSize: new(logErrorsPage), SortKey: "time", SortDirection: sonarr.SortDirectionDescending, Level: "error"})
	if err != nil {
		return auditOut{}, err
	}

	out := auditOut{Scanned: 3}
	var newest time.Time
	for _, b := range backups.Answer {
		if t := parseTime(b.Time); t.After(newest) {
			newest = t
		}
	}
	switch {
	case newest.IsZero():
		out.report(limit, finding{Subject: "backups", Problem: problemNoRecentBackup, Detail: "Sonarr holds no backup of its database and settings", Fix: "backup_create (in the admin toolset) makes one now; Sonarr makes one on its own every seven days unless backupInterval in the host section says otherwise"})
	case time.Since(newest) > backupStale:
		out.report(limit, finding{Subject: "backups", Problem: problemNoRecentBackup, Detail: fmt.Sprintf("the newest backup is from %s, %d days ago", day(newest.Format(time.RFC3339)), int(time.Since(newest).Hours()/24)), Fix: "backup_create (in the admin toolset) makes one now; check backupInterval in the host section, and the log for the Backup task failing"})
	}

	for _, u := range updates.Answer {
		if !u.Latest || u.Installed || u.InstalledOn != "" {
			continue
		}
		fix := "server_updates lists what it changes"
		if u.Installable {
			fix += "; Sonarr installs it from its own System page, or on its schedule when Updates are set to automatic in the host section"
		} else {
			fix += "; this Sonarr runs in a container, so it is updated by replacing the image"
		}
		out.report(limit, finding{Subject: u.Version, Problem: problemUpdateAvailable, Detail: fmt.Sprintf("%s was released %s; %s is running", u.Version, day(u.ReleaseDate), runningVersion(updates.Answer)), Fix: fix})
	}

	type logged struct {
		count   int
		newest  sonarr.LogResource
		message string
	}
	byLogger := map[string]*logged{}
	for _, e := range errors.Answer.Records {
		if time.Since(parseTime(e.Time)) > logErrorsSince {
			continue
		}
		l := byLogger[e.Logger]
		if l == nil {
			l = &logged{newest: e, message: e.Message}
			byLogger[e.Logger] = l
		}
		l.count++
	}
	for _, logger := range slices.Sorted(maps.Keys(byLogger)) {
		l := byLogger[logger]
		detail := fmt.Sprintf("%d in the last day, the newest at %s: %s", l.count, l.newest.Time, l.message)
		if l.newest.Exception != "" {
			detail += " (" + firstLine(l.newest.Exception) + ")"
		}
		out.report(limit, finding{Subject: logger, Problem: problemLogErrors, Detail: detail, Fix: "log_list with level error (in the admin toolset) shows each; audit_health, audit_stuck_downloads and audit_failed_downloads say what to do about the ones they know"})
	}

	return out, nil
}

// logErrorsPage is how many of the newest errors the audit reads: enough
// for a day of them, and a page Sonarr answers at once.
const logErrorsPage = 500

// runningVersion is the version an update list says is installed, or
// words for one it does not name.
func runningVersion(updates []sonarr.UpdateResource) string {
	running := "an older release"
	for _, u := range updates {
		if u.Installed || u.InstalledOn != "" {
			running = u.Version
		}
	}

	return running
}
