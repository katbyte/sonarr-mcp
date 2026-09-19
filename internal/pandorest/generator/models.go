package generator

import (
	"fmt"
	"strings"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
)

func (g *gen) modelFile(m *definitions.Model) string {
	g.declare(m.Name, "model "+m.Name)

	var b strings.Builder
	switch {
	case len(m.Union) > 0:
		fmt.Fprintf(&b, "// %s is the %q schema, a oneOf/anyOf union kept as raw JSON;\n", m.Name, m.SchemaName)
		fmt.Fprintf(&b, "// decode it into one of: %s.\n", strings.Join(m.Union, ", "))
	case m.SchemaName != "":
		fmt.Fprintf(&b, "// %s is the %q schema.\n", m.Name, m.SchemaName)
	default:
		fmt.Fprintf(&b, "// %s is an object the document declares inline.\n", m.Name)
	}
	if m.Description != "" {
		b.WriteString("//\n")
		b.WriteString(comment("", m.Description))
	}

	switch {
	case len(m.Union) > 0:
		fmt.Fprintf(&b, "type %s = json.RawMessage\n", m.Name)
	case len(m.Fields) == 0:
		fmt.Fprintf(&b, "type %s struct{}\n", m.Name) // gofumpt's spelling of an empty struct
	default:
		fmt.Fprintf(&b, "type %s struct {\n", m.Name)
		for i := range m.Fields {
			f := &m.Fields[i]
			if i > 0 && f.Description != "" {
				b.WriteString("\n")
			}
			b.WriteString(comment("\t", f.Description))
			fmt.Fprintf(&b, "\t%s %s `json:%q`\n", f.Name, g.fieldType(f), fieldTag(f, m.WrittenWhole))
		}
		b.WriteString("}\n")
	}

	return g.fileHeader(b.String())
}

func (g *gen) constantsFile(grp *definitions.Group) string {
	var b strings.Builder
	for i := range grp.Constants {
		c := &grp.Constants[i]
		possible := "PossibleValuesFor" + c.Name
		g.declare(c.Name, "constant "+c.Name)
		g.declare(possible, "constant "+c.Name)

		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "// %s is the %q enum.\n", c.Name, c.SchemaName)
		if c.Description != "" {
			b.WriteString("//\n")
			b.WriteString(comment("", c.Description))
		}
		fmt.Fprintf(&b, "type %s string\n\n", c.Name)

		fmt.Fprintf(&b, "// Values of %s.\nconst (\n", c.Name)
		for _, v := range c.Values {
			g.declare(v.Name, "constant "+c.Name)
			fmt.Fprintf(&b, "\t%s %s = %q\n", v.Name, c.Name, v.Value)
		}
		b.WriteString(")\n\n")

		fmt.Fprintf(&b, "// %s returns every value of %s.\n", possible, c.Name)
		fmt.Fprintf(&b, "func %s() []string {\n\treturn []string{\n", possible)
		for _, v := range c.Values {
			fmt.Fprintf(&b, "\t\tstring(%s),\n", v.Name)
		}
		b.WriteString("\t}\n}\n")
	}

	return g.fileHeader(b.String())
}
