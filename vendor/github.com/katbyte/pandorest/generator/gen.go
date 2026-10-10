package generator

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/katbyte/pandorest/definitions"
)

// gen holds what every file of one package needs.
type gen struct {
	svc       *definitions.Service
	opts      Options
	models    map[string]*definitions.Model
	constants map[string]*definitions.Constant
	// paged is the service's first list operation, which says how its lists
	// page; nil for a service with none
	paged *definitions.Operation

	// declared maps each package-level identifier to what declared it
	declared map[string]string
	clashes  []string
}

func newGen(svc *definitions.Service, opts Options) (*gen, error) {
	if svc.Package == "" || !isIdent(svc.Package) {
		return nil, fmt.Errorf("%s: %q is not a package name", svc.Name, svc.Package)
	}
	if opts.ClientImport == "" {
		return nil, fmt.Errorf("%s: no base client to build the package on: its config names no ClientImport", svc.Name)
	}

	g := &gen{
		svc:       svc,
		opts:      opts,
		models:    svc.Models(),
		constants: svc.Constants(),
		declared:  map[string]string{},
	}
	for _, o := range svc.Operations() {
		if o.Pageable != nil {
			g.paged = o
			break
		}
	}

	if opts.Client != "" && (opts.Credential != "" || opts.NewDoc != "") {
		return nil, fmt.Errorf("%s: its config words the default client.go's New (Credential, NewDoc) and gives a Client of its own as well, which has its own words: drop one or the other", svc.Name)
	}
	if opts.Client == "" {
		// New's parameter must be a name, and not one New already uses
		if argument := credentialArgument(opts.Credential); opts.Credential != "" && (!isIdent(argument) || token.IsKeyword(argument) || slices.Contains([]string{"baseURL", "opts", "c", "err", "client", "errors"}, argument)) {
			return nil, fmt.Errorf("%s: Credential %q makes no name for New's parameter (%q): call it what a caller would, as in \"API key\"", svc.Name, opts.Credential, argument)
		}
		if opts.NewDoc != "" && !strings.HasPrefix(strings.TrimSpace(opts.NewDoc), "New ") {
			return nil, fmt.Errorf("%s: NewDoc must document New, and start with its name: %q", svc.Name, opts.NewDoc)
		}
		if !isIdent(svc.Auth) {
			return nil, fmt.Errorf("%s: Auth %q is not the name of an authorizer of %s, which the default client.go needs; name one, or give the service a Client of its own", svc.Name, svc.Auth, opts.ClientImport)
		}
		if opts.ClientService != "" && !isIdent(opts.ClientService) {
			return nil, fmt.Errorf("%s: ClientService %q is not the name of a value in the package", svc.Name, opts.ClientService)
		}
		g.declare("Client", clientFileName)
		g.declare("New", clientFileName)
		if opts.ClientService != "" {
			// declared by hand beside the generated files, and not to be declared again by a model
			g.declare(opts.ClientService, "the service's own file")
		}

		return g, nil
	}

	names, err := declarations(opts.Client)
	if err != nil {
		return nil, fmt.Errorf("%s: the config's Client is not Go source: %w", svc.Name, err)
	}
	for _, want := range []string{"Client", "New"} {
		if !slices.Contains(names, want) {
			return nil, fmt.Errorf("%s: the config's Client does not declare %s; it must declare the Client type and New", svc.Name, want)
		}
	}
	for _, name := range names {
		g.declare(name, clientFileName)
	}

	return g, nil
}

const clientFileName = "client.go"

// declarations lists what a piece of Go source declares at package level, a
// method as Receiver.Method, which is how the generator names the operations
// it declares on Client.
func declarations(src string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), clientFileName, "package p\n\n"+src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) == 1 {
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				id, ok := recv.(*ast.Ident)
				if !ok {
					return nil, errors.New("method " + name + " has a receiver that is not a plain type")
				}
				name = id.Name + "." + name
			}
			names = append(names, name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					names = append(names, spec.Name.Name)
				case *ast.ValueSpec:
					for _, id := range spec.Names {
						names = append(names, id.Name)
					}
				case *ast.ImportSpec:
					return nil, errors.New("it imports " + spec.Path.Value + "; the generator writes the imports, for the packages it knows")
				}
			}
		}
	}

	return names, nil
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

// goType renders a type as it is written in a list, a map, an argument or an
// option: a struct model by its name.
func (g *gen) goType(t definitions.TypeRef) string {
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
		return "[]" + g.goType(*t.NestedItem)
	case definitions.Dictionary:
		return "map[string]" + g.goType(*t.NestedItem)
	case definitions.Reference:
		return t.ReferenceName
	}

	return "any"
}

// heldType renders a type as a model's field holds it: a struct model by
// pointer, anything else as goType has it.
func (g *gen) heldType(t definitions.TypeRef) string {
	if t.Type == definitions.Reference && g.isStruct(t.ReferenceName) {
		return "*" + t.ReferenceName
	}

	return g.goType(t)
}

// fieldType is how a model holds a field: a struct model by pointer, a
// boolean as *bool, a number whose null is kept (KeepsNull) by pointer, the
// rest by value.
func (g *gen) fieldType(f *definitions.Field) string {
	if f.Type.Type == definitions.Boolean {
		return "*bool"
	}
	if f.KeepsNull {
		return "*" + g.heldType(f.Type)
	}

	return g.heldType(f.Type)
}

// fieldTag is the JSON tag of a model's field. Booleans are *bool with omitempty:
// the servers default many flags to true, so a body must be able to leave one
// unset as well as send an explicit false. Lists and maps are omitzero, so a
// nil one is left out but an empty one is sent, which is how a body clears a
// list. The rest are omitempty, except the strings and numbers of a model
// written back whole (a settings object), which are always sent: read "",
// write "", rather than a null the server does not save. A field the
// document requires is always sent whatever it is, so an empty string goes
// as "" rather than not at all.
func fieldTag(m *definitions.Model, f *definitions.Field) string {
	if f.Required {
		return f.JSONName
	}
	switch f.Type.Type {
	case definitions.List, definitions.Dictionary:
		return f.JSONName + ",omitzero"
	case definitions.String, definitions.Integer, definitions.Integer64, definitions.Float, definitions.Double:
		// a number whose null is kept is a pointer, and its nil is left out
		if m.WrittenWhole && !f.KeepsNull {
			return f.JSONName
		}
	default:
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
		ext = append(ext, g.opts.ClientImport)
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

// apiVersionConsts names the document a package was generated from, so a
// caller can read it at run time and compare it with the server's own.
func (g *gen) apiVersionConsts() string {
	g.declare("APIVersion", clientFileName)
	consts := fmt.Sprintf(`// APIVersion is the version of the API document this package was generated
// from (%s, as the document names itself); a server of another version may
// answer differently.
const APIVersion = %q

`, g.svc.Title, g.svc.APIVersion)
	if g.opts.Version == "" {
		return consts
	}
	g.declare("DocumentVersion", clientFileName)

	return consts + fmt.Sprintf(`// DocumentVersion is the version the document's file is named by: the
// release of the server it was taken from, which is the one to compare with a
// running server's where the document's own version names the API and does not
// move from release to release.
const DocumentVersion = %q

`, g.opts.Version)
}

// defaultClient is the client.go of a service that gives none of its own: the
// Client type, and a New that wraps its credential in the base client's
// authorizer the service's Auth names and makes the client the service's own
// way where it says what that is. Its arguments: the document's title, the
// authorizer, what makes the client (the package's client.Service, or the
// base client itself), New's documentation, its parameter for the credential
// and its refusal of an empty one.
const defaultClient = `// Client is a client for %[1]s; each of its operations is a method.
// Requests go through Client.Client, the shared base client.
type Client struct {
	Client *client.Client
}

%[4]sfunc New(baseURL, %[5]s string, opts ...client.Option) (*Client, error) {
	if %[5]s == "" {
		return nil, errors.New(%[6]q)
	}
	c, err := %[3]s.New(baseURL, client.%[2]s(%[5]s), opts...)
	if err != nil {
		return nil, err
	}

	return &Client{Client: c}, nil
}
`

// defaultNewDoc is the documentation of the default New, for a service that
// words none of its own: the document's title, and the parameter the
// credential is passed in.
const defaultNewDoc = `New returns a client for %[1]s at baseURL (scheme and host, optionally a
path prefix) that authenticates with %[2]s. The options are the base
client's: a logger to trace to, a transport of the caller's own.`

func (g *gen) clientFile() string {
	if g.opts.Client != "" {
		return g.fileHeader(g.apiVersionConsts() + strings.TrimSpace(g.opts.Client) + "\n")
	}

	argument, refusal := "token", "a token is required"
	if g.opts.Credential != "" {
		argument, refusal = credentialArgument(g.opts.Credential), g.opts.Credential+" is required"
	}
	doc := cmp.Or(g.opts.NewDoc, fmt.Sprintf(defaultNewDoc, g.svc.Title, argument))

	return g.fileHeader(g.apiVersionConsts() + fmt.Sprintf(defaultClient, g.svc.Title, g.svc.Auth, cmp.Or(g.opts.ClientService, "client"), comment("", doc), argument, refusal))
}

// credentialArgument is the name of New's parameter for a credential the
// service calls by a name of its own: "API key" is apiKey.
func credentialArgument(credential string) string {
	var b strings.Builder
	for i, word := range strings.FieldsFunc(credential, func(r rune) bool { return !isIdentRune(r) || r == '_' }) {
		if i == 0 {
			b.WriteString(strings.ToLower(word))
			continue
		}
		first, size := utf8.DecodeRuneInString(word)
		b.WriteRune(unicode.ToUpper(first))
		b.WriteString(word[size:])
	}

	return b.String()
}

// docFile is the package's documentation. It is the one generated file
// without the generated header: linters pass over a file that has it, and a
// package with a file written by hand beside the generated ones (the
// service's own, a test) would then be told it has no package comment. Its
// first line says what wrote it instead, which is how Generated knows it.
func (g *gen) docFile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Package %s is a typed client for %s %s, generated by pandorest\n", g.svc.Package, g.svc.Title, g.svc.APIVersion)
	fmt.Fprintf(&b, "// from the definitions in %s, which were imported from %s.\n", filepath.ToSlash(g.opts.Definitions), g.svc.Source)
	fmt.Fprintf(&b, `//
// # Layout
//
// Every operation of the API is a method on Client, in a file of its own named
// <tag>_method_<operation>.go. Each model is in <tag>_model_<model>.go and each
// tag's enums in <tag>_constants.go; a model or enum more than one tag uses is
// under common_. The requests go through the hand-written base client,
// %s.
//
// # Calls
//
// A method takes a context, then the path parameters in path order, then the
// request body (input: the model for a JSON body, or an io.Reader and its
// content type for raw bytes), then, when the operation has query or header
// parameters, a <Name>OperationOptions. Unset options are not sent: a
// boolean or a number is a pointer, sent when set (so 0 and false can be
// asked for), an empty string or list is skipped, and a list is sent
// comma-joined or as one parameter for each element, as the document says.
//
// It returns a <Name>OperationResponse holding HttpResponse and, when the
// operation answers JSON, Model. HttpResponse is set whenever the server
// answered, including alongside an error, and its body can be read again. An
// operation that answers a file leaves the body unread in
// HttpResponse.Body for the caller to read and close. A status the operation
// does not document is an error, and client.StatusCode reads the status from
// it.
`, g.opts.ClientImport)
	if g.paged != nil {
		fmt.Fprintf(&b, "//\n// A list operation, one whose options page with %s and %s, also has a\n// <Name>Complete method that pages through every result.\n", g.paged.Pageable.StartIndexOption, g.paged.Pageable.LimitOption)
	}
	if notes := comment("", g.opts.Notes); notes != "" {
		b.WriteString("//\n")
		b.WriteString(notes)
	}
	if len(g.svc.Workarounds) > 0 {
		b.WriteString("//\n// # Workarounds\n//\n// The importer fixed these bugs in the document before generating from it:\n//\n")
		for _, w := range g.svc.Workarounds {
			fmt.Fprintf(&b, "//   - %s\n", w)
		}
	}
	fmt.Fprintf(&b, "package %s\n", g.svc.Package)

	return b.String()
}
