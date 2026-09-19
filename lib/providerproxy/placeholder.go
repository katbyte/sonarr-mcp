package providerproxy

import "encoding/base64"

// Media bodies are never committed: a poster is hundreds of kilobytes and a
// trailer is tens of megabytes, and neither is what these tests are about.
// They are elided at record time and replaced on replay by the smallest valid
// file of the same kind, so the server still gets something it can
// decode and save - which is the behaviour under test - without the
// repository carrying a provider's artwork.
//
// Both are 2x2 grey, produced with:
//
//	ffmpeg -f lavfi -i color=c=gray:s=2x2 -frames:v 1 tiny.jpg
var (
	tinyJPEG = mustDecode("/9j/4AAQSkZJRgABAgAAAQABAAD//gAPTGF2YzYzLjEuMTAxAP/bAEMACAQEBAQEBQUFBQUFBgYGBgYGBgYGBgYGBgcHBwgICAcHBwYGBwcICAgICQkJCAgICAkJCgoKDAwLCw4ODhERFP/EAEoAAQAAAAAAAAAAAAAAAAAAAAABAQAAAAAAAAAAAAAAAAAAAAAQAQAAAAAAAAAAAAAAAAAAAAARAQAAAAAAAAAAAAAAAAAAAAD/wAARCAACAAIDASIAAhEAAxEA/9oADAMBAAIRAxEAPwAAD//Z")
	tinyPNG  = mustDecode("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAACXBIWXMAAAABAAAAAQBPJcTWAAAAEklEQVR4nGOsq6tjYGBgYQADABHwAYD8EnVLAAAAAElFTkSuQmCC")
)

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("providerproxy: bad placeholder image: " + err.Error())
	}

	return b
}
