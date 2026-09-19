package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// unmarshalRaw decodes a raw JSON answer.
func unmarshalRaw(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}

	return json.Unmarshal(raw, v)
}

// seasonKey groups episodes by series and season.
type seasonKey struct {
	series int
	season int
}

// queuedEpisodes is the set of episodes the download queue holds.
func (r *registry) queuedEpisodes(ctx context.Context) (map[int]bool, error) {
	queue, err := r.queueAll(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, q := range queue {
		if q.EpisodeId > 0 {
			out[q.EpisodeId] = true
		}
	}

	return out, nil
}

// auditMissingEpisodes groups Sonarr's own wanted list - monitored, aired,
// no file - by series and season.
func (r *registry) auditMissingEpisodes(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	res, err := r.client.GetWantedMissingComplete(ctx, sonarr.GetWantedMissingOperationOptions{
		Monitored: new(true), IncludeSeries: new(true), SortKey: "episodes.airDateUtc", SortDirection: sonarr.SortDirectionAscending,
	})
	if err != nil {
		return auditOut{}, err
	}
	queued, err := r.queuedEpisodes(ctx)
	if err != nil {
		return auditOut{}, err
	}

	type group struct {
		title            string
		labels           []string
		queued, searched int
		oldest           string
		allAired         int
	}
	groups := map[seasonKey]*group{}
	var order []seasonKey
	for i := range res.Items {
		e := &res.Items[i]
		if !s.covers(e.SeriesId) {
			continue
		}
		k := seasonKey{e.SeriesId, e.SeasonNumber}
		g := groups[k]
		if g == nil {
			g = &group{oldest: e.AirDate}
			if e.Series != nil {
				g.title = e.Series.Title
			}
			groups[k] = g
			order = append(order, k)
		}
		g.labels = append(g.labels, episodeLabel(e.SeasonNumber, e.EpisodeNumber))
		switch {
		case queued[e.Id]:
			g.queued++
		case e.LastSearchTime != "":
			g.searched++
		}
	}

	// a whole season missing reads differently from a gap in one, so each
	// group's season is looked at: are all of its aired episodes missing?
	now := time.Now()
	for _, k := range order {
		eps, err := s.episodesOf(ctx, k.series)
		if err != nil {
			return auditOut{}, err
		}
		for i := range eps {
			if eps[i].SeasonNumber == k.season && aired(&eps[i], now) {
				groups[k].allAired++
			}
		}
	}

	slices.SortStableFunc(order, func(a, b seasonKey) int {
		if c := strings.Compare(groups[a].title, groups[b].title); c != 0 {
			return c
		}
		return a.season - b.season
	})
	out := auditOut{Scanned: len(s.series)}
	for _, k := range order {
		g := groups[k]
		problem := "episodes missing"
		if len(g.labels) == g.allAired && g.allAired > 1 {
			problem = "whole season missing"
		}
		detail := fmt.Sprintf("%d missing: %s", len(g.labels), shortList(g.labels, 12))
		if g.oldest != "" {
			detail += "; oldest aired " + g.oldest
		}
		var notes []string
		if g.queued > 0 {
			notes = append(notes, fmt.Sprintf("%d already downloading", g.queued))
		}
		if g.searched > 0 {
			notes = append(notes, fmt.Sprintf("%d searched for and not found", g.searched))
		}
		if len(notes) > 0 {
			detail += " (" + strings.Join(notes, ", ") + ")"
		}
		fix := fmt.Sprintf("season_search %d, or episode_search for the gaps", k.season)
		if g.queued == len(g.labels) {
			fix = "nothing yet: every one is downloading"
		}
		out.report(limit, finding{
			Series: g.title, SeriesID: k.series, Subject: fmt.Sprintf("season %d", k.season), Problem: problem, Detail: detail, Fix: fix,
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

// auditCutoffUnmet groups Sonarr's cutoff-unmet list by series, naming each
// file's quality and the cutoff it falls short of.
func (r *registry) auditCutoffUnmet(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
	res, err := r.client.GetWantedCutoffComplete(ctx, sonarr.GetWantedCutoffOperationOptions{
		Monitored: new(true), IncludeSeries: new(true), IncludeEpisodeFile: new(true),
		SortKey: "episodes.airDateUtc", SortDirection: sonarr.SortDirectionAscending,
	})
	if err != nil {
		return auditOut{}, err
	}
	profiles, err := s.qualityProfiles(ctx)
	if err != nil {
		return auditOut{}, err
	}
	byID := map[int]*sonarr.QualityProfileResource{}
	for i := range profiles {
		byID[profiles[i].Id] = &profiles[i]
	}

	type group struct {
		title, profile, cutoff string
		labels                 []string
		byQuality              map[string]int
		lowScore               int
	}
	groups := map[int]*group{}
	var order []int
	for i := range res.Items {
		e := &res.Items[i]
		if !s.covers(e.SeriesId) {
			continue
		}
		g := groups[e.SeriesId]
		if g == nil {
			g = &group{byQuality: map[string]int{}}
			if e.Series != nil {
				g.title = e.Series.Title
				if p := byID[e.Series.QualityProfileId]; p != nil {
					g.profile, g.cutoff = p.Name, cutoffName(p)
					if p.CutoffFormatScore > 0 {
						g.cutoff += fmt.Sprintf(" and custom format score %d", p.CutoffFormatScore)
					}
				}
			}
			groups[e.SeriesId] = g
			order = append(order, e.SeriesId)
		}
		label := episodeLabel(e.SeasonNumber, e.EpisodeNumber)
		if f := e.EpisodeFile; f != nil {
			q := qualityName(f.Quality)
			g.byQuality[q]++
			label += " " + q
			if !boolv(f.QualityCutoffNotMet) {
				// Sonarr lists it for its custom format score, not its quality
				g.lowScore++
				label += fmt.Sprintf(" (score %d)", f.CustomFormatScore)
			}
		}
		g.labels = append(g.labels, label)
	}

	slices.SortStableFunc(order, func(a, b int) int { return strings.Compare(groups[a].title, groups[b].title) })
	out := auditOut{Scanned: len(s.series)}
	for _, id := range order {
		g := groups[id]
		var qualities []string
		for q, n := range g.byQuality {
			qualities = append(qualities, fmt.Sprintf("%d %s", n, q))
		}
		slices.Sort(qualities)
		detail := fmt.Sprintf("%d below %s's cutoff of %s (%s): %s", len(g.labels), g.profile, g.cutoff, strings.Join(qualities, ", "), shortList(g.labels, 10))
		problem := "below quality cutoff"
		if g.lowScore == len(g.labels) {
			problem = "below custom format cutoff"
		}
		out.report(limit, finding{
			Series: g.title, SeriesID: id, Subject: fmt.Sprintf("%d files", len(g.labels)), Problem: problem, Detail: detail,
			Fix: "series_search, or episode_search for the ones that matter",
		})
	}

	return out, nil
}
