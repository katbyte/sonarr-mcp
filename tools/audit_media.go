package tools

import (
	"context"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The audits that read each file's own streams - what Sonarr's media info
// says about the video, rather than what its name says.

// resolutionClass is the quality resolution a video's frame size belongs to.
// The width decides as much as the height, because a film framed at 2.39:1 is
// 1920x800 and still 1080p.
func resolutionClass(frame string) int {
	w, h, ok := strings.Cut(strings.ToLower(frame), "x")
	if !ok {
		return 0
	}
	width, err1 := strconv.Atoi(strings.TrimSpace(w))
	height, err2 := strconv.Atoi(strings.TrimSpace(h))
	if err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0
	}
	switch {
	case width >= 3200 || height >= 2000:
		return 2160
	case width >= 1800 || height >= 1000:
		return 1080
	case width >= 1200 || height >= 700:
		return 720
	default:
		return 480
	}
}

// sdClass folds the standard definition resolutions together: Sonarr's 576p
// and 480p qualities are both what a DVD or SDTV rip is.
func sdClass(res int) int {
	if res > 0 && res < 720 {
		return 480
	}

	return res
}

// auditQualityMismatch compares the quality each file is recorded as with
// the resolution of its video.
func (*registry) auditQualityMismatch(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
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
		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		numbers := episodeNumbers(eps)
		for _, f := range files {
			if f.Quality == nil || f.Quality.Quality == nil || f.MediaInfo == nil {
				continue
			}
			claimed, actual := sdClass(f.Quality.Quality.Resolution), sdClass(resolutionClass(f.MediaInfo.Resolution))
			if claimed == 0 || actual == 0 || claimed == actual {
				continue
			}
			found := finding{
				Series: series.Title, SeriesID: series.Id, Subject: episodeLabel(f.SeasonNumber, numbers[f.Id]...) + " " + path.Base(f.Path),
				Detail: fmt.Sprintf("recorded as %s, the video is %s (%dp)", qualityName(f.Quality), f.MediaInfo.Resolution, actual),
			}
			if actual < claimed {
				found.Problem = "labelled better than it is"
				found.Fix = fmt.Sprintf("file_edit file %d to its real quality, so Sonarr upgrades it", f.Id)
			} else {
				found.Problem = "labelled worse than it is"
				found.Fix = fmt.Sprintf("file_edit file %d to its real quality, so Sonarr stops trying to upgrade it", f.Id)
			}
			out.report(limit, found)
		}
	}

	return out, nil
}

const (
	defaultRuntimeTolerance = 40
	minRuntimeDiffMinutes   = 3
)

// auditRuntime compares each file's running time with its episodes'.
func (*registry) auditRuntime(ctx context.Context, s *snapshot, limit, tolerance int) (auditOut, error) {
	if tolerance <= 0 {
		tolerance = defaultRuntimeTolerance
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
		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		byFile := map[int][]*sonarr.EpisodeResource{}
		for j := range eps {
			if eps[j].EpisodeFileId > 0 {
				byFile[eps[j].EpisodeFileId] = append(byFile[eps[j].EpisodeFileId], &eps[j])
			}
		}
		for _, f := range files {
			// the specials have no typical length to hold them to
			if f.SeasonNumber == 0 || f.MediaInfo == nil || len(byFile[f.Id]) == 0 {
				continue
			}
			expected := 0
			var numbers []int
			for _, e := range byFile[f.Id] {
				runtime := e.Runtime
				if runtime <= 0 {
					runtime = series.Runtime
				}
				expected += runtime
				numbers = append(numbers, e.EpisodeNumber)
			}
			if expected <= 0 {
				continue
			}
			subject := episodeLabel(f.SeasonNumber, numbers...) + " " + path.Base(f.Path)
			actual, ok := runtimeMinutes(f.MediaInfo.RunTime)
			if !ok || actual <= 0 {
				out.report(limit, finding{
					Series: series.Title, SeriesID: series.Id, Subject: subject, Problem: "no running time",
					Detail: "Sonarr could not read a running time from the file: it may be damaged or not a video",
					Fix:    fmt.Sprintf("episode_search to replace it (with file %d deleted first)", f.Id),
				})
				continue
			}
			diff := math.Abs(actual - float64(expected))
			pct := int(math.Round(diff * 100 / float64(expected)))
			if pct <= tolerance || diff < minRuntimeDiffMinutes {
				continue
			}
			problem := "shorter than it should be"
			if actual > float64(expected) {
				problem = "longer than it should be"
			}
			out.report(limit, finding{
				Series: series.Title, SeriesID: series.Id, Subject: subject, Problem: problem,
				Detail: fmt.Sprintf("runs %.0f minutes, the episode is %d (%d%% off): a truncated download, a sample, or the wrong file", actual, expected, pct),
				Fix:    "release_search for the episode and grab a replacement",
			})
		}
	}

	return out, nil
}

// languageCodes are the ISO 639-2 codes audio tracks are tagged with, for the
// languages a TV library is likeliest to hold, by Sonarr's name for each.
var languageCodes = map[string][]string{
	"english": {"eng", "en"}, "japanese": {"jpn", "ja"}, "german": {"ger", "deu", "de"}, "french": {"fre", "fra", "fr"},
	"spanish": {"spa", "es"}, "italian": {"ita", "it"}, "portuguese": {"por", "pt"}, "dutch": {"dut", "nld", "nl"},
	"swedish": {"swe", "sv"}, "norwegian": {"nor", "nob", "nno", "no"}, "danish": {"dan", "da"}, "finnish": {"fin", "fi"},
	"polish": {"pol", "pl"}, "russian": {"rus", "ru"}, "korean": {"kor", "ko"}, "chinese": {"chi", "zho", "zh"},
	"hindi": {"hin", "hi"}, "arabic": {"ara", "ar"}, "turkish": {"tur", "tr"}, "hebrew": {"heb", "he"},
	"czech": {"cze", "ces", "cs"}, "hungarian": {"hun", "hu"}, "greek": {"gre", "ell", "el"}, "thai": {"tha", "th"},
	"vietnamese": {"vie", "vi"}, "ukrainian": {"ukr", "uk"}, "romanian": {"rum", "ron", "ro"}, "icelandic": {"ice", "isl", "is"},
}

// hasAudioIn reports whether a media info's audio track list ("eng/jpn")
// includes a language, and whether the list names any language at all.
func hasAudioIn(audio, language string) (has, tagged bool) {
	codes := languageCodes[strings.ToLower(language)]
	for tag := range strings.SplitSeq(strings.ToLower(audio), "/") {
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "und" || tag == "unknown" {
			continue
		}
		tagged = true
		if strings.EqualFold(tag, language) {
			has = true
		}
		for _, c := range codes {
			if tag == c {
				has = true
			}
		}
	}

	return has, tagged
}

// auditLanguage checks each file is in the language the series is watched
// in: its original language, unless another is named.
func (*registry) auditLanguage(ctx context.Context, s *snapshot, limit int, language string) (auditOut, error) {
	out := auditOut{Scanned: len(s.series)}
	for i := range s.series {
		series := &s.series[i]
		if series.Statistics == nil || series.Statistics.EpisodeFileCount == 0 {
			continue
		}
		want := language
		if want == "" && series.OriginalLanguage != nil {
			want = series.OriginalLanguage.Name
		}
		if want == "" {
			continue
		}
		files, err := s.filesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		eps, err := s.episodesOf(ctx, series.Id)
		if err != nil {
			return auditOut{}, err
		}
		numbers := episodeNumbers(eps)
		for _, f := range files {
			langs := languageNames(f.Languages)
			recorded := false
			unknown := len(langs) == 0
			for _, l := range langs {
				if strings.EqualFold(l, want) {
					recorded = true
				}
				if strings.EqualFold(l, "unknown") {
					unknown = true
				}
			}
			audio := ""
			if f.MediaInfo != nil {
				audio = f.MediaInfo.AudioLanguages
			}
			hasAudio, tagged := hasAudioIn(audio, want)
			subject := episodeLabel(f.SeasonNumber, numbers[f.Id]...) + " " + path.Base(f.Path)
			switch {
			case tagged && !hasAudio:
				out.report(limit, finding{
					Series: series.Title, SeriesID: series.Id, Subject: subject, Problem: "no audio in the language",
					Detail: fmt.Sprintf("audio tracks are %s, none of them %s; recorded as %s", audio, want, strings.Join(langs, ", ")),
					Fix:    "release_search for the episode and grab a release in " + want,
				})
			case !recorded && !hasAudio && !unknown:
				out.report(limit, finding{
					Series: series.Title, SeriesID: series.Id, Subject: subject, Problem: "recorded in another language",
					Detail: fmt.Sprintf("recorded as %s, not %s, and its audio tracks are not tagged", strings.Join(langs, ", "), want),
					Fix:    fmt.Sprintf("file_edit file %d if the recording is wrong; otherwise release_search for a %s release", f.Id, want),
				})
			case unknown && !hasAudio:
				out.report(limit, finding{
					Series: series.Title, SeriesID: series.Id, Subject: subject, Problem: "language unknown",
					Detail: "Sonarr could not tell the file's language from its name, and its audio tracks are not tagged",
					Fix:    fmt.Sprintf("file_edit file %d to record its language", f.Id),
				})
			}
		}
	}

	return out, nil
}

// mediaAuditSpecs are the audits with an input of their own, run by
// audit_all with their defaults.
func (r *registry) mediaAuditSpecs() []auditSpec {
	return []auditSpec{
		{name: "audit_runtime", run: func(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
			return r.auditRuntime(ctx, s, limit, 0)
		}, note: fmt.Sprintf("more than %d%% off the episode's running time", defaultRuntimeTolerance)},
		{name: "audit_language", run: func(ctx context.Context, s *snapshot, limit int) (auditOut, error) {
			return r.auditLanguage(ctx, s, limit, "")
		}, note: "against each series' original language"},
	}
}

func registerMediaAudits(r *registry) {
	type runtimeIn struct {
		auditIn
		Tolerance int `json:"tolerance_percent,omitempty" jsonschema:"flag files more than this far off the episode's running time, default 40"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_runtime",
		Description: "Episode files whose running time is far off the episode's - a truncated download, a sample imported as the episode, the wrong episode entirely - or that have no running time at all. " +
			"A file holding several episodes is held to their total; the specials are left out. Fix with release_search to replace one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, auditOut, error) {
		s, err := r.snap(ctx, in.Series)
		if err != nil {
			return nil, auditOut{}, err
		}
		out, err := r.auditRuntime(ctx, s, in.Limit, in.Tolerance)

		return nil, out, err
	})

	type languageIn struct {
		auditIn
		Language string `json:"language,omitempty" jsonschema:"the language files should be in, e.g. English; default each series' original language"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_language",
		Description: "Episode files not in the language they should be: audio tracks tagged in other languages only, a file recorded as a dub, or one whose language Sonarr could not tell. " +
			"Checks each series' original language, or one named. Fix with file_edit when the recording is wrong, or release_search for a release in the right language.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in languageIn) (*mcp.CallToolResult, auditOut, error) {
		s, err := r.snap(ctx, in.Series)
		if err != nil {
			return nil, auditOut{}, err
		}
		out, err := r.auditLanguage(ctx, s, in.Limit, in.Language)

		return nil, out, err
	})
}
