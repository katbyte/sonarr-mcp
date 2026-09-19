package generator

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
)

// gen holds what every file of one package needs.
type gen struct {
	svc       *definitions.Service
	opts      Options
	models    map[string]*definitions.Model
	constants map[string]*definitions.Constant

	// declared maps each package-level identifier to what declared it
	declared map[string]string
	clashes  []string
}

func newGen(svc *definitions.Service, opts Options) (*gen, error) {
	if svc.Auth != "Sonarr" {
		return nil, fmt.Errorf("%s: unknown Auth %q (want Sonarr)", svc.Name, svc.Auth)
	}
	if svc.Package == "" || !isIdent(svc.Package) {
		return nil, fmt.Errorf("%s: %q is not a package name", svc.Name, svc.Package)
	}

	return &gen{
		svc:       svc,
		opts:      opts,
		models:    svc.Models(),
		constants: svc.Constants(),
		declared:  map[string]string{"Client": "client.go", "New": "client.go"},
	}, nil
}

// declare records a package-level identifier.
func (g *gen) declare(name, by string) {
	if prev, ok := g.declared[name]; ok {
		g.clashes = append(g.clashes, fmt.Sprintf("%s is declared by both %s and %s", name, prev, by))
		return
	}
	g.declared[name] = by
}

func (g *gen) checkIdentifiers() error {
	if len(g.clashes) == 0 {
		return nil
	}
	slices.Sort(g.clashes)

	return fmt.Errorf("%s: the generated identifiers clash:\n  %s", g.svc.Name, strings.Join(g.clashes, "\n  "))
}

// isStruct reports whether a reference names a struct model (not a union
// alias or a constant).
func (g *gen) isStruct(name string) bool {
	m := g.models[name]
	return m != nil && len(m.Union) == 0
}

// goType renders a type. pointer asks for *T for a struct model, which is
// how fields and single results hold them; list and map elements do not.
func (g *gen) goType(t definitions.TypeRef, pointer bool) string {
	switch t.Type {
	case definitions.Boolean:
		return "bool"
	case definitions.Integer:
		return "int"
	case definitions.Integer64:
		return "int64"
	case definitions.Float:
		return "float32"
	case definitions.Double:
		return "float64"
	case definitions.String:
		return "string"
	case definitions.Any:
		return "any"
	case definitions.RawObject:
		return "json.RawMessage"
	case definitions.RawFile:
		return "io.Reader"
	case definitions.List:
		return "[]" + g.goType(*t.NestedItem, false)
	case definitions.Dictionary:
		return "map[string]" + g.goType(*t.NestedItem, false)
	case definitions.Reference:
		if pointer && g.isStruct(t.ReferenceName) {
			return "*" + t.ReferenceName
		}
		return t.ReferenceName
	}

	return "any"
}

// fieldType is how a model holds a field: a struct model by pointer, a
// boolean as *bool, the rest by value.
func (g *gen) fieldType(f *definitions.Field) string {
	if f.Type.Type == definitions.Boolean {
		return "*bool"
	}

	return g.goType(f.Type, true)
}

// fieldTag is a model field's JSON tag. Booleans are *bool with omitempty:
// the servers default many flags to true, so a body must be able to leave one
// unset as well as send an explicit false. Lists and maps are omitzero, so a
// nil one is left out but an empty one is sent, which is how a body clears a
// list. The rest are omitempty.
func fieldTag(f *definitions.Field) string {
	if f.Type.Type == definitions.List || f.Type.Type == definitions.Dictionary {
		return f.JSONName + ",omitzero"
	}

	return f.JSONName + ",omitempty"
}

// fileHeader starts a file: the generated marker, the package clause and
// the imports the body uses.
func (g *gen) fileHeader(body string) string {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n")
	fmt.Fprintf(&b, "package %s\n\n", g.svc.Package)

	var std, ext []string
	for _, imp := range []struct{ path, use string }{
		{"context", "context."},
		{"encoding/json", "json."},
		{"errors", "errors."},
		{"fmt", "fmt."},
		{"io", "io."},
		{"net/http", "http."},
		{"net/http/httptest", "httptest."},
		{"net/url", "url."},
		{"slices", "slices."},
		{"strconv", "strconv."},
		{"strings", "strings."},
		{"testing", "testing."},
	} {
		if usesPackage(body, imp.use) {
			std = append(std, imp.path)
		}
	}
	if usesPackage(body, "client.") {
		ext = append(ext, ClientImport)
	}
	switch len(std) + len(ext) {
	case 0:
	case 1:
		fmt.Fprintf(&b, "import %q\n\n", append(std, ext...)[0])
	default:
		b.WriteString("import (\n")
		for _, p := range std {
			fmt.Fprintf(&b, "\t%q\n", p)
		}
		if len(std) > 0 && len(ext) > 0 {
			b.WriteString("\n")
		}
		for _, p := range ext {
			fmt.Fprintf(&b, "\t%q\n", p)
		}
		b.WriteString(")\n\n")
	}
	b.WriteString(body)

	return b.String()
}

// usesPackage reports whether code refers to a package selector such as
// "json." outside a comment or string, well enough for generated code: the
// selector must follow a character that cannot end an identifier.
func usesPackage(code, selector string) bool {
	for line := range strings.SplitSeq(code, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		for i := 0; ; {
			j := strings.Index(line[i:], selector)
			if j < 0 {
				break
			}
			at := i + j
			if at == 0 || !isIdentRune(rune(line[at-1])) && line[at-1] != '.' && line[at-1] != '"' {
				return true
			}
			i = at + len(selector)
		}
	}

	return false
}

func isIdentRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func isIdent(s string) bool {
	for i, r := range s {
		if !isIdentRune(r) || i == 0 && unicode.IsDigit(r) {
			return false
		}
	}

	return s != ""
}

// comment renders text as Go line comments with the given indent, or nothing
// when the text is blank.
func comment(indent, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString(indent)
		b.WriteString("//")
		if line = strings.TrimRight(line, " \t"); line != "" {
			b.WriteString(" ")
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// firstSentence trims text to its first sentence, for one-line doc comments.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}

	return s
}

func (g *gen) clientFile() string {
	body := fmt.Sprintf(`// Client is a client for the %[1]s API; each of its operations is a method.
// Requests go through Client.Client, the shared base client.
type Client struct {
	Client *client.Client
}

// New returns a client for the %[1]s server at baseURL (scheme and host,
// optionally the URL base it is configured with) that authenticates with its
// API key.
func New(baseURL, apiKey string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("API key is required")
	}
	c, err := client.New(baseURL, client.APIKey(apiKey))
	if err != nil {
		return nil, err
	}

	return &Client{Client: c}, nil
}
`, g.svc.Title)

	return g.fileHeader(body)
}

func (g *gen) docFile() string {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n")
	fmt.Fprintf(&b, "// Package %s is a typed client for %s %s, generated by internal/pandorest\n", g.svc.Package, g.svc.Title, g.svc.APIVersion)
	fmt.Fprintf(&b, "// from the definitions in %s, which were imported from %s.\n", filepath.ToSlash(g.opts.Definitions), g.svc.Source)
	b.WriteString(`//
// # Layout
//
// Every operation of the API is a method on Client, in a file of its own named
// <tag>_method_<operation>.go. Each model is in <tag>_model_<model>.go and each
// tag's enums in <tag>_constants.go; a model or enum more than one tag uses is
// under common_. The requests go through the hand-written base client,
// lib/client.
//
// # Calls
//
// A method takes a context, then the path parameters in path order, then the
// request body (input: the model for a JSON body, or an io.Reader and its
// content type for raw bytes), then, when the operation has query or header
// parameters, a <Name>OperationOptions. Unset options are not sent: zero
// values are skipped, a *bool is sent when set, and a list is sent as one
// parameter per element.
//
// It returns a <Name>OperationResponse holding HttpResponse and, when the
// operation answers JSON, Model. HttpResponse is set whenever the server
// answered, including alongside an error, and its body can be read again. An
// operation that answers a file leaves the body unread in
// HttpResponse.Body for the caller to read and close. A status the operation
// does not document is a *client.StatusError; client.IsNotFound recognises a
// 404.
//
// A list operation that pages by page number and page size also has a
// <Name>Complete method that pages through every result.
//
// Dates are strings: Sonarr sends RFC 3339 in UTC ("2026-09-19T13:00:00Z").
`)
	if len(g.svc.Workarounds) > 0 {
		b.WriteString("//\n// # Workarounds\n//\n// The importer fixed these bugs in the document before generating from it:\n//\n")
		for _, w := range g.svc.Workarounds {
			fmt.Fprintf(&b, "//   - %s\n", w)
		}
	}
	fmt.Fprintf(&b, "package %s\n", g.svc.Package)

	return b.String()
}
