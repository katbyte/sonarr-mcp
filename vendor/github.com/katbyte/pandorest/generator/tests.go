package generator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/pandorest/definitions"
)

// The generated tests. Every operation gets a test against a canned server
// that proves the method builds the request its definition describes (method,
// escaped path, every option on the wire, the body and its content type),
// decodes a documented answer, streams a file, and returns an undocumented
// status as an error; a paged list also proves its Complete pager. They test
// the generated code against the definitions, not the server: the
// integration suite's read sweep and bespoke tests do that.

// acceptOption reports whether the operation leaves what it asks for to its
// caller: an Accept option, which a workaround adds and whose value is then
// the whole header.
func acceptOption(o *definitions.Operation) bool {
	return slices.ContainsFunc(o.Options, func(opt definitions.Option) bool {
		return opt.In == definitions.InHeader && strings.EqualFold(opt.Name, "Accept")
	})
}

// sampleText is what the canned server answers an operation that answers
// text: not JSON, and with space a reader must not trim.
const sampleText = "some text, as sent\n"

// undocumentedStatus is a status no operation documents.
const undocumentedStatus = http.StatusTeapot

// testHelpersFile is the generated client_test.go: the canned server the
// operation tests share, the paged one a service with lists has, and New's
// validation.
func (g *gen) testHelpersFile() string {
	helpers := operationServerHelpers
	if g.redirects() {
		// a redirect that is an operation's answer says where it sends its caller, and a client that followed it would ask the canned server twice
		helpers = strings.Replace(helpers, "\t\tw.WriteHeader(status)\n", "\t\tif status >= http.StatusMultipleChoices && status < http.StatusBadRequest {\n\t\t\tw.Header().Set(\"Location\", \"/redirected\")\n\t\t}\n\t\tw.WriteHeader(status)\n", 1)
	}

	return g.fileHeader(helpers + g.pagedServerHelper() + newTest)
}

// redirects reports whether any operation of the service answers a redirect:
// one whose first expected status, the one its test serves, is a redirect.
func (g *gen) redirects() bool {
	return slices.ContainsFunc(g.svc.Operations(), func(o *definitions.Operation) bool {
		return len(o.ExpectedStatusCodes) > 0 && o.ExpectedStatusCodes[0] >= http.StatusMultipleChoices && o.ExpectedStatusCodes[0] < http.StatusBadRequest
	})
}

const operationServerHelpers = `// operationServer is a canned server that answers every request with one
// status, content type and body, and records what it was sent.
type operationServer struct {
	*httptest.Server
	requests []*http.Request
	bodies   []string
}

// newOperationServer starts a canned server for the test and a client for it.
func newOperationServer(t *testing.T, status int, contentType, body string) (*Client, *operationServer) {
	t.Helper()

	s := &operationServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.bodies = append(s.bodies, string(b))
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	c, err := New(s.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	// the server's own client: closing a test server closes the idle
	// connections of http.DefaultTransport, which would break the
	// parallel tests sharing it
	c.Client.HTTPClient = s.Client()

	return c, s
}

// only returns the one request the server received.
func (s *operationServer) only(t *testing.T) (*http.Request, string) {
	t.Helper()

	if len(s.requests) != 1 {
		t.Fatalf("the server received %d requests, want 1", len(s.requests))
	}

	return s.requests[0], s.bodies[0]
}

// expectRequest checks the method and escaped path of a request.
func expectRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()

	if r.Method != method || r.URL.EscapedPath() != path {
		t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.EscapedPath(), method, path)
	}
}

// expectQuery checks the values sent for one query parameter.
func expectQuery(t *testing.T, r *http.Request, name string, want ...string) {
	t.Helper()

	if got := r.URL.Query()[name]; !slices.Equal(got, want) {
		t.Errorf("query %s = %q, want %q", name, got, want)
	}
}

`

// pagedServerHelper is the canned server the Complete tests share, which
// answers pages in the shape the service's lists have: the JSON names of the
// first list operation's items and total. A service with no lists has none.
func (g *gen) pagedServerHelper() string {
	if g.paged == nil {
		return ""
	}
	items, total := g.paged.Pageable.ItemsField, g.paged.Pageable.TotalField
	if m := g.models[g.paged.Response.Type.ReferenceName]; m != nil {
		for i := range m.Fields {
			switch m.Fields[i].Name {
			case g.paged.Pageable.ItemsField:
				items = m.Fields[i].JSONName
			case g.paged.Pageable.TotalField:
				total = m.Fields[i].JSONName
			}
		}
	}
	answer := strconv.Quote(fmt.Sprintf(`{%q:[%%s],%q:%%d}`, items, total))
	records := "start index"
	if g.paged.Pageable.ByPage {
		records = "page"
	}

	return `// pagedServer answers the first pages requests with one item each and an
// empty list after, reporting total items, and records the ` + records + ` of
// each request.
func pagedServer(t *testing.T, startIndex, item string, total, pages int) (*Client, *[]string) {
	t.Helper()

	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		starts = append(starts, r.URL.Query().Get(startIndex))
		w.Header().Set("Content-Type", "application/json")
		if len(starts) > pages {
			item = ""
		}
		_, _ = fmt.Fprintf(w, ` + answer + `, item, total)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.Client.HTTPClient = srv.Client()

	return c, &starts
}

`
}

const newTest = `func TestNew(t *testing.T) {
	t.Parallel()

	if _, err := New("http://server", ""); err == nil {
		t.Error("New without a token succeeded")
	}
	if _, err := New("server", "test-token"); err == nil {
		t.Error("New without a scheme succeeded")
	}
	c, err := New("http://server/", "test-token")
	if err != nil || c.Client.BaseURL != "http://server" {
		t.Errorf("New = %+v, %v", c, err)
	}
}
`

// testFile renders the test of one operation.
func (g *gen) testFile(o *definitions.Operation) string {
	var b strings.Builder
	call := g.testCall(o)

	fmt.Fprintf(&b, "func TestOperation%s(t *testing.T) {\n\tt.Parallel()\n\n", o.Name)

	// the documented answer
	status := o.ExpectedStatusCodes[0]
	contentType, body := g.sampleResponse(o)
	fmt.Fprintf(&b, "\tc, s := newOperationServer(t, %d, %q, %q)\n", status, contentType, body)
	fmt.Fprintf(&b, "\tresult, err := %s\n", call.expr)
	b.WriteString("\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n")
	b.WriteString("\tr, body := s.only(t)\n")
	if o.Request == nil {
		b.WriteString("\t_ = body\n")
	}
	fmt.Fprintf(&b, "\texpectRequest(t, r, %s, %q)\n", httpMethods[o.Method], call.path)
	for _, q := range call.query {
		fmt.Fprintf(&b, "\texpectQuery(t, r, %q, %s)\n", q.name, quoteAll(q.values))
	}
	for _, h := range call.headers {
		fmt.Fprintf(&b, "\tif got := r.Header.Get(%q); got != %q {\n\t\tt.Errorf(\"header %s = %%q, want %%q\", got, %q)\n\t}\n", h.name, h.value, h.name, h.value)
	}
	if asksFor := o.Response.AsksFor(); asksFor != "" && !acceptOption(o) {
		fmt.Fprintf(&b, "\tif got := r.Header.Get(\"Accept\"); !strings.HasPrefix(got, %q) {\n\t\tt.Errorf(\"Accept = %%q, want %s asked for first\", got)\n\t}\n", asksFor, asksFor)
	}
	if o.Request != nil {
		fmt.Fprintf(&b, "\tif got := r.Header.Get(\"Content-Type\"); got != %q {\n\t\tt.Errorf(\"Content-Type = %%q, want %%q\", got, %q)\n\t}\n", call.bodyType, call.bodyType)
		if o.Request.Type.Type == definitions.RawFile {
			b.WriteString("\tif body != \"raw bytes\" {\n\t\tt.Errorf(\"body = %q, want the raw bytes\", body)\n\t}\n")
		} else {
			b.WriteString("\tif !json.Valid([]byte(body)) {\n\t\tt.Errorf(\"body = %q, want JSON\", body)\n\t}\n")
		}
	}
	fmt.Fprintf(&b, "\tif result.HttpResponse == nil || result.HttpResponse.StatusCode != %d {\n\t\tt.Fatalf(\"HttpResponse = %%+v\", result.HttpResponse)\n\t}\n", status)
	b.WriteString(g.responseChecks(o))

	// a null result
	if o.Response != nil && o.Response.Type.Type != definitions.RawFile && status != http.StatusNoContent && slices.Contains(o.ExpectedStatusCodes, http.StatusNoContent) {
		b.WriteString("\n\t// a 204 is a null result, with no model\n")
		b.WriteString("\tc, _ = newOperationServer(t, http.StatusNoContent, \"\", \"\")\n")
		fmt.Fprintf(&b, "\tresult, err = %s\n", call.expr)
		b.WriteString("\tif err != nil || result.Model != nil {\n\t\tt.Errorf(\"a 204 = %v, model %v\", err, result.Model)\n\t}\n")
	}

	// an answer that is not the JSON documented; text is whatever was sent
	if o.Response != nil && o.Response.Type.Type != definitions.RawFile && !o.Response.Text() && status != http.StatusNoContent {
		b.WriteString("\n\t// an answer that does not decode is an error, with the response\n")
		fmt.Fprintf(&b, "\tc, _ = newOperationServer(t, %d, \"application/json\", \"<html>\")\n", status)
		fmt.Fprintf(&b, "\tresult, err = %s\n", call.expr)
		b.WriteString("\tif err == nil || client.StatusCode(err) != 0 || result.HttpResponse == nil {\n\t\tt.Errorf(\"an answer that does not decode = %v\", err)\n\t}\n")
	}

	// an undocumented status
	b.WriteString("\n\t// a status the operation does not document is an error, with the response\n")
	fmt.Fprintf(&b, "\tc, _ = newOperationServer(t, %d, \"text/plain\", \"no\")\n", undocumentedStatus)
	fmt.Fprintf(&b, "\tresult, err = %s\n", call.expr)
	fmt.Fprintf(&b, "\tif client.StatusCode(err) != %d || result.HttpResponse == nil {\n\t\tt.Errorf(\"an undocumented status = %%v, %%+v\", err, result.HttpResponse)\n\t}\n", undocumentedStatus)
	b.WriteString("}\n")

	if o.Pageable != nil {
		b.WriteString(g.completeTest(o, call))
	}

	return g.fileHeader(b.String())
}

// testCallInfo is a call with sample arguments and what it should send.
type testCallInfo struct {
	expr     string // the call expression
	args     []string
	path     string // the escaped path expected
	query    []wireValue
	headers  []wireHeader
	bodyType string
	options  string // the options literal, or ""
	optNames map[string]string
}

type wireValue struct {
	name   string
	values []string
}

type wireHeader struct{ name, value string }

// testCall builds the call with a sample value for every path parameter and
// option, and the request those values should produce.
func (g *gen) testCall(o *definitions.Operation) testCallInfo {
	info := testCallInfo{optNames: map[string]string{}}
	args := []string{"t.Context()"}

	path := o.Path
	for _, p := range o.PathParameters {
		expr, wire := g.samplePath(p)
		args = append(args, expr)
		path = strings.Replace(path, "{"+p.Name+"}", wire, 1)
	}
	info.path = path

	if o.Request != nil {
		if o.Request.Type.Type == definitions.RawFile {
			info.bodyType = concreteType(o.Request.ContentType)
			args = append(args, `strings.NewReader("raw bytes")`, strconv.Quote(info.bodyType))
		} else {
			info.bodyType = o.Request.ContentType
			args = append(args, g.sampleBody(o.Request.Type))
		}
	}

	if len(o.Options) > 0 {
		var fields []string
		for _, opt := range o.Options {
			expr, wire, ok := g.sampleOption(opt)
			if !ok {
				continue
			}
			info.optNames[opt.Field] = opt.Name
			fields = append(fields, opt.Field+": "+expr)
			switch {
			case opt.DeepObject:
				info.query = append(info.query, wireValue{opt.Name + "[a]", []string{"b"}}, wireValue{opt.Name + "[k]", []string{"v"}})
			case opt.In == definitions.InHeader:
				info.headers = append(info.headers, wireHeader{opt.Name, wire[0]})
			default:
				info.query = append(info.query, wireValue{opt.Name, wire})
			}
		}
		info.options = o.Name + "OperationOptions{}"
		if len(fields) > 0 {
			info.options = o.Name + "OperationOptions{\n\t\t" + strings.Join(fields, ",\n\t\t") + ",\n\t}"
		}
		args = append(args, info.options)
	}
	info.args = args
	info.expr = "c." + o.Name + "(" + strings.Join(args, ", ") + ")"

	return info
}

// samplePath is a path argument and how it appears in the escaped path.
func (g *gen) samplePath(p definitions.PathParameter) (expr, wire string) {
	switch p.Type.Type {
	case definitions.Integer, definitions.Integer64:
		return "7", "7"
	case definitions.Float, definitions.Double:
		return "1.5", "1.5"
	case definitions.Reference:
		if c := g.constants[p.Type.ReferenceName]; c != nil && len(c.Values) > 0 {
			return c.Values[0].Name, url.PathEscape(c.Values[0].Value)
		}
	default:
	}
	// a slash in the value proves the argument is escaped
	value := "p/" + p.Argument

	return strconv.Quote(value), url.PathEscape(value)
}

// sampleOption is an option value and what it sends; ok is false for a type
// an option cannot hold.
func (g *gen) sampleOption(opt definitions.Option) (expr string, wire []string, ok bool) {
	switch opt.Type.Type {
	case definitions.Boolean:
		return "new(true)", []string{"true"}, true
	case definitions.Integer:
		return "new(7)", []string{"7"}, true
	case definitions.Integer64:
		return "new(int64(7))", []string{"7"}, true
	case definitions.Float:
		return "new(float32(1.5))", []string{"1.5"}, true
	case definitions.Double:
		return "new(1.5)", []string{"1.5"}, true
	case definitions.String:
		value := "v-" + opt.Field
		return strconv.Quote(value), []string{value}, true
	case definitions.Reference:
		if c := g.constants[opt.Type.ReferenceName]; c != nil && len(c.Values) > 0 {
			return c.Values[0].Name, []string{c.Values[0].Value}, true
		}
		return "", nil, false
	case definitions.Dictionary:
		if opt.DeepObject {
			// two keys, so the order on the wire is proven sorted
			return `map[string]string{"k": "v", "a": "b"}`, nil, true
		}
		return `map[string]string{"k": "v"}`, []string{`{"k":"v"}`}, true
	case definitions.List:
		items, values := g.sampleList(*opt.Type.NestedItem)
		if items == "" {
			return "", nil, false
		}
		if opt.CommaSeparated {
			values = []string{strings.Join(values, ",")}
		}
		return g.goType(opt.Type) + "{" + items + "}", values, true
	default:
		return "", nil, false
	}
}

// sampleList is two list elements and what each sends.
func (g *gen) sampleList(item definitions.TypeRef) (items string, values []string) {
	switch item.Type {
	case definitions.String:
		return `"a", "b"`, []string{"a", "b"}
	case definitions.Integer, definitions.Integer64:
		return "1, 2", []string{"1", "2"}
	case definitions.Reference:
		c := g.constants[item.ReferenceName]
		if c == nil || len(c.Values) == 0 {
			return "", nil
		}
		last := c.Values[len(c.Values)-1]
		return c.Values[0].Name + ", " + last.Name, []string{c.Values[0].Value, last.Value}
	default:
		return "", nil
	}
}

// sampleBody is a request body value.
func (g *gen) sampleBody(t definitions.TypeRef) string {
	switch t.Type {
	case definitions.List, definitions.Dictionary:
		return g.goType(t) + "{}"
	case definitions.Reference:
		if c := g.constants[t.ReferenceName]; c != nil && len(c.Values) > 0 {
			return c.Values[0].Name
		}
		if m := g.models[t.ReferenceName]; m != nil && len(m.Union) > 0 {
			return t.ReferenceName + `("{}")`
		}
		return t.ReferenceName + "{}"
	default:
		return `json.RawMessage("{}")`
	}
}

// concreteType names a concrete media type for a range such as image/*.
func concreteType(contentType string) string {
	switch {
	case !strings.Contains(contentType, "*"):
		return contentType
	case strings.HasPrefix(contentType, "image/"):
		return "image/png"
	case strings.HasPrefix(contentType, "text/"):
		return "text/plain"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio/mpeg"
	case strings.HasPrefix(contentType, "video/"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// sampleResponse is the answer the canned server gives for the operation's
// documented response.
func (g *gen) sampleResponse(o *definitions.Operation) (contentType, body string) {
	switch {
	case o.Response == nil:
		return "", ""
	case o.Response.Type.Type == definitions.RawFile:
		return concreteType(o.Response.ContentType), "file bytes"
	case o.Response.Text():
		// labelled JSON, as a server that sends text may label it: the method reads it as text all the same
		return definitions.JSONContentType, sampleText
	default:
		return "application/json", g.sampleJSON(o.Response.Type)
	}
}

// sampleJSON is a JSON document of a type.
func (g *gen) sampleJSON(t definitions.TypeRef) string {
	switch t.Type {
	case definitions.List:
		return "[" + g.sampleJSON(*t.NestedItem) + "]"
	case definitions.String:
		return `"s"`
	case definitions.Integer, definitions.Integer64:
		return "7"
	case definitions.Float, definitions.Double:
		return "1.5"
	case definitions.Boolean:
		return "true"
	case definitions.Reference:
		if c := g.constants[t.ReferenceName]; c != nil && len(c.Values) > 0 {
			b, _ := json.Marshal(c.Values[0].Value)
			return string(b)
		}
		return "{}"
	default:
		return "{}"
	}
}

// responseChecks checks what the operation decoded or streamed.
func (g *gen) responseChecks(o *definitions.Operation) string {
	switch {
	case o.Response == nil:
		return ""
	case o.Response.Type.Type == definitions.RawFile:
		return "\tstreamed, _ := io.ReadAll(result.HttpResponse.Body)\n\t_ = result.HttpResponse.Body.Close()\n" +
			"\tif string(streamed) != \"file bytes\" {\n\t\tt.Errorf(\"streamed body = %q\", streamed)\n\t}\n"
	}
	typ, pointer := g.responseModel(o)
	switch {
	case o.Response.Text():
		return fmt.Sprintf("\tif result.Model == nil || *result.Model != %q {\n\t\tt.Errorf(\"the text was not held as it was sent: %%v\", result.Model)\n\t}\n", sampleText)
	case pointer:
		return "\tif result.Model == nil {\n\t\tt.Error(\"the model was not decoded\")\n\t}\n"
	case typ == "json.RawMessage" || strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map["):
		return "\tif result.Model == nil {\n\t\tt.Error(\"the model was not decoded\")\n\t}\n"
	default:
		// a union alias or any: decoded as raw JSON
		return "\t_ = result.Model\n"
	}
}

// completeTest proves a paged list's Complete method walks the pages.
func (g *gen) completeTest(o *definitions.Operation, call testCallInfo) string {
	p := o.Pageable
	startName := call.optNames[p.StartIndexOption]
	args := strings.Join(slicesWithout(call.args, call.options), ", ")
	if args != "" {
		args += ", "
	}
	options := func(limit int) string {
		return fmt.Sprintf("%sOperationOptions{%s: new(%d)}", o.Name, p.LimitOption, limit)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nfunc TestOperation%sComplete(t *testing.T) {\n\tt.Parallel()\n\n", o.Name)
	b.WriteString("\t// the three ways a walk ends: the total is reached, an empty page when the\n")
	b.WriteString("\t// server reports no total, and a page shorter than the limit\n")
	b.WriteString("\tfor _, tc := range []struct {\n\t\tname          string\n\t\ttotal, pages  int\n\t\toptions       ")
	fmt.Fprintf(&b, "%sOperationOptions\n\t\titems         int\n\t\tstarts        []string\n\t}{\n", o.Name)
	// a walk by index starts with no index and goes on from the count it
	// has; one by page asks for the first page and then the next
	starts := []string{`"", "1"`, `"", "1", "2"`, `""`}
	if p.ByPage {
		starts = []string{`"1", "2"`, `"1", "2", "3"`, `"1"`}
	}
	fmt.Fprintf(&b, "\t\t{\"total reached\", 2, 9, %s, 2, []string{%s}},\n", options(1), starts[0])
	fmt.Fprintf(&b, "\t\t{\"empty page\", 0, 2, %s, 2, []string{%s}},\n", options(1), starts[1])
	fmt.Fprintf(&b, "\t\t{\"short page\", 0, 9, %s, 1, []string{%s}},\n", options(2), starts[2])
	b.WriteString("\t} {\n\t\tt.Run(tc.name, func(t *testing.T) {\n\t\t\tt.Parallel()\n\n")
	fmt.Fprintf(&b, "\t\t\tc, starts := pagedServer(t, %q, %q, tc.total, tc.pages)\n", startName, g.sampleJSON(p.ItemType))
	fmt.Fprintf(&b, "\t\t\tresult, err := c.%sComplete(%stc.options)\n", o.Name, args)
	b.WriteString("\t\t\tif err != nil {\n\t\t\t\tt.Fatal(err)\n\t\t\t}\n")
	b.WriteString("\t\t\tif len(result.Items) != tc.items || !slices.Equal(*starts, tc.starts) || result.LatestHttpResponse == nil {\n")
	b.WriteString("\t\t\t\tt.Errorf(\"Complete = %d items over pages starting %q, want %d over %q\", len(result.Items), *starts, tc.items, tc.starts)\n")
	b.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n")

	return b.String()
}

func slicesWithout(items []string, drop string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if drop == "" || it != drop {
			out = append(out, it)
		}
	}

	return out
}

func quoteAll(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, strconv.Quote(v))
	}

	return strings.Join(quoted, ", ")
}
