package replayproxy

import "encoding/base64"

// Media bodies are never committed: they are elided when recorded and replaced on replay by the smallest valid file of the kind, so the server still
// gets something to decode and save. Both are 2x2 grey, from:
//
//	ffmpeg -f lavfi -i color=c=gray:s=2x2 -frames:v 1 tiny.jpg
var (
	tinyJPEG = mustDecode("/9j/4AAQSkZJRgABAgAAAQABAAD//gAPTGF2YzYzLjEuMTAxAP/bAEMACAQEBAQEBQUFBQUFBgYGBgYGBgYGBgYGBgcHBwgICAcHBwYGBwcICAgICQkJCAgICAkJCgoKDAwLCw4ODhERFP/EAEoAAQAAAAAAAAAAAAAAAAAAAAABAQAAAAAAAAAAAAAAAAAAAAAQAQAAAAAAAAAAAAAAAAAAAAARAQAAAAAAAAAAAAAAAAAAAAD/wAARCAACAAIDASIAAhEAAxEA/9oADAMBAAIRAxEAPwAAD//Z")
	tinyPNG  = mustDecode("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAACXBIWXMAAAABAAAAAQBPJcTWAAAAEklEQVR4nGOsq6tjYGBgYQADABHwAYD8EnVLAAAAAElFTkSuQmCC")
)

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("replayproxy: bad placeholder image: " + err.Error())
	}

	return b
}
