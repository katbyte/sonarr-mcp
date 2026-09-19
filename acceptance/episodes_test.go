//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

func TestEpisodeList(t *testing.T) {
	out := call(t, "episode_list", map[string]any{"series": firefly.Title, "season": 1})

	eps := rows(t, out["episodes"], "episodes")
	if str(out["series"]) != firefly.Title || len(eps) != 14 || num(t, out["total"], "total") != 14 {
		t.Fatalf("Firefly season 1 = %d episodes: %v", len(eps), out)
	}
	e01 := findRow(t, eps, "episode", "S01E01")
	file := object(t, e01["file"], "file")
	if str(e01["title"]) != "The Train Job" || e01["has_file"] != true || e01["aired"] != true || str(e01["air_date"]) != "2002-09-20" ||
		str(file["quality"]) != "WEBDL-1080p" || file["below_cutoff"] != false || !strings.HasPrefix(str(file["relative_path"]), "Season 1/") {
		t.Errorf("S01E01 = %v", e01)
	}
	if e06 := findRow(t, eps, "episode", "S01E06"); object(t, e06["file"], "file")["below_cutoff"] != true {
		t.Errorf("S01E06 (SDTV) = %v", e06)
	}
	if e07 := findRow(t, eps, "episode", "S01E07"); e07["has_file"] != false || e07["file"] != nil {
		t.Errorf("S01E07 = %v", e07)
	}

	// the missing ones are the aired, monitored ones with no file
	missing := call(t, "episode_list", map[string]any{"series": firefly.Title, "missing": true})
	if n := num(t, missing["total"], "total"); n != 8 {
		t.Errorf("Firefly missing = %d, want E07-E14", n)
	}
	// the specials are season 0, which the option cannot send as a number
	specials := call(t, "episode_list", map[string]any{"series": firefly.Title, "season": 0})
	for _, e := range rows(t, specials["episodes"], "episodes") {
		if !strings.HasPrefix(str(e["episode"]), "S00") {
			t.Errorf("a special is %v", e["episode"])
		}
	}
	if len(rowsOf(specials["episodes"])) == 0 {
		t.Error("Firefly has specials, and none came back")
	}
	// and a limit caps the list, not the count
	capped := call(t, "episode_list", map[string]any{"series": breakingBad.Title, "limit": 3})
	if len(rows(t, capped["episodes"], "episodes")) != 3 || num(t, capped["total"], "total") < 60 {
		t.Errorf("capped = %v of %v", len(rowsOf(capped["episodes"])), capped["total"])
	}
}

func TestEpisodeAndSeasonMonitor(t *testing.T) {
	// unmonitor two of Breaking Bad's episodes, by label and by 1x form
	out := call(t, "episode_monitor", map[string]any{"series": breakingBad.Title, "episodes": []any{"S01E01", "1x02"}, "monitored": false})
	eps := rows(t, out["episodes"], "episodes")
	if len(eps) != 2 {
		t.Fatalf("episode_monitor = %v", out)
	}
	for _, e := range eps {
		if e["monitored"] != false {
			t.Errorf("%v is still monitored", e["episode"])
		}
	}
	t.Cleanup(func() {
		call(t, "episode_monitor", map[string]any{"series": breakingBad.Title, "episodes": []any{"S01E01", "S01E02"}, "monitored": true})
	})
	missing := call(t, "episode_list", map[string]any{"series": breakingBad.Title, "season": 1, "missing": true})
	if n := num(t, missing["total"], "total"); n != 5 {
		t.Errorf("season 1 missing after unmonitoring two = %d, want 5", n)
	}
	for _, bad := range []any{"S09E01", "episode two", ""} {
		callErr(t, "episode_monitor", map[string]any{"series": breakingBad.Title, "episodes": []any{bad}, "monitored": true})
	}

	// a whole season, and every episode in it follows
	season := call(t, "season_monitor", map[string]any{"series": breakingBad.Title, "seasons": []any{5}, "monitored": false})
	if changed := strs(t, season["changed"], "changed"); len(changed) != 1 || changed[0] != "season 5: monitored false" {
		t.Errorf("season_monitor = %v", season)
	}
	t.Cleanup(func() {
		call(t, "season_monitor", map[string]any{"series": breakingBad.Title, "seasons": []any{5}, "monitored": true})
	})
	five := call(t, "episode_list", map[string]any{"series": breakingBad.Title, "season": 5})
	for _, e := range rows(t, five["episodes"], "episodes") {
		if e["monitored"] != false {
			t.Errorf("%v is still monitored after its season was unmonitored", e["episode"])
		}
	}
	// the audit sees the season go
	if f := findings(t, call(t, "audit_missing_episodes", map[string]any{"series": breakingBad.Title}), "subject", "season 5"); len(f) != 0 {
		t.Errorf("season 5 is still missing after unmonitoring it: %v", f)
	}
	// changing nothing changes nothing
	if again := call(t, "season_monitor", map[string]any{"series": breakingBad.Title, "seasons": []any{5}, "monitored": false}); len(rowsOf(again["changed"])) != 0 {
		t.Errorf("a no-op season_monitor changed %v", again["changed"])
	}
	callErr(t, "season_monitor", map[string]any{"series": breakingBad.Title, "seasons": []any{9}, "monitored": true})
	callErr(t, "season_monitor", map[string]any{"series": breakingBad.Title, "seasons": []any{}, "monitored": true})
}

func TestCalendarList(t *testing.T) {
	// the seeded series all aired years ago; a window reaching back over
	// Severance's second season finds it
	out := call(t, "calendar_list", map[string]any{"days": 1, "past_days": 3000})

	eps := rows(t, out["episodes"], "episodes")
	if len(eps) == 0 || str(out["from"]) == "" || str(out["to"]) == "" {
		t.Fatalf("calendar = %v", out)
	}
	found := false
	for _, e := range eps {
		if str(e["series"]) == severance.Title && strings.HasPrefix(str(e["episode"]), "S02") {
			found = true
		}
		if str(e["air_date_utc"]) == "" || numOr0(e["series_id"]) == 0 {
			t.Errorf("a calendar row lacks its date or series: %v", e)
		}
	}
	if !found {
		t.Errorf("Severance's second season is not on the calendar: %v", eps)
	}
	// in air order
	for i := 1; i < len(eps); i++ {
		if str(eps[i]["air_date_utc"]) < str(eps[i-1]["air_date_utc"]) {
			t.Fatalf("not in air order at %d: %v then %v", i, eps[i-1]["air_date_utc"], eps[i]["air_date_utc"])
		}
	}

	// the default window is the week ahead
	week := call(t, "calendar_list", nil)
	if str(week["from"]) >= str(week["to"]) {
		t.Errorf("default window = %v to %v", week["from"], week["to"])
	}
	call(t, "calendar_list", map[string]any{"include_unmonitored": true, "days": 30})
}
