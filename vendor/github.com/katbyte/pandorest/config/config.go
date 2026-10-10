// Package config describes the services pandorest imports and generates, the
// equivalent of Pandora's resource-manager.hcl. A repository that generates
// an SDK lists its services as Service values and hands them to pandorest.Run;
// paths are relative to the repository root, which is where its make targets
// run the command from.
package config

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// DefaultDir is the directory a service's documents and definitions live in
// when its Dir is empty.
const DefaultDir = "api-defs"

// DefaultClient is the base client a service's generated package is built on
// when its ClientImport is empty: pandorest's own.
const DefaultClient = "github.com/katbyte/pandorest/client"

// Naming is how operations get their Go method names.
type Naming string

const (
	// PathNaming builds names from the HTTP method and path, for documents
	// whose operationIds are machine-made and lossy (Emby's
	// getAudiocodecs, and duplicates).
	PathNaming Naming = "path"
	// OperationIDNaming uses the operationId (Jellyfin's are hand-written
	// and unique).
	OperationIDNaming Naming = "operationId"
)

// Paging says how a service's list operations page, which is how the importer
// tells them from the rest and what the generator builds each one's Complete
// method on: a GET that takes both parameters and answers a model with both
// properties is a list. Names are matched without regard to case.
type Paging struct {
	// Start and Limit are the query parameters: where to start and how many
	// to answer. Start is the index of the first result wanted, from 0
	// (Emby's StartIndex and Limit), unless ByPage says it is a page.
	Start string
	Limit string
	// Items and Total are the properties of the answer: the page's results
	// and how many there are in all (Emby's Items and TotalRecordCount).
	Items string
	Total string
	// ByPage says Start is a page number, counted from 1, rather than an
	// index: Sonarr's page and pageSize, answering records and
	// totalRecords.
	ByPage bool
}

// Service is one server API.
//
// Its document and definitions are versioned by the document's own version
// string: <dir>/<name>-openapi-<version>.json is imported into
// <dir>/<name>-<version>/ beside it, and Resolve picks the highest version
// present, which is the one the SDK is generated from. An older document can
// stay beside it for the diff, and a newer one is picked up by being added.
type Service struct {
	// Name is the -service flag and the prefix of the document and
	// definitions names.
	Name string
	// Package is the Go package name of the generated SDK.
	Package string
	// Output is the generated package directory.
	Output string
	// Dir is the directory the documents and definitions are in; empty is
	// DefaultDir.
	Dir string
	// Naming picks how method names are built.
	Naming Naming
	// TagSuffix is trimmed from tag names to make group names ("Service"
	// on Emby: LibraryService is the Library group).
	TagSuffix string
	// PathPrefix is trimmed from a path before a method is named after it,
	// so that every Sonarr operation is not named GetApiV3...: with /api/v3,
	// GET /api/v3/series/{id} is GetSeriesById. A path outside the prefix is
	// named in full, and the paths themselves keep it.
	PathPrefix string
	// Words spells the run-together words of the paths, which name methods
	// with one capital otherwise: Sonarr's /api/v3/episodefile would make
	// GetEpisodefile, and with "episodefile": "EpisodeFile" makes
	// GetEpisodeFile. Keys are lower case and each value is its key's
	// letters, or the import fails; a segment not listed is capitalised as
	// it stands.
	Words map[string]string
	// ClientImport is the import path of the base client the generated
	// package sends every request through; empty is DefaultClient. A
	// repository that keeps a base client of its own names it here, and it
	// must have what generated code calls (see the README).
	ClientImport string
	// Auth names how the service authenticates, and is recorded in its
	// definitions. The client.go the generator writes by default wraps the
	// token New is given in the base client's authorizer of this name,
	// client.<Auth>(token): APIKey or Bearer on pandorest's own.
	Auth string
	// ClientService names a client.Service the repository declares by hand
	// in the generated package, in a file of its own beside the generated
	// ones: what is the service's own about being talked to. The client.go
	// the generator writes by default makes its client from it; without one
	// the client is for a server of no particular kind.
	ClientService string
	// Credential is what the service calls the credential New is given, as
	// a caller would read it: "API key". The client.go the generator writes
	// by default names New's parameter after it (apiKey) and refuses an
	// empty one in its words ("API key is required"); empty is a token.
	Credential string
	// NewDoc is the documentation of the New the generator writes by
	// default, in place of its own: what the address is on this service,
	// where its credential is found. It starts "New returns", and its lines
	// are kept as they are broken.
	NewDoc string
	// Client, when set, is the Go source of client.go after its APIVersion
	// constant, in place of the default: the Client type, a struct holding
	// the base client in a field named Client, and New(baseURL, token
	// string) (*Client, error), with whatever else the service's client
	// declares (a hosted API's address, say). It refers to the base client
	// as client and declares no imports: the generator writes them, for
	// the packages generated code uses (errors, fmt, strings, strconv,
	// net/http, net/url, io, encoding/json, context, slices), so a source
	// that needs another does not compile. The generated tests hold New to
	// refusing an empty token and an address with no scheme, and to leaving
	// the slash off the end of the address it keeps.
	Client string
	// Notes is a paragraph added to the generated package's documentation,
	// for what is true of the service's every operation and no document
	// says: how it writes its dates, say.
	Notes string
	// Paging says how the service's lists page; nil is a service whose
	// operations get no Complete method.
	Paging *Paging
	// PreferJSON reads a response that lists JSON beside another media type
	// as JSON: Swashbuckle lists application/json, text/json and text/plain
	// for every action ASP.NET's formatters can answer, and the text/plain
	// there is the same JSON. Without it such a response is a file, which
	// is what text/css listed beside JSON is on Jellyfin.
	PreferJSON bool
	// ExpectSameShape counts a status outside 2xx as one an operation
	// expects when the document gives it the success answer's shape: a
	// failure that still answers in full, as Sonarr's test-all answers 400
	// with every provider's result when one of them fails. Without it only
	// the 2xx statuses an operation declares are expected.
	ExpectSameShape bool
	// SendRequired holds the document to its word on required properties: a
	// property a schema lists as required is always sent, an empty string
	// as "" rather than not at all (Radarr refuses host settings saved
	// without theirs). Without it required is not read.
	SendRequired bool
	// WrittenWhole names the object schemas callers read and write back
	// whole - settings objects - whose empty strings and zero numbers the
	// generated models send rather than leave out: the server reads a field
	// left out as null, which is not the value that was read. A name the
	// document does not have fails the import.
	WrittenWhole []string
	// KeepNull names the model fields, as Schema.property, whose null is
	// not their zero, which the SDK holds as pointers where it holds every
	// other number by value: season 0 is the specials and a parental limit
	// of 0 is the strictest, so a season or a limit the server leaves out
	// must not read as either. Each must be a number the document declares
	// nullable, or the import fails naming it.
	KeepNull []string

	// Version, Spec and Definitions are set by Resolve: the document's
	// version, the vendored OpenAPI document, and where the importer writes
	// and the generator reads, the last two relative to the repository.
	Version     string
	Spec        string
	Definitions string

	// Root is the repository root the paths above are relative to; empty
	// is the working directory.
	Root string
}

// Select returns the services named in a comma-separated list, or all of them
// for an empty one.
func Select(services []Service, names string) ([]Service, error) {
	if names == "" {
		return services, nil
	}
	var out []Service
	for name := range strings.SplitSeq(names, ",") {
		svc, ok := Find(services, strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown service %q (have %s)", name, strings.Join(Names(services), ", "))
		}
		out = append(out, svc)
	}

	return out, nil
}

// Find returns a service by name.
func Find(services []Service, name string) (Service, bool) {
	for _, s := range services {
		if s.Name == name {
			return s, true
		}
	}

	return Service{}, false
}

// Names lists the services' names, in order.
func Names(services []Service) []string {
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, s.Name)
	}

	return out
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

// Resolve finds the service's documents under its directory and settles on
// the highest version: Spec is that document and Definitions its definitions
// directory. A service with no document is an error.
func (s Service) Resolve() (Service, error) {
	versions, err := s.Versions()
	if err != nil {
		return s, err
	}
	if len(versions) == 0 {
		return s, fmt.Errorf("%s: no document %s under %q", s.Name, filepath.ToSlash(s.SpecPath("<version>")), cmp.Or(s.Root, "."))
	}
	s.Version = versions[len(versions)-1]
	s.Spec = s.SpecPath(s.Version)
	s.Definitions = s.DefinitionsPath(s.Version)

	return s, nil
}

// Versions lists the versions of the service's vendored documents, lowest
// first.
func (s Service) Versions() ([]string, error) {
	matches, err := filepath.Glob(s.Path(s.SpecPath("*")))
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(base, s.Name+"-openapi-"), ".json"))
	}
	slices.SortFunc(versions, CompareVersions)

	return versions, nil
}

// SpecPath is where the service's document of one version is vendored.
func (s Service) SpecPath(version string) string {
	return filepath.Join(cmp.Or(s.Dir, DefaultDir), s.Name+"-openapi-"+version+".json")
}

// DefinitionsPath is where one version's definitions are checked in.
func (s Service) DefinitionsPath(version string) string {
	return filepath.Join(cmp.Or(s.Dir, DefaultDir), s.Name+"-"+version)
}

// CompareVersions orders two version strings by their dot-separated parts,
// numerically where both parts are numbers (4.10 above 4.9), otherwise as
// text; a version with more parts is above its prefix.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		var c int
		if aErr == nil && bErr == nil {
			c = cmp.Compare(an, bn)
		} else {
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}

	return cmp.Compare(len(as), len(bs))
}
