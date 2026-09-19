package differ

import (
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
)

func str() definitions.TypeRef { return definitions.TypeRef{Type: definitions.String} }

func ref(name string) definitions.TypeRef {
	return definitions.TypeRef{Type: definitions.Reference, ReferenceName: name}
}

// base is a small service the tests change one thing at a time.
func base() *definitions.Service {
	return &definitions.Service{
		Name: "mini", Title: "Mini", APIVersion: "1", Workarounds: []string{"mini-old"},
		Groups: []definitions.Group{
			{
				Name: "Items",
				Operations: []definitions.Operation{
					{
						Name: "GetItems", Method: "GET", Path: "/Items", ExpectedStatusCodes: []int{200},
						Options: []definitions.Option{
							{Name: "Limit", Field: "Limit", In: definitions.InQuery, Type: definitions.TypeRef{Type: definitions.Integer}},
							{Name: "Fields", Field: "Fields", In: definitions.InQuery, Type: str()},
						},
						Response: &definitions.Body{ContentType: "application/json", Type: ref("Item")},
						Pageable: &definitions.Pageable{},
					},
					{
						Name: "DeleteItem", Method: "DELETE", Path: "/Items/{Id}", ExpectedStatusCodes: []int{204},
						PathParameters: []definitions.PathParameter{{Name: "Id", Argument: "id", Type: str()}},
					},
				},
				Models: []definitions.Model{{Name: "Item", Fields: []definitions.Field{
					{Name: "Id", JSONName: "Id", Type: str()},
					{Name: "Tags", JSONName: "Tags", Type: definitions.TypeRef{Type: definitions.List, NestedItem: &definitions.TypeRef{Type: definitions.String}}},
				}}},
				Constants: []definitions.Constant{{Name: "Kind", Values: []definitions.ConstantValue{{Name: "KindMovie", Value: "Movie"}, {Name: "KindSeries", Value: "Series"}}}},
			},
		},
	}
}

func TestNoChanges(t *testing.T) {
	t.Parallel()

	r := Diff(base(), base())
	if !r.Empty() || r.Breaking() || r.String() != "mini: no changes\n" {
		t.Errorf("Diff of the same service = %q", r.String())
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()

	newer := base()
	newer.APIVersion = "2"
	newer.Workarounds = []string{"mini-new"}
	items := &newer.Groups[0]
	get := &items.Operations[0]
	get.Options[0].Type = definitions.TypeRef{Type: definitions.Integer64}                                                                   // changed: breaking
	get.Options = append(get.Options[:1], definitions.Option{Name: "SearchTerm", Field: "SearchTerm", In: definitions.InQuery, Type: str()}) // Fields removed, SearchTerm added
	get.ExpectedStatusCodes = []int{200, 204}                                                                                                // widened: not breaking
	items.Operations = items.Operations[:1]                                                                                                  // DeleteItem removed
	items.Operations = append(items.Operations, definitions.Operation{Name: "PostItem", Method: "POST", Path: "/Items", ExpectedStatusCodes: []int{200}})
	items.Models[0].Fields = append(items.Models[0].Fields[:1], definitions.Field{Name: "TagItems", JSONName: "TagItems", Type: str()})
	items.Models[0].WrittenWhole = true // not breaking: a body sends more of what it holds
	items.Constants[0].Values = append(items.Constants[0].Values[:1], definitions.ConstantValue{Name: "KindEpisode", Value: "Episode"})
	newer.Groups = append(newer.Groups, definitions.Group{Name: "Other", Models: []definitions.Model{{Name: "Extra"}}})

	r := Diff(base(), newer)
	if r.Empty() || !r.Breaking() {
		t.Fatalf("Diff = %q, want breaking changes", r.String())
	}
	got := r.String()
	for _, want := range []string{
		"mini: 2 added, 1 removed, 3 changed (4 breaking)",
		"  document: Mini 1 -> Mini 2",
		"  workaround added: mini-new",
		"  workaround removed: mini-old",
		"+ operation PostItem (POST /Items)",
		"- operation DeleteItem (DELETE /Items/{Id}) [breaking]",
		"~ operation GetItems (GET /Items)",
		"    - option query fields: String [breaking]",
		"    ~ option query limit: Integer -> Integer64 [breaking]",
		"    + option query searchterm: String",
		"    ~ expected status codes [200] -> [200 204]\n",
		"~ model Item",
		"    ~ written whole false -> true\n",
		"    + field TagItems: String",
		"    - field Tags: List[String] [breaking]",
		"+ model Extra",
		"~ constant Kind",
		`    + value "Episode"`,
		`    - value "Series" [breaking]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestDiffBodiesAndPaths(t *testing.T) {
	t.Parallel()

	newer := base()
	get := &newer.Groups[0].Operations[0]
	get.Name = "ListItems"
	get.Response = nil
	get.Pageable = nil
	del := &newer.Groups[0].Operations[1]
	del.PathParameters[0].Type = definitions.TypeRef{Type: definitions.Integer}
	del.Request = &definitions.Body{ContentType: "application/json", Type: ref("Item")}
	del.ExpectedStatusCodes = []int{200}
	newer.Groups = append(newer.Groups, definitions.Group{Name: "Moved"})
	newer.Groups[1].Operations = append(newer.Groups[1].Operations, *del)
	newer.Groups[0].Operations = newer.Groups[0].Operations[:1]

	got := Diff(base(), newer)
	for _, want := range []string{
		"~ operation ListItems (GET /Items)",
		"    ~ method name GetItems -> ListItems [breaking]",
		"    - response Reference(Item) (application/json) [breaking]",
		"    ~ pageable true -> false [breaking]",
		"~ operation DeleteItem (DELETE /Items/{Id})",
		"    ~ tag Items -> Moved\n",
		"    ~ path parameter 1: id String -> id Integer [breaking]",
		"    + request Reference(Item) (application/json) [breaking]",
		"    ~ expected status codes [204] -> [200] [breaking]",
	} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, got.String())
		}
	}
}
