package definitions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() *Service {
	str := TypeRef{Type: String}
	return &Service{
		Name: "mini", Package: "mini", Title: "Mini", APIVersion: "1", Source: "mini.json", Auth: "Sonarr", Workarounds: []string{},
		Groups: []Group{
			{Name: CommonGroup, Models: []Model{{Name: "Item", SchemaName: "Item", Fields: []Field{{Name: "Id", JSONName: "Id", Type: str, Description: "a <b> & c"}}}}},
			{Name: "Items", Tag: "ItemsService", Operations: []Operation{{
				Name: "GetItem", Method: "GET", Path: "/Items/{Id}", ExpectedStatusCodes: []int{200},
				PathParameters: []PathParameter{{Name: "Id", Argument: "id", Type: str}},
				Response:       &Body{ContentType: "application/json", Type: TypeRef{Type: Reference, ReferenceName: "Item"}},
			}}},
		},
	}
}

func TestSaveLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	svc := sample()
	// a group file from an earlier import whose tag has since gone
	if err := os.WriteFile(filepath.Join(dir, "Gone.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(svc, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Gone.json")); !os.IsNotExist(err) {
		t.Error("the stale group file survived Save")
	}

	raw, err := os.ReadFile(filepath.Join(dir, CommonGroup+".json")) //nolint:gosec // a temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Description": "a <b> & c"`) || !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("Common.json is escaped or unterminated:\n%s", raw)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, svc) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, svc)
	}
	if err := Validate(got); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLoadRejects(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "run the importer first") {
		t.Errorf("Load of an empty dir = %v", err)
	}
	if err := Save(sample(), dir); err != nil {
		t.Fatal(err)
	}

	misnamed := filepath.Join(dir, "Other.json")
	if err := os.Rename(filepath.Join(dir, "Items.json"), misnamed); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "which belongs in Items.json") {
		t.Errorf("Load of a misnamed group = %v", err)
	}
	if err := os.WriteFile(misnamed, []byte(`{"Name": "Other", "Surprise": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("Load of an unknown field = %v", err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		patch func(*Service)
		want  string
	}{
		{"dangling reference", func(s *Service) { s.Groups[1].Operations[0].Response.Type.ReferenceName = "Nope" }, `reference to undefined type "Nope"`},
		{"list without item", func(s *Service) { s.Groups[0].Models[0].Fields[0].Type = TypeRef{Type: List} }, "List without a nested item"},
		{"unknown type", func(s *Service) { s.Groups[0].Models[0].Fields[0].Type = TypeRef{Type: "Uuid"} }, `unknown type "Uuid"`},
		{"no status codes", func(s *Service) { s.Groups[1].Operations[0].ExpectedStatusCodes = nil }, "GetItem: no expected status codes"},
		{"duplicate operation", func(s *Service) {
			s.Groups[1].Operations = append(s.Groups[1].Operations, s.Groups[1].Operations[0])
		}, "two operations are named GetItem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := sample()
			tt.patch(svc)
			if err := Validate(svc); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestTypeRefString(t *testing.T) {
	t.Parallel()

	nested := TypeRef{Type: Dictionary, NestedItem: &TypeRef{Type: List, NestedItem: &TypeRef{Type: Reference, ReferenceName: "Item"}}}
	if got := nested.String(); got != "Dictionary[List[Reference(Item)]]" {
		t.Errorf("String = %q", got)
	}
	same := TypeRef{Type: Dictionary, NestedItem: &TypeRef{Type: List, NestedItem: &TypeRef{Type: Reference, ReferenceName: "Item"}}}
	if !nested.Equal(same) || nested.Equal(TypeRef{Type: Dictionary}) {
		t.Error("Equal is wrong")
	}
}
