package tools

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
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

// folders is what disk and Sonarr say about each other's folders, read once
// for both folder audits: which series' folders are not there, and which
// folders Sonarr does not know, paired where a folder looks like the series
// that lost its own.
type folders struct {
	// unmapped are the folders Sonarr does not know, by root folder path
	unmapped map[string][]sonarr.UnmappedFolder
	// unreachable are the root folders Sonarr cannot read at all;
	// audit_health reports those, and nothing under them can be judged
	unreachable map[string]bool
	// missing are the series whose own folder is not on disk
	missing map[int]bool
	// movedTo is the folder that looks like a series whose folder is gone,
	// and movedFrom the series such a folder belongs to
	movedTo   map[int]sonarr.UnmappedFolder
	movedFrom map[string]*sonarr.SeriesResource
	// emptyRoots are the root folders that are there but hold none of their
	// series' folders: a drive not mounted, rather than a rename
	emptyRoots map[string]int
}

// folderState reads the state once per snapshot.
func (r *registry) folderState(ctx context.Context, s *snapshot) (*folders, error) {
	s.mu.Lock()
	if s.folderState != nil {
		defer s.mu.Unlock()
		return s.folderState, nil
	}
	s.mu.Unlock()

	roots, err := r.client.GetRootFolder(ctx)
	if err != nil {
		return nil, err
	}
	f := &folders{
		unmapped: map[string][]sonarr.UnmappedFolder{}, unreachable: map[string]bool{}, missing: map[int]bool{},
		movedTo: map[int]sonarr.UnmappedFolder{}, movedFrom: map[string]*sonarr.SeriesResource{}, emptyRoots: map[string]int{},
	}
	var rootPaths []string
	for _, root := range roots.Model {
		p := strings.TrimRight(root.Path, "/")
		rootPaths = append(rootPaths, p)
		if !boolv(root.Accessible) {
			f.unreachable[p] = true
			continue
		}
		f.unmapped[p] = root.UnmappedFolders
	}

	under := map[string]int{} // series per root folder, to spot a root that holds none of them
	for i := range s.series {
		series := &s.series[i]
		// a series with no files has no folder until Sonarr imports one, so
		// there is nothing to have lost
		if series.Path == "" || series.Statistics == nil || series.Statistics.EpisodeFileCount == 0 {
			continue
		}
		root := rootOf(series.Path, rootPaths)
		if f.unreachable[root] {
			continue
		}
		if root != "" {
			under[root]++
		}
		here, err := s.foldersIn(ctx, path.Dir(strings.TrimRight(series.Path, "/")))
		if err != nil {
			return nil, err
		}
		if _, ok := here[strings.ToLower(path.Base(strings.TrimRight(series.Path, "/")))]; ok {
			continue
		}
		f.missing[series.Id] = true
		if root != "" {
			f.emptyRoots[root]++
		}
		if u, ok := matchingFolder(series, f.unmapped[root]); ok {
			f.movedTo[series.Id] = u
			f.movedFrom[strings.ToLower(u.Path)] = series
		}
	}
	for root, n := range f.emptyRoots {
		if n < 2 || n != under[root] {
			delete(f.emptyRoots, root)
		}
	}

	s.mu.Lock()
	s.folderState = f
	s.mu.Unlock()

	return f, nil
}

// rootOf is the root folder a path is in, or "".
func rootOf(p string, roots []string) string {
	for _, root := range roots {
		if inFolder(p, root) {
			return root
		}
	}

	return ""
}

// matchingFolder finds the folder that looks like a series: its title, one of
// its alternate titles, with or without a year in brackets. A folder renamed
// to something else entirely is not guessed at.
func matchingFolder(series *sonarr.SeriesResource, unmapped []sonarr.UnmappedFolder) (sonarr.UnmappedFolder, bool) {
	names := append([]string{series.Title, series.SortTitle}, alternateTitles(series)...)
	want := map[string]bool{}
	for _, n := range names {
		if n == "" {
			continue
		}
		want[normaliseTitle(n)] = true
		want[normaliseTitle(n+" "+strconv.Itoa(series.Year))] = true
	}
	for _, u := range unmapped {
		name := normaliseTitle(u.Name)
		bare := name
		if m := folderYear.FindStringSubmatch(u.Name); m != nil {
			bare = normaliseTitle(m[1])
		}
		if want[name] || want[bare] {
			return u, true
		}
	}

	return sonarr.UnmappedFolder{}, false
}

// auditUnmappedFolders lists the folders in the root folders that no series
// lives in, with what is in each.
func (r *registry) auditUnmappedFolders(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	state, err := r.folderState(ctx, s)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{}
	for _, root := range slices.Sorted(maps.Keys(state.unmapped)) {
		if s.only() != 0 && !inFolder(s.series[0].Path, root) {
			continue
		}
		out.Scanned++
		for _, u := range state.unmapped[root] {
			if s.only() != 0 {
				continue // one series has no unmapped folders of its own
			}
			f := finding{Subject: u.Path}
			if moved := state.movedFrom[strings.ToLower(u.Path)]; moved != nil {
				f.Series, f.SeriesID = moved.Title, moved.Id
				f.Problem = problemFolderOfMovedSeries
				f.Detail = fmt.Sprintf("%s is no series Sonarr knows, and %s - whose own folder %s is not on disk - goes by that name",
					u.Name, moved.Title, moved.Path)
				f.Fix = fmt.Sprintf("series_edit %s path %q, then series_rescan; series_import would add it a second time", moved.Title, u.Path)
				out.report(limit, f)
				continue
			}
			f.Problem = problemFolderUnknown
			f.Detail = fmt.Sprintf("%s is in the root folder %s but is no series Sonarr knows", u.Name, root)
			videos, err := s.videoFiles(ctx, u.Path)
			if err != nil {
				return auditOut{}, err
			}
			f.Detail += "; it holds " + countOf(len(videos), "video file")
			f.Fix = "series_import with this folder (dry_run first to see the match)"
			out.report(limit, f)
		}
	}

	return out, nil
}

// auditMissingFolders finds the series whose folder is not on disk: renamed
// or moved outside Sonarr, or on a drive that is not there.
func (r *registry) auditMissingFolders(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	state, err := r.folderState(ctx, s)
	if err != nil {
		return auditOut{}, err
	}
	out := auditOut{Scanned: len(s.series)}
	for root, n := range state.emptyRoots {
		out.report(limit, finding{
			Subject: root, Problem: problemRootFolderEmpty,
			Detail: fmt.Sprintf("Sonarr can read %s, and not one of the %s under it is there: a drive not mounted, or the library moved", root, countOf(n, "series folder")),
			Fix:    "mount the drive, or point the series at where they are now with series_edit path and series_rescan",
		})
	}
	for i := range s.series {
		series := &s.series[i]
		if !state.missing[series.Id] {
			continue
		}
		if root := rootOf(series.Path, slices.Sorted(maps.Keys(state.emptyRoots))); root != "" {
			continue // counted above, once for the root folder
		}
		f := finding{Series: series.Title, SeriesID: series.Id, Subject: series.Path}
		files := 0
		if series.Statistics != nil {
			files = series.Statistics.EpisodeFileCount
		}
		if u, ok := state.movedTo[series.Id]; ok {
			f.Problem = problemSeriesFolderRenamed
			f.Detail = fmt.Sprintf("%s is not on disk; %s is in the same root folder, is no series Sonarr knows, and goes by the same name", series.Path, u.Path)
			f.Fix = fmt.Sprintf("series_edit path %q (the files stay where they are), then series_rescan", u.Path)
			out.report(limit, f)
			continue
		}
		f.Problem = problemSeriesFolderMissing
		f.Detail = series.Path + " is not on disk, and no folder Sonarr does not know goes by that name"
		if files > 0 {
			f.Detail += "; Sonarr still records " + countOf(files, "file") + " there"
		}
		f.Fix = "series_edit path to where the folder is now, then series_rescan; or series_rescan to drop the file records, if the files are gone for good"
		out.report(limit, f)
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
				f.Problem, f.Detail = problemFileNotTracked, "Sonarr does not track this file and would not say why"
				f.Fix = "import_scan the series folder"
				out.report(limit, f)
				continue
			}
			c := projectCandidate(m)
			switch {
			case c.Episodes == "":
				f.Problem = problemUnreadableName
				f.Detail = "Sonarr cannot read an episode from the name"
				f.Fix = "import_apply naming its episodes, or rename it and series_rescan"
			case c.Importable:
				f.Problem = problemNotImported
				f.Detail = fmt.Sprintf("reads as %s %s, and nothing stops it importing", c.Episodes, c.Quality)
				f.Fix = "import_apply, or series_rescan"
			default:
				f.Problem = problemImportRejected
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
func (r *registry) auditMissingFiles(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	state, err := r.folderState(ctx, s)
	if err != nil {
		return auditOut{}, err
	}
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
			if state.missing[series.Id] {
				continue // the folder itself is gone: audit_missing_folders says so, and where it went
			}
			out.report(limit, finding{
				Series: series.Title, SeriesID: series.Id, Subject: series.Path, Problem: problemFolderEmpty,
				Detail: fmt.Sprintf("the folder is there and empty, and Sonarr records %s in it", countOf(len(files), "file")),
				Fix:    "series_rescan, which drops the records so the episodes are missing again",
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
			Series: series.Title, SeriesID: series.Id, Subject: fmt.Sprintf("%d files", len(gone)), Problem: problemFilesGone,
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
			Subject: "Rename Episodes", Problem: problemRenamingOff,
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
			Series: series.Title, SeriesID: series.Id, Subject: fmt.Sprintf("%d files", len(plan.Model)), Problem: problemNamesOffFormat,
			Detail: detail, Fix: "series_rename (dry_run first to see every rename)",
		})
	}

	return out, nil
}
