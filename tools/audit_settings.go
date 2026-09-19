package tools

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
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
			f.Problem = "continuing series not monitored"
			f.Detail = fmt.Sprintf("%s is still %s, and Sonarr will not grab any new episode of it", series.Title, series.Status)
			f.Fix = "series_edit monitored true, if it is still wanted"
			out.report(limit, f)
		case boolv(series.Monitored) && len(seasons) > 0 && !anyMonitored:
			f.Problem = "nothing monitored"
			f.Detail = "the series is monitored but every season is not, so Sonarr searches for nothing"
			f.Fix = "season_monitor the seasons wanted"
			out.report(limit, f)
		case continuing && boolv(series.Monitored) && series.MonitorNewItems == sonarr.NewItemMonitorTypesNone:
			f.Problem = "new seasons will not be monitored"
			f.Detail = "the series is still airing, and a season that appears later will be added unmonitored and never searched for"
			f.Fix = "series_edit monitor_new_items all"
			out.report(limit, f)
		case continuing && boolv(series.Monitored) && anyMonitored && !boolv(seasons[len(seasons)-1].Monitored):
			latest := seasons[len(seasons)-1].SeasonNumber
			f.Subject = fmt.Sprintf("season %d", latest)
			f.Problem = "latest season not monitored"
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
			f.Problem = "anime not typed anime"
			f.Detail = fmt.Sprintf("its genres are %s and it is typed %s, so Sonarr will not read the absolute episode numbers anime releases use", strings.Join(series.Genres, ", "), series.SeriesType)
			f.Fix = "series_edit series_type anime"
			out.report(limit, f)
		}
		if hasGenre(series, "talk show", "news") && series.SeriesType != sonarr.SeriesTypesDaily {
			f := base
			f.Problem = "daily show not typed daily"
			f.Detail = fmt.Sprintf("its genres are %s and it is typed %s, so Sonarr will not match releases named by air date", strings.Join(series.Genres, ", "), series.SeriesType)
			f.Fix = "series_edit series_type daily"
			out.report(limit, f)
		}
		if !slices.ContainsFunc(roots.Model, func(root sonarr.RootFolderResource) bool { return inFolder(series.Path, root.Path) }) {
			f := base
			f.Subject = series.Path
			f.Problem = "outside every root folder"
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
		if err := unmarshalRaw(res.Model, &want); err != nil {
			return auditOut{}, err
		}
		if have := path.Base(strings.TrimRight(series.Path, "/")); want.Folder != "" && have != want.Folder {
			f := base
			f.Subject = series.Path
			f.Problem = "folder not named to the format"
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

	out := auditOut{Scanned: len(profiles) + len(details.Model) + len(formats.Model) + len(release.Model)}
	onSeries := map[int]int{}
	for i := range all {
		onSeries[all[i].QualityProfileId]++
	}
	for _, p := range profiles {
		if onSeries[p.Id] == 0 {
			out.report(limit, finding{
				Subject: p.Name, Problem: "quality profile unused",
				Detail: fmt.Sprintf("no series is on the %s profile", p.Name), Fix: "leave it, or delete it in Sonarr if it will not be wanted",
			})
		}
	}

	for _, d := range details.Model {
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
				Subject: d.Label, Problem: "tag unused",
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
				Subject: d.Label, Problem: "tag scopes settings but no series",
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
	for _, cf := range formats.Model {
		if !scored[cf.Id] {
			out.report(limit, finding{
				Subject: cf.Name, Problem: "custom format scored nowhere",
				Detail: fmt.Sprintf("no quality profile gives %q a score, so matching it changes nothing", cf.Name),
				Fix:    "score it in a quality profile in Sonarr, or delete it",
			})
		}
	}

	for _, p := range release.Model {
		if p.Enabled != nil && !*p.Enabled {
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("release profile %d", p.Id)
			}
			out.report(limit, finding{
				Subject: name, Problem: "release profile disabled",
				Detail: "the release profile is switched off, so its required and ignored terms do nothing", Fix: "enable or delete it in Sonarr",
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

	checks := slices.Clone(health.Model)
	slices.SortStableFunc(checks, func(a, b sonarr.HealthResource) int { return healthRank(string(a.Type)) - healthRank(string(b.Type)) })
	out := auditOut{Scanned: len(checks) + len(roots.Model)}
	for _, h := range checks {
		f := finding{Subject: h.Source, Problem: "health " + string(h.Type), Detail: h.Message}
		if h.WikiUrl != "" {
			f.Fix = "see " + h.WikiUrl
		}
		out.report(limit, f)
	}

	for _, root := range roots.Model {
		if !boolv(root.Accessible) {
			out.report(limit, finding{
				Subject: root.Path, Problem: "root folder unreachable",
				Detail: "Sonarr cannot reach the root folder: a mount gone, or permissions", Fix: "check the mount or the permissions Sonarr runs with",
			})
			continue
		}
		// the disk a root folder is on is the longest disk path containing it
		var disk *sonarr.DiskSpaceResource
		for i := range disks.Model {
			d := &disks.Model[i]
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
				Subject: root.Path, Problem: "root folder low on space", Detail: detail + ": imports will start failing when it fills",
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
