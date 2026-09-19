//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/fakes/newznab"
)

// release builds a catalogue entry for an episode of a first season.
func release(title string, tvdb, episode int, sizeGB float64, category int) newznab.Release {
	return newznab.Release{
		Title: title, TVDBID: tvdb, Season: 1, Episode: episode, Size: int64(sizeGB * (1 << 30)),
		Category: category, PubDate: time.Now().Add(-48 * time.Hour), Grabs: 12, Group: "FAKE",
	}
}

// catalogue is what the fake indexer offers: Breaking Bad's first season at
// the quality its profile wants, and at qualities it does not, so a search
// has something to grab and something to reject; and one episode of Firefly
// for the RSS feed Sonarr's indexer test reads.
func catalogue() []newznab.Release {
	bb := breakingBad.TvdbID
	return []newznab.Release{
		release("Breaking.Bad.S01E01.Pilot.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 1, 1.6, newznab.CategoryHD),
		release("Breaking.Bad.S01E01.Pilot.720p.HDTV.x264-FAKE", bb, 1, 0.9, newznab.CategoryHD),
		release("Breaking.Bad.S01E01.Pilot.2160p.WEB-DL.DDP5.1.HDR.HEVC-FAKE", bb, 1, 7.5, newznab.CategoryUHD),
		release("Breaking.Bad.S01E02.Cats.in.the.Bag.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 2, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E03.And.the.Bags.in.the.River.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 3, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E04.Cancer.Man.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 4, 1.4, newznab.CategoryHD),
		release("Breaking.Bad.S01E05.Gray.Matter.1080p.WEB-DL.DD5.1.H.264-FAKE", bb, 5, 1.4, newznab.CategoryHD),
		release("Firefly.S01E07.Safe.1080p.WEB-DL.DD5.1.H.264-FAKE", firefly.TvdbID, 7, 1.3, newznab.CategoryHD),
	}
}

// fakeVideo writes a video of black frames and a silent audio track (Sonarr
// will not import a file without one): minutes long, at frame size (e.g.
// 1920x1080) - the real resolution, since Sonarr reads the quality's
// resolution from the video rather than the name. testenv.sh's video() makes
// the library's files the same way.
func fakeVideo(ctx context.Context, path string, minutes int, frame string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil { //nolint:gosec // the container reads it as another user
		return err
	}
	out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", //nolint:gosec // fixed arguments and a path under the test's data dir
		"-f", "lavfi", "-i", "color=c=black:s="+frame+":r=1", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono",
		"-t", strconv.Itoa(minutes*60), "-c:v", "libx264", "-preset", "ultrafast", "-crf", "51", "-g", "3600",
		"-pix_fmt", "yuv420p", "-c:a", "flac", "-compression_level", "0", "-metadata:s:a:0", "language=und", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", path, err, out)
	}

	return os.Chmod(path, 0o666) //nolint:gosec // the container reads it as another user
}
