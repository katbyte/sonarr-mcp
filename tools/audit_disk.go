package tools

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// renaming reports whether Sonarr's Rename Episodes setting is on.
func (r *registry) renaming(ctx context.Context) (bool, error) {
	naming, err := r.client.GetConfigNaming(ctx)
	if err != nil {
		return false, err
	}

	return naming.Model != nil && boolv(naming.Model.RenameEpisodes), nil
}

// auditUnmappedFolders lists the folders in the root folders that no series
// lives in.
func (r *registry) auditUnmappedFolders(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	folders, err := r.client.GetRootFolder(ctx)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{}
	for _, f := range folders.Model {
		if s.only() != 0 && !inFolder(s.series[0].Path, f.Path) {
			continue
		}
		out.Scanned++
		for _, u := range f.UnmappedFolders {
			if s.only() != 0 {
				continue // one series has no unmapped folders of its own
			}
			out.report(limit, finding{
				Subject: u.Path, Problem: "folder not in Sonarr",
				Detail: fmt.Sprintf("%s is in the root folder %s but is no series Sonarr knows", u.Name, f.Path),
				Fix:    "series_import with this folder (dry_run first to see the match)",
			})
		}
	}

	return out, nil
}

// auditUntrackedFiles compares each series' folder with the files Sonarr
// records, and asks Sonarr about any video file it is not tracking.
func (r *registry) auditUntrackedFiles(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	out := auditOut{Scanned: len(s.series)}
	for i := range s.series {
		series := &s.series[i]
		disk, err := s.diskOf(ctx, series)
		if err != nil {
			return auditOut{}, err
		}
		if len(disk) == 0 {
			continue
		}
		files, err := s.filesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		tracked := map[string]bool{}
		for _, f := range files {
			tracked[f.Path] = true
		}
		var untracked []string
		for _, p := range disk {
			if !tracked[p] {
				untracked = append(untracked, p)
			}
		}
		if len(untracked) == 0 {
			continue
		}
		// only a folder with something untracked is worth Sonarr parsing
		scanned, err := r.scanFolder(ctx, series.Path)
		if err != nil {
			return auditOut{}, err
		}
		byPath := map[string]*sonarr.ManualImportResource{}
		for j := range scanned {
			byPath[scanned[j].Path] = &scanned[j]
		}
		for _, p := range untracked {
			f := finding{Series: series.Title, SeriesID: series.Id, Subject: strings.TrimPrefix(p, strings.TrimRight(series.Path, "/")+"/")}
			m := byPath[p]
			if m == nil {
				f.Problem, f.Detail = "file not tracked", "Sonarr does not track this file and would not say why"
				f.Fix = "import_scan the series folder"
				out.report(limit, f)
				continue
			}
			c := projectCandidate(m)
			switch {
			case c.Episodes == "":
				f.Problem = "cannot tell which episode"
				f.Detail = "Sonarr cannot read an episode from the name"
				f.Fix = "import_apply naming its episodes, or rename it and series_rescan"
			case c.Importable:
				f.Problem = "not imported"
				f.Detail = fmt.Sprintf("reads as %s %s, and nothing stops it importing", c.Episodes, c.Quality)
				f.Fix = "import_apply, or series_rescan"
			default:
				f.Problem = "rejected"
				f.Detail = fmt.Sprintf("reads as %s %s", c.Episodes, c.Quality)
				f.Fix = "import_apply to import it anyway, or delete it if it is a spare copy"
			}
			if len(c.Rejections) > 0 {
				f.Detail += "; Sonarr says: " + strings.Join(c.Rejections, "; ")
			}
			out.report(limit, f)
		}
	}

	return out, nil
}

// auditMissingFiles finds the files Sonarr records that are not on disk.
func (*registry) auditMissingFiles(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	out := auditOut{Scanned: len(s.series)}
	for i := range s.series {
		series := &s.series[i]
		if series.Statistics == nil || series.Statistics.EpisodeFileCount == 0 {
			continue
		}
		files, err := s.filesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		disk, err := s.diskOf(ctx, series)
		if err != nil {
			return auditOut{}, err
		}
		if len(disk) == 0 {
			out.report(limit, finding{
				Series: series.Title, SeriesID: series.Id, Subject: series.Path, Problem: "series folder gone",
				Detail: fmt.Sprintf("Sonarr records %d files here, and the folder has no video files, or is not there", len(files)),
				Fix:    "series_rescan, which drops the records so the episodes are missing again (series_edit path first if the folder moved)",
			})
			continue
		}
		onDisk := map[string]bool{}
		for _, p := range disk {
			onDisk[p] = true
		}
		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		numbers := episodeNumbers(eps)
		var gone []string
		for _, f := range files {
			if !onDisk[f.Path] {
				gone = append(gone, episodeLabel(f.SeasonNumber, numbers[f.Id]...)+" "+path.Base(f.Path))
			}
		}
		if len(gone) == 0 {
			continue
		}
		slices.Sort(gone)
		out.report(limit, finding{
			Series: series.Title, SeriesID: series.Id, Subject: fmt.Sprintf("%d files", len(gone)), Problem: "files gone from disk",
			Detail: "recorded but not on disk: " + shortList(gone, 8),
			Fix:    "series_rescan, which drops the records so the episodes are missing again",
		})
	}

	return out, nil
}

// auditNaming asks Sonarr, series by series, which files it would rename.
func (r *registry) auditNaming(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	renaming, err := r.renaming(ctx)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{Scanned: len(s.series)}
	// with renaming off Sonarr keeps every name a file arrives with, and its
	// rename preview is empty whatever the names are - so there is nothing
	// to compare against the format, and saying so is the finding
	if !renaming {
		out.report(limit, finding{
			Subject: "Rename Episodes", Problem: "renaming is off",
			Detail: "Sonarr keeps the names files arrive with, so it cannot say which differ from the naming format",
			Fix:    "turn on Rename Episodes in Sonarr's media management settings; this audit then lists the files to rename",
		})
		return out, nil
	}
	for i := range s.series {
		series := &s.series[i]
		if series.Statistics == nil || series.Statistics.EpisodeFileCount == 0 {
			continue
		}
		plan, err := r.client.GetRename(ctx, sonarr.GetRenameOperationOptions{SeriesId: series.Id})
		if err != nil {
			return auditOut{}, err
		}
		if len(plan.Model) == 0 {
			continue
		}
		var samples []string
		for j, p := range plan.Model {
			if j == 3 {
				break
			}
			samples = append(samples, fmt.Sprintf("%s -> %s", path.Base(p.ExistingPath), path.Base(p.NewPath)))
		}
		detail := fmt.Sprintf("%d of %d files are not named to the format: %s", len(plan.Model), series.Statistics.EpisodeFileCount, strings.Join(samples, "; "))
		out.report(limit, finding{
			Series: series.Title, SeriesID: series.Id, Subject: fmt.Sprintf("%d files", len(plan.Model)), Problem: "files not named to the format",
			Detail: detail, Fix: "series_rename (dry_run first to see every rename)",
		})
	}

	return out, nil
}
