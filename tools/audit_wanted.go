package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// queuedEpisodes is the set of episodes the download queue holds.
func (r *registry) queuedEpisodes(ctx context.Context) (map[sonarr.EpisodeID]bool, error) {
	queue, err := r.queueAll(ctx)
	if err != nil {
		return nil, err
	}
	out := map[sonarr.EpisodeID]bool{}
	for _, q := range queue {
		if q.EpisodeId > 0 {
			out[q.EpisodeId] = true
		}
	}

	return out, nil
}

// auditMissingEpisodes finds the seasons with episodes Sonarr wants and has no file for: monitored, aired, in a monitored series. Which seasons those
// are comes from the counts Sonarr keeps for every season, which add up to its own wanted list, and the episodes are read only for the seasons
// listed: the wanted list itself is every missing episode with its series, tens of thousands of rows on a big library.
func (r *registry) auditMissingEpisodes(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	type season struct {
		series *sonarr.SeriesResource
		number int
	}
	var short []season
	for i := range s.series {
		series := &s.series[i]
		if !boolv(series.Monitored) {
			continue
		}
		for _, in := range series.Seasons {
			if in.Statistics != nil && in.Statistics.EpisodeCount > in.Statistics.EpisodeFileCount {
				short = append(short, season{series, in.SeasonNumber})
			}
		}
	}
	slices.SortStableFunc(short, func(a, b season) int {
		if c := strings.Compare(a.series.Title, b.series.Title); c != 0 {
			return c
		}
		return a.number - b.number
	})

	// the series of the seasons that will be listed are read side by side; a season past the limit is counted and not read
	listed := map[sonarr.SeriesID]bool{}
	for _, k := range short[:min(len(short), mcpregistry.LimitOr(limit, 100))] {
		listed[k.series.Id] = true
	}
	if err := s.warm(ctx, reads{episodes: true, of: func(series *sonarr.SeriesResource) bool { return listed[series.Id] }}); err != nil {
		return auditOut{}, err
	}
	queued, err := r.queuedEpisodes(ctx)
	if err != nil {
		return auditOut{}, err
	}

	out := auditOut{Scanned: len(s.series)}
	now := time.Now()
	for _, k := range short {
		if len(out.Findings) >= mcpregistry.LimitOr(limit, 100) {
			out.Found++

			continue
		}

		eps, err := s.episodesOf(ctx, k.series.Id)
		if err != nil {
			return auditOut{}, err
		}
		var missing []*sonarr.EpisodeResource
		allAired := 0
		for i := range eps {
			e := &eps[i]
			if e.SeasonNumber != k.number || !aired(e, now) {
				continue
			}
			allAired++
			if boolv(e.Monitored) && e.EpisodeFileId == 0 && !boolv(e.HasFile) {
				missing = append(missing, e)
			}
		}
		// the counts are Sonarr's from a moment ago: a season filled since then has nothing to say
		if len(missing) == 0 {
			continue
		}
		slices.SortStableFunc(missing, func(a, b *sonarr.EpisodeResource) int { return a.AirDateUtc.Compare(b.AirDateUtc.Time) })

		labels := make([]string, 0, len(missing))
		downloading, searched := 0, 0
		for _, e := range missing {
			labels = append(labels, episodeLabel(e.SeasonNumber, e.EpisodeNumber))
			switch {
			case queued[e.Id]:
				downloading++
			case !e.LastSearchTime.IsZero():
				searched++
			}
		}

		// a whole season missing reads differently from a gap in one
		problem := problemEpisodesMissing
		if len(labels) == allAired && allAired > 1 {
			problem = problemWholeSeasonMissing
		}
		detail := fmt.Sprintf("%d missing: %s", len(labels), shortList(labels, 12))
		if oldest := missing[0].AirDate; !oldest.IsZero() {
			detail += "; oldest aired " + day(oldest.Time)
		}
		var notes []string
		if downloading > 0 {
			notes = append(notes, fmt.Sprintf("%d already downloading", downloading))
		}
		if searched > 0 {
			notes = append(notes, fmt.Sprintf("%d searched for and not found", searched))
		}
		if len(notes) > 0 {
			detail += " (" + strings.Join(notes, ", ") + ")"
		}
		fix := fmt.Sprintf("season_search %d, or episode_search for the gaps", k.number)
		if downloading == len(labels) {
			fix = "nothing yet: every one is downloading"
		}
		out.report(limit, finding{
			Series: k.series.Title, SeriesID: k.series.Id, Subject: fmt.Sprintf("season %d", k.number), Problem: problem, Detail: detail, Fix: fix,
		})
	}

	return out, nil
}

// shortList joins a list, cut to n items with a count of the rest.
func shortList(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}

	return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
}

// auditCutoffUnmet finds, series by series, the files of monitored episodes that fall short of what their profile upgrades until: its quality
// cutoff, as Sonarr's own Cutoff Unmet list has it, or the custom format score it asks for, which that list never looks at. It reads each series'
// files and episodes itself: Sonarr's list is every such episode with its file, hundreds of thousands of rows on a big library.
func (*registry) auditCutoffUnmet(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	profiles, err := s.qualityProfiles(ctx)
	if err != nil {
		return auditOut{}, err
	}
	byID := map[sonarr.QualityProfileID]*sonarr.QualityProfileResource{}
	for i := range profiles {
		byID[profiles[i].Id] = &profiles[i]
	}

	type group struct {
		title, profile, cutoff string
		labels                 []string
		byQuality              map[string]int
		lowScore               int
		// noUpgrades is a profile with Upgrades Allowed off - which Sonarr's
		// own default profiles are - so Sonarr lists these files and will
		// never replace one by itself
		noUpgrades bool
	}
	groups := map[sonarr.SeriesID]*group{}
	var order []sonarr.SeriesID

	// as Sonarr's list does, a series that is not monitored is passed over, and so is one with no files to fall short
	wanted := func(series *sonarr.SeriesResource) bool {
		return boolv(series.Monitored) && hasFiles(series) && byID[series.QualityProfileId] != nil
	}
	if err := s.warm(ctx, reads{files: true, episodes: true, of: wanted}); err != nil {
		return auditOut{}, err
	}
	for i := range s.series {
		series := &s.series[i]
		if !wanted(series) {
			continue
		}
		p := byID[series.QualityProfileId]
		files, err := s.filesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		monitored := map[sonarr.EpisodeFileID]bool{}
		numbers := episodeNumbers(eps)
		for j := range eps {
			if eps[j].EpisodeFileId > 0 && boolv(eps[j].Monitored) {
				monitored[eps[j].EpisodeFileId] = true
			}
		}
		// in the order the episodes run, so the first few named are the first few of the series
		first := func(f *sonarr.EpisodeFileResource) int {
			if of := numbers[f.Id]; len(of) > 0 {
				return slices.Min(of)
			}

			return 0
		}
		files = slices.Clone(files)
		slices.SortStableFunc(files, func(a, b sonarr.EpisodeFileResource) int {
			return cmp.Or(cmp.Compare(a.SeasonNumber, b.SeasonNumber), cmp.Compare(first(&a), first(&b)))
		})
		for _, f := range files {
			quality := boolv(f.QualityCutoffNotMet)
			score := p.CutoffFormatScore > 0 && f.CustomFormatScore < p.CutoffFormatScore
			if !monitored[f.Id] || !quality && !score {
				continue // unwanted, or good enough on both counts
			}

			g := groups[series.Id]
			if g == nil {
				g = &group{title: series.Title, profile: p.Name, cutoff: cutoffName(p), noUpgrades: !boolv(p.UpgradeAllowed), byQuality: map[string]int{}}
				if p.CutoffFormatScore > 0 {
					g.cutoff += fmt.Sprintf(" and custom format score %d", p.CutoffFormatScore)
				}
				groups[series.Id] = g
				order = append(order, series.Id)
			}
			g.byQuality[qualityName(f.Quality)]++
			label := episodeLabel(f.SeasonNumber, numbers[f.Id]...) + " " + qualityName(f.Quality)
			if !quality {
				// good enough on quality, and short of the score alone
				g.lowScore++
				label += fmt.Sprintf(" (score %d)", f.CustomFormatScore)
			}
			g.labels = append(g.labels, label)
		}
	}

	slices.SortStableFunc(order, func(a, b sonarr.SeriesID) int { return strings.Compare(groups[a].title, groups[b].title) })
	out := auditOut{Scanned: len(s.series)}
	for _, id := range order {
		g := groups[id]
		var qualities []string
		for q, n := range g.byQuality {
			qualities = append(qualities, fmt.Sprintf("%d %s", n, q))
		}
		slices.Sort(qualities)
		detail := fmt.Sprintf("%d below %s's cutoff of %s (%s): %s", len(g.labels), g.profile, g.cutoff, strings.Join(qualities, ", "), shortList(g.labels, 10))
		problem := problemBelowQualityCutoff
		if g.lowScore == len(g.labels) {
			problem = problemBelowFormatCutoff
		}
		fix := "series_search, or episode_search for the ones that matter"
		// with upgrades off Sonarr rejects every release for an episode that
		// has a file, searched for by hand or not
		if g.noUpgrades {
			detail += "; the profile has Upgrades Allowed off, so Sonarr will not replace them, on its own or when searched for"
			fix = "profile_edit (in the settings toolset) the profile with upgrade_allowed true, then " + fix
		}
		out.report(limit, finding{
			Series: g.title, SeriesID: id, Subject: fmt.Sprintf("%d files", len(g.labels)), Problem: problem, Detail: detail,
			Fix: fix,
		})
	}

	return out, nil
}

// auditNoFiles lists every series Sonarr records no file for, one finding each, and says which it is: wanted and never fetched, with nothing of it
// monitored, with files in its folder Sonarr does not track, with no episode Sonarr knows of, or with no air dates. A series that has not aired
// yet has nothing to fetch, so it is left out. Which series those are comes from the series list; the episodes and the folder are read only for
// the series listed.
func (r *registry) auditNoFiles(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	out := auditOut{Scanned: len(s.series)}
	now := time.Now()
	listed := map[sonarr.SeriesID]bool{}
	for i := range s.series {
		if !hasFiles(&s.series[i]) && !notAiredYet(&s.series[i], now) && len(listed) < mcpregistry.LimitOr(limit, 100) {
			listed[s.series[i].Id] = true
		}
	}
	if len(listed) == 0 {
		return out, nil
	}

	if err := s.warm(ctx, reads{episodes: true, disk: true, of: func(series *sonarr.SeriesResource) bool { return listed[series.Id] }}); err != nil {
		return auditOut{}, err
	}
	queued, err := r.queuedEpisodes(ctx)
	if err != nil {
		return auditOut{}, err
	}

	for i := range s.series {
		series := &s.series[i]
		if hasFiles(series) || notAiredYet(series, now) {
			continue
		}
		// past the limit a series is counted and not read
		if !listed[series.Id] {
			out.Found++

			continue
		}

		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		disk, err := s.diskOf(ctx, series)
		if err != nil {
			return auditOut{}, err
		}
		if f, ok := noFiles(series, eps, disk, queued, now); ok {
			out.report(limit, f)
		}
	}

	return out, nil
}

// notAiredYet reports whether the series list says a series has not aired: Sonarr calls it upcoming, or its first episode airs later.
func notAiredYet(series *sonarr.SeriesResource, now time.Time) bool {
	return series.Status == sonarr.SeriesStatusTypeUpcoming || series.FirstAired.After(now)
}

// noFiles is the finding for a series with no files: which of the five kinds it is, since each is put right a different way. A series none of
// whose episodes has aired, with one to come, has none: there is nothing to fetch until it airs.
func noFiles(series *sonarr.SeriesResource, eps []sonarr.EpisodeResource, disk []string, queued map[sonarr.EpisodeID]bool, now time.Time) (finding, bool) {
	// a special nobody monitors says nothing about a show that has seasons: one often airs before the show itself does
	seasons := slices.ContainsFunc(eps, func(e sonarr.EpisodeResource) bool { return e.SeasonNumber > 0 })
	known, shown, wanted, downloading := 0, 0, 0, 0
	var next time.Time
	for i := range eps {
		e := &eps[i]
		if seasons && e.SeasonNumber == 0 && !boolv(e.Monitored) {
			continue
		}
		known++
		if !aired(e, now) {
			date := e.AirDateUtc.Time
			if date.IsZero() {
				date = e.AirDate.Time
			}
			if !date.IsZero() && (next.IsZero() || date.Before(next)) {
				next = date
			}

			continue
		}
		shown++
		if boolv(e.Monitored) {
			wanted++
		}
		if boolv(e.Monitored) && queued[e.Id] {
			downloading++
		}
	}

	f := finding{Series: series.Title, SeriesID: series.Id, Subject: series.Path}
	switch {
	case known == 0:
		f.Problem = problemNoEpisodes
		f.Detail = "Sonarr lists no episode of it, so there is nothing for it to fetch"
		f.Fix = "series_refresh, to read the show from TheTVDB again; if it stays empty, series_delete (in the admin toolset) removes it"
		if len(disk) > 0 {
			f.Detail += fmt.Sprintf("; its folder holds %s, which Sonarr has no episode to match to", countOf(len(disk), "video file"))
			f.Fix = "series_refresh, to read the show from TheTVDB again; if it stays empty it may be the wrong show: series_delete (in the admin toolset) leaves the files, and series_import adds the folder as the right one"
		}
	case len(disk) > 0:
		f.Problem = problemNoneTracked
		f.Detail = fmt.Sprintf("its folder holds %s, and Sonarr tracks none", countOf(len(disk), "video file"))
		f.Fix = "import_scan the folder, then import_apply; audit_untracked_files says why each file is not tracked"
	case shown == 0 && !next.IsZero():
		return finding{}, false
	case shown == 0:
		f.Problem = problemNoAirDates
		f.Detail = fmt.Sprintf("Sonarr has an air date for none of its %s, so it never looks for one", countOf(known, "episode"))
		f.Fix = "series_refresh, in case TheTVDB has the dates now; season_search fetches a season without them"
	case !boolv(series.Monitored):
		f.Problem = problemNoFilesUnwanted
		f.Detail = countOf(shown, "episode") + " aired and Sonarr will fetch none: the series is not monitored"
		f.Fix = "series_edit monitored true, then series_search; or series_delete (in the admin toolset) if it is not wanted"
	case wanted == 0:
		f.Problem = problemNoFilesUnwanted
		f.Detail = countOf(shown, "episode") + " aired and Sonarr will fetch none: not one is monitored"
		f.Fix = "season_monitor the seasons wanted, then series_search; or series_delete (in the admin toolset) if it is not wanted"
	default:
		f.Problem = problemNoFiles
		f.Detail = fmt.Sprintf("Sonarr wants %s that aired and has a file for none", countOf(wanted, "episode"))
		f.Fix = "series_search; audit_missing_episodes lists them by season"
		if downloading > 0 {
			f.Detail += fmt.Sprintf(", %d downloading now", downloading)
		}
		if downloading == wanted {
			f.Fix = "nothing yet: every one is downloading"
		}
	}

	if series.Status == sonarr.SeriesStatusTypeDeleted {
		f.Detail += "; TheTVDB has removed the show"
	}
	if added := day(series.Added.Time); added != "" {
		f.Detail += "; added " + added
	}

	return f, true
}
