// Command pandorest generates sdk/sonarr from the Sonarr API document
// vendored under api-defs, with github.com/katbyte/pandorest: the document is
// imported into definitions beside it, fixing what it gets wrong on the way
// (see workarounds), and the client is written from those.
//
// It is run from the repository root, as the make targets do:
//
//	go run ./sdk/pandorest import     # the document -> api-defs/sonarr-<version>/
//	go run ./sdk/pandorest generate   # the definitions -> sdk/sonarr
//	go run ./sdk/pandorest diff       # what a refreshed document changes
//	go run ./sdk/pandorest check      # the definitions match the document, and every operation has a method
package main

import (
	"github.com/katbyte/pandorest"
	"github.com/katbyte/sonarr-mcp/sdk/pandorest/workarounds"
)

func main() {
	pandorest.Main(config())
}

// config is what sonarr-mcp generates its client from.
func config() pandorest.Config {
	return pandorest.Config{Services: workarounds.Services, Workarounds: workarounds.All}
}
