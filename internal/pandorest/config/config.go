// Package config lists the services pandorest imports and generates, the
// equivalent of Pandora's resource-manager.hcl. Paths are relative to the
// repository root, which is where the make targets run pandorest from.
package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Naming is how operations get their Go method names.
type Naming string

const (
	// PathNaming builds names from the HTTP method and path, for documents
	// with no operationIds (Sonarr's has none).
	PathNaming Naming = "path"
	// OperationIDNaming uses the operationId, for documents whose ids are
	// hand-written and unique.
	OperationIDNaming Naming = "operationId"
)

// Service is one server API.
type Service struct {
	// Name is the -service flag and the definitions directory name.
	Name string
	// Package is the Go package name of the generated SDK.
	Package string
	// Spec is the vendored OpenAPI document.
	Spec string
	// Definitions is where the importer writes and the generator reads.
	Definitions string
	// Output is the generated package directory.
	Output string
	// Naming picks how method names are built.
	Naming Naming
	// TagSuffix is trimmed from tag names to make group names (a document
	// whose tags are LibraryService would trim "Service").
	TagSuffix string
	// PathPrefix is trimmed from a path before a method is named after it,
	// so every Sonarr operation is not named GetApiV3...: GET
	// /api/v3/series/{id} is GetSeriesById. Paths outside it are named in
	// full.
	PathPrefix string
	// Words spells the run-together words of the paths, which name methods
	// with one capital otherwise: Sonarr's /api/v3/episodefile would make
	// GetEpisodefile, and with "episodefile": "EpisodeFile" makes
	// GetEpisodeFile. Keys are lower case; a segment not listed is
	// capitalised as it stands.
	Words map[string]string
	// Auth names the lib/client authorizer the generated New uses.
	Auth string

	// Root is the repository root the paths above are relative to; empty
	// is the working directory.
	Root string
}

// Services is every service, in the order the make targets process them.
var Services = []Service{
	{
		Name:        "sonarr",
		Package:     "sonarr",
		Spec:        "docs/sonarr-openapi.json",
		Definitions: "api-definitions/sonarr",
		Output:      "lib/sonarr",
		Naming:      PathNaming,
		PathPrefix:  "/api/v3",
		Words:       sonarrWords,
		Auth:        "Sonarr",
	},
}

// sonarrWords are the path segments of Sonarr's API that run two or more
// words together, spelled the way its tags and models spell them.
var sonarrWords = map[string]string{
	"autotagging":         "AutoTagging",
	"customfilter":        "CustomFilter",
	"customformat":        "CustomFormat",
	"delayprofile":        "DelayProfile",
	"diskspace":           "DiskSpace",
	"downloadclient":      "DownloadClient",
	"episodefile":         "EpisodeFile",
	"filesystem":          "FileSystem",
	"importlist":          "ImportList",
	"importlistexclusion": "ImportListExclusion",
	"indexerflag":         "IndexerFlag",
	"languageprofile":     "LanguageProfile",
	"manualimport":        "ManualImport",
	"mediacover":          "MediaCover",
	"mediafiles":          "MediaFiles",
	"mediamanagement":     "MediaManagement",
	"qualitydefinition":   "QualityDefinition",
	"qualityprofile":      "QualityProfile",
	"releaseprofile":      "ReleaseProfile",
	"remotepathmapping":   "RemotePathMapping",
	"rootfolder":          "RootFolder",
	"seasonpass":          "SeasonPass",
	"testall":             "TestAll",
}

// Select returns the named services, or all of them for an empty list.
func Select(names string) ([]Service, error) {
	if names == "" {
		return Services, nil
	}
	var out []Service
	for name := range strings.SplitSeq(names, ",") {
		svc, ok := Find(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown service %q (have %s)", name, strings.Join(serviceNames(), ", "))
		}
		out = append(out, svc)
	}

	return out, nil
}

// Find returns a service by name.
func Find(name string) (Service, bool) {
	for _, s := range Services {
		if s.Name == name {
			return s, true
		}
	}

	return Service{}, false
}

// In returns a copy of the service whose files are under root. The paths
// stay as configured, relative to the repository, because they are recorded
// in the definitions and the generated docs; Path resolves them.
func (s Service) In(root string) Service {
	s.Root = root

	return s
}

// Path resolves one of the service's paths under its root.
func (s Service) Path(p string) string { return filepath.Join(s.Root, p) }

func serviceNames() []string {
	out := make([]string, 0, len(Services))
	for _, s := range Services {
		out = append(out, s.Name)
	}

	return out
}
