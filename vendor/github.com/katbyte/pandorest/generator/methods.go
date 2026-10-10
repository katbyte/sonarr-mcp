package generator

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/pandorest/definitions"
)

// methodFile renders one operation: its response struct, its options struct
// when it has options, the method, and the Complete pager for a list.
func (g *gen) methodFile(o *definitions.Operation) string {
	responseType := o.Name + "OperationResponse"
	optionsType := o.Name + "OperationOptions"
	g.declare(responseType, "operation "+o.Name)

	var b strings.Builder
	g.writeResponse(&b, o, responseType)
	if len(o.Options) > 0 {
		g.declare(optionsType, "operation "+o.Name)
		g.writeOptions(&b, o, optionsType)
	}
	g.writeMethod(&b, o, responseType, optionsType)
	if o.Pageable != nil {
		g.writeComplete(&b, o, optionsType)
	}

	return g.fileHeader(b.String())
}

// responseModel is how the response struct holds the model: pointers for a
// struct, a constant or a primitive, the value for slices, maps and raw JSON.
func (g *gen) responseModel(o *definitions.Operation) (typ string, pointer bool) {
	if o.Response == nil || o.Response.Type.Type == definitions.RawFile {
		return "", false
	}
	t := o.Response.Type
	switch t.Type {
	case definitions.List, definitions.Dictionary, definitions.RawObject, definitions.Any:
		return g.goType(t), false
	case definitions.Reference:
		if m := g.models[t.ReferenceName]; m != nil && len(m.Union) > 0 {
			return t.ReferenceName, false
		}
	default:
	}

	return "*" + g.goType(t), true
}

func (g *gen) writeResponse(b *strings.Builder, o *definitions.Operation, responseType string) {
	fmt.Fprintf(b, "// %s is the result of %s.\n", responseType, o.Name)
	if o.Response != nil && o.Response.Type.Type == definitions.RawFile {
		fmt.Fprintf(b, "//\n// The operation answers %s, left unread in HttpResponse.Body, which\n// the caller must close.\n", o.Response.ContentType)
	}
	fmt.Fprintf(b, "type %s struct {\n\tHttpResponse *http.Response\n", responseType)
	if typ, _ := g.responseModel(o); typ != "" {
		fmt.Fprintf(b, "\tModel %s\n", typ)
	}
	b.WriteString("}\n\n")
}

// optionType is how the options struct holds an option: a pointer for a
// boolean or a number, so false and 0 can be sent (a series' specials are
// season 0, a backdrop is image 0), the plain type for the rest, whose zero
// value is not sent.
func (g *gen) optionType(t definitions.TypeRef) string {
	switch t.Type {
	case definitions.Boolean, definitions.Integer, definitions.Integer64, definitions.Float, definitions.Double:
		return "*" + g.goType(t)
	default:
		return g.goType(t)
	}
}

func (g *gen) writeOptions(b *strings.Builder, o *definitions.Operation, optionsType string) {
	fmt.Fprintf(b, "// %s holds the query and header parameters of %s.\n", optionsType, o.Name)
	fmt.Fprintf(b, "type %s struct {\n", optionsType)
	for i, opt := range o.Options {
		doc := opt.Description
		if opt.Deprecated {
			doc = strings.TrimSpace(doc + "\n\nDeprecated: the document marks this parameter deprecated.")
		}
		if i > 0 && doc != "" {
			b.WriteString("\n")
		}
		b.WriteString(comment("\t", doc))
		fmt.Fprintf(b, "\t%s %s\n", opt.Field, g.optionType(opt.Type))
	}
	b.WriteString("}\n\n")

	for _, in := range []struct{ where, method, typ string }{
		{definitions.InHeader, "ToHeaders", "Headers"},
		{definitions.InQuery, "ToQuery", "QueryParams"},
	} {
		fmt.Fprintf(b, "// %s returns the %s the options set.\n", in.method, strings.ToLower(in.where)+" parameters")
		fmt.Fprintf(b, "func (o %s) %s() *client.%s {\n\tout := client.%s{}\n", optionsType, in.method, in.typ, in.typ)
		for _, opt := range o.Options {
			if opt.In == in.where {
				b.WriteString(appendOption(opt))
			}
		}
		b.WriteString("\treturn &out\n}\n\n")
	}
}

// appendOption renders the statement that adds one option when it is set. A
// list goes comma-separated or as one parameter per element, as the
// document says.
func appendOption(opt definitions.Option) string {
	field := "o." + opt.Field
	var cond, value string
	switch t := opt.Type; t.Type {
	case definitions.Boolean:
		cond, value = field+" != nil", "strconv.FormatBool(*"+field+")"
	case definitions.Integer:
		cond, value = field+" != nil", "strconv.Itoa(*"+field+")"
	case definitions.Integer64:
		cond, value = field+" != nil", "strconv.FormatInt(*"+field+", 10)"
	case definitions.Float:
		cond, value = field+" != nil", "strconv.FormatFloat(float64(*"+field+"), 'f', -1, 32)"
	case definitions.Double:
		cond, value = field+" != nil", "strconv.FormatFloat(*"+field+", 'f', -1, 64)"
	case definitions.List:
		if !opt.CommaSeparated {
			item := "v"
			switch t.NestedItem.Type {
			case definitions.Integer:
				item = "strconv.Itoa(v)"
			case definitions.Integer64:
				item = "strconv.FormatInt(v, 10)"
			case definitions.Reference:
				item = "string(v)"
			default:
			}
			return fmt.Sprintf("\tfor _, v := range %s {\n\t\tout.Append(%q, %s)\n\t}\n", field, opt.Name, item)
		}
		cond, value = "len("+field+") > 0", "client.CSV("+field+")"
	case definitions.Dictionary:
		if opt.DeepObject {
			return fmt.Sprintf("\tfor _, kv := range client.DeepObject(%q, %s) {\n\t\tout.Append(kv[0], kv[1])\n\t}\n", opt.Name, field)
		}
		cond, value = "len("+field+") > 0", "client.JSONObject("+field+")"
	case definitions.Reference:
		cond, value = field+` != ""`, "string("+field+")"
	default:
		cond, value = field+` != ""`, field
	}

	return fmt.Sprintf("\tif %s {\n\t\tout.Append(%q, %s)\n\t}\n", cond, opt.Name, value)
}

// pathExpression renders the path with each placeholder filled from its
// escaped argument.
func pathExpression(o *definitions.Operation) string {
	if len(o.PathParameters) == 0 {
		return fmt.Sprintf("%q", o.Path)
	}
	byName := map[string]definitions.PathParameter{}
	for _, p := range o.PathParameters {
		byName[p.Name] = p
	}
	var format strings.Builder
	var args []string
	rest := o.Path
	for rest != "" {
		open := strings.IndexByte(rest, '{')
		closeIdx := strings.IndexByte(rest, '}')
		if open < 0 || closeIdx < open {
			format.WriteString(strings.ReplaceAll(rest, "%", "%%"))
			break
		}
		format.WriteString(strings.ReplaceAll(rest[:open], "%", "%%"))
		p := byName[rest[open+1:closeIdx]]
		switch p.Type.Type {
		case definitions.Integer, definitions.Integer64:
			format.WriteString("%d")
			args = append(args, p.Argument)
		case definitions.Double:
			format.WriteString("%s")
			args = append(args, "strconv.FormatFloat("+p.Argument+", 'f', -1, 64)")
		case definitions.Reference:
			format.WriteString("%s")
			args = append(args, "url.PathEscape(string("+p.Argument+"))")
		default:
			format.WriteString("%s")
			args = append(args, "url.PathEscape("+p.Argument+")")
		}
		rest = rest[closeIdx+1:]
	}

	return fmt.Sprintf("fmt.Sprintf(%q, %s)", format.String(), strings.Join(args, ", "))
}

// arguments renders the method's parameters after ctx, and the call that
// passes them on.
func (g *gen) arguments(o *definitions.Operation, optionsType string) (params, call []string) {
	for _, p := range o.PathParameters {
		params = append(params, p.Argument+" "+g.goType(p.Type))
		call = append(call, p.Argument)
	}
	if o.Request != nil {
		if o.Request.Type.Type == definitions.RawFile {
			params = append(params, "input io.Reader", "contentType string")
			call = append(call, "input", "contentType")
		} else {
			params = append(params, "input "+g.goType(o.Request.Type))
			call = append(call, "input")
		}
	}
	if len(o.Options) > 0 {
		params = append(params, "options "+optionsType)
		call = append(call, "options")
	}

	return params, call
}

var httpMethods = map[string]string{
	"GET": "http.MethodGet", "POST": "http.MethodPost", "PUT": "http.MethodPut", "DELETE": "http.MethodDelete", "PATCH": "http.MethodPatch",
}

var statusNames = map[int]string{
	http.StatusOK: "http.StatusOK", http.StatusCreated: "http.StatusCreated", http.StatusAccepted: "http.StatusAccepted",
	http.StatusNoContent: "http.StatusNoContent", http.StatusPartialContent: "http.StatusPartialContent",
	http.StatusBadRequest:       "http.StatusBadRequest",
	http.StatusMovedPermanently: "http.StatusMovedPermanently", http.StatusFound: "http.StatusFound", http.StatusSeeOther: "http.StatusSeeOther",
	http.StatusNotModified: "http.StatusNotModified", http.StatusTemporaryRedirect: "http.StatusTemporaryRedirect", http.StatusPermanentRedirect: "http.StatusPermanentRedirect",
}

func (g *gen) writeMethod(b *strings.Builder, o *definitions.Operation, responseType, optionsType string) {
	g.declare("Client."+o.Name, "operation "+o.Name)
	params, _ := g.arguments(o, optionsType)

	fmt.Fprintf(b, "// %s calls %s %s.", o.Name, o.Method, o.Path)
	if s := firstSentence(o.Description); s != "" {
		b.WriteString(" " + s)
	}
	b.WriteString("\n")
	if o.Request != nil && o.Request.Type.Type == definitions.RawFile {
		if strings.Contains(o.Request.ContentType, "*") {
			fmt.Fprintf(b, "//\n// input is sent as contentType, which must name a concrete type: the\n// operation takes %s.\n", o.Request.ContentType)
		} else {
			fmt.Fprintf(b, "//\n// input is sent as contentType, or as %s when that is empty.\n", o.Request.ContentType)
		}
	}
	if o.Deprecated {
		b.WriteString("//\n// Deprecated: the document marks this operation deprecated.\n")
	}
	fmt.Fprintf(b, "func (c Client) %s(%s) (result %s, err error) {\n",
		o.Name, strings.Join(append([]string{"ctx context.Context"}, params...), ", "), responseType)

	b.WriteString("\topts := client.RequestOptions{\n")
	if o.Request != nil {
		fmt.Fprintf(b, "\t\tContentType: %q,\n", o.Request.ContentType)
	}
	b.WriteString("\t\tExpectedStatusCodes: []int{\n")
	for _, code := range o.ExpectedStatusCodes {
		name, ok := statusNames[code]
		if !ok {
			name = strconv.Itoa(code)
		}
		fmt.Fprintf(b, "\t\t\t%s,\n", name)
	}
	b.WriteString("\t\t},\n")
	fmt.Fprintf(b, "\t\tHTTPMethod: %s,\n", httpMethods[o.Method])
	if len(o.Options) > 0 {
		b.WriteString("\t\tOptionsObject: options,\n")
	}
	fmt.Fprintf(b, "\t\tPath: %s,\n", pathExpression(o))
	if o.Response != nil && o.Response.Type.Type == definitions.RawFile {
		b.WriteString("\t\tStreamResponse: true,\n")
	}
	b.WriteString("\t}\n\n")

	b.WriteString("\treq, err := c.Client.NewRequest(ctx, opts)\n\tif err != nil {\n\t\treturn\n\t}\n\n")
	if o.Request != nil {
		if o.Request.Type.Type == definitions.RawFile {
			b.WriteString("\tif err = req.SetBody(input, contentType); err != nil {\n\t\treturn\n\t}\n\n")
		} else {
			b.WriteString("\tif err = req.Marshal(input); err != nil {\n\t\treturn\n\t}\n\n")
		}
	}

	b.WriteString("\tvar resp *client.Response\n\tresp, err = req.Execute(ctx)\n")
	b.WriteString("\tif resp != nil {\n\t\tresult.HttpResponse = resp.Response\n\t}\n")
	b.WriteString("\tif err != nil {\n\t\treturn\n\t}\n\n")

	if typ, pointer := g.responseModel(o); typ != "" {
		if slices.Contains(o.ExpectedStatusCodes, http.StatusNoContent) {
			// a 204 is a null result: no model
			b.WriteString("\tif resp.StatusCode == http.StatusNoContent {\n\t\treturn\n\t}\n\n")
		}
		if pointer {
			fmt.Fprintf(b, "\tvar model %s\n\tresult.Model = &model\n", strings.TrimPrefix(typ, "*"))
			b.WriteString("\tif err = resp.Unmarshal(result.Model); err != nil {\n\t\treturn\n\t}\n\n")
		} else {
			b.WriteString("\tif err = resp.Unmarshal(&result.Model); err != nil {\n\t\treturn\n\t}\n\n")
		}
	}
	b.WriteString("\treturn\n}\n")
}

func (g *gen) writeComplete(b *strings.Builder, o *definitions.Operation, optionsType string) {
	p := o.Pageable
	method := o.Name + "Complete"
	resultType := o.Name + "CompleteResult"
	g.declare(resultType, "operation "+o.Name)
	g.declare("Client."+method, "operation "+o.Name)
	params, call := g.arguments(o, optionsType)

	fmt.Fprintf(b, "\n// %s is every result %s loaded.\n", resultType, method)
	fmt.Fprintf(b, "type %s struct {\n\tLatestHttpResponse *http.Response\n\tItems []%s\n}\n\n", resultType, g.goType(p.ItemType))

	if p.ByPage {
		fmt.Fprintf(b, "// %s calls %s page by page, from the\n", method, o.Name)
		fmt.Fprintf(b, "// page options.%s names (the first when unset), until every result is loaded.\n", p.StartIndexOption)
		fmt.Fprintf(b, "// options.%s is the page size, client.DefaultPageSize when unset.\n", p.LimitOption)
	} else {
		fmt.Fprintf(b, "// %s calls %s page by page from options.%s until every\n", method, o.Name, p.StartIndexOption)
		fmt.Fprintf(b, "// result is loaded. options.%s is the page size, client.DefaultPageSize when\n// unset.\n", p.LimitOption)
	}
	fmt.Fprintf(b, "func (c Client) %s(%s) (result %s, err error) {\n", method, strings.Join(append([]string{"ctx context.Context"}, params...), ", "), resultType)
	fmt.Fprintf(b, "\tlimit := client.DefaultPageSize\n\tif options.%[1]s != nil && *options.%[1]s > 0 {\n\t\tlimit = *options.%[1]s\n\t}\n", p.LimitOption)
	fmt.Fprintf(b, "\toptions.%s = &limit\n", p.LimitOption)
	if p.ByPage {
		fmt.Fprintf(b, "\tnumber := 1\n\tif options.%[1]s != nil && *options.%[1]s > 0 {\n\t\tnumber = *options.%[1]s\n\t}\n", p.StartIndexOption)
		fmt.Fprintf(b, "\toptions.%s = &number\n", p.StartIndexOption)
	} else {
		fmt.Fprintf(b, "\tstart := 0\n\tif options.%[1]s != nil {\n\t\tstart = *options.%[1]s\n\t}\n", p.StartIndexOption)
	}
	b.WriteString("\tfor {\n")
	fmt.Fprintf(b, "\t\tvar page %sOperationResponse\n", o.Name)
	fmt.Fprintf(b, "\t\tpage, err = c.%s(%s)\n", o.Name, strings.Join(append([]string{"ctx"}, call...), ", "))
	b.WriteString("\t\tresult.LatestHttpResponse = page.HttpResponse\n")
	b.WriteString("\t\tif err != nil {\n\t\t\terr = fmt.Errorf(\"loading results: %w\", err)\n\t\t\treturn\n\t\t}\n")
	fmt.Fprintf(b, "\t\tif page.Model == nil || len(page.Model.%s) == 0 {\n\t\t\treturn\n\t\t}\n", p.ItemsField)
	fmt.Fprintf(b, "\t\tresult.Items = append(result.Items, page.Model.%s...)\n", p.ItemsField)
	if p.ByPage {
		b.WriteString("\t\t// a short page is the last; so is the page that reaches the total, when\n")
		b.WriteString("\t\t// the server reports one\n")
		fmt.Fprintf(b, "\t\tif len(page.Model.%[1]s) < limit || page.Model.%[2]s > 0 && number*limit >= page.Model.%[2]s {\n\t\t\treturn\n\t\t}\n", p.ItemsField, p.TotalField)
		// options holds a pointer to number, so the next call asks for the next page
		b.WriteString("\t\tnumber++\n")
	} else {
		fmt.Fprintf(b, "\t\tstart += len(page.Model.%s)\n\t\toptions.%s = &start\n", p.ItemsField, p.StartIndexOption)
		b.WriteString("\t\t// a short page is the last; so is reaching the total, when the server\n")
		b.WriteString("\t\t// reports one (a list may report 0 whatever it holds)\n")
		fmt.Fprintf(b, "\t\tif len(page.Model.%[1]s) < limit || page.Model.%[2]s > 0 && start >= page.Model.%[2]s {\n\t\t\treturn\n\t\t}\n", p.ItemsField, p.TotalField)
	}
	b.WriteString("\t}\n}\n")
}
