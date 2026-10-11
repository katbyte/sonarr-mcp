package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Records are the things Sonarr keeps lists of in its settings: indexers and
// download clients, notifications, import lists, delay and release profiles,
// path mappings, custom formats and the rest. Each kind is listed, added to,
// changed and deleted the one way, by the tools registerRecords makes for
// it, so a client that has changed one kind has changed them all:
//
//   - a record is answered as Sonarr holds it, by Sonarr's own names for its
//     properties, with its credentials left out, its tags by label, and a
//     provider's own settings (its URL, host, category) under settings;
//   - set changes properties by those same names, and a name the kind does
//     not have is refused with the ones it has;
//   - a delete changes nothing without confirm, and says what it would
//     delete.
//
// A record is handled here as its JSON form, a map, and made the SDK's type
// only to be sent: the tools are the same for every kind because nothing in
// them knows a kind's fields.

// recordKind is one kind of record and how the SDK reads and writes it.
type recordKind[T any] struct {
	// prefix names the tools: indexer makes indexer_add, indexer_edit and so
	// on; one and many are what an answer calls a record and several
	prefix, one, many string
	// nameKey is the property a record is named by, "name" for most; with
	// none, a record is named by its id alone
	nameKey string
	// loses is what goes with a record deleted, for the answer that asks
	// for confirm
	loses string

	list   func(ctx context.Context) ([]T, error)
	create func(ctx context.Context, rec T, force bool) (*T, error)
	update func(ctx context.Context, id int, rec T, force bool) (*T, error)
	remove func(ctx context.Context, id int) error

	// a provider is one of several kinds, each with settings of its own, and
	// Sonarr can be asked to try one: kinds are the ones there are, as blank
	// records
	kinds   func(ctx context.Context) ([]T, error)
	test    func(ctx context.Context, rec T) error
	testAll func(ctx context.Context) ([]sonarr.ProviderTestAllResult, error)
	// on are the properties that switch a provider on, those of them a
	// record has set: an indexer's RSS and searches, the events a
	// notification is sent for. Sonarr tries a provider as it is added only
	// when it is switched on, and its test of them all tries only those
	on func(rec map[string]any) []string

	// specs are the conditions a record of this kind is made of (a custom
	// format's, an auto tag's), as blank ones by kind
	specs func(ctx context.Context) ([]map[string]any, error)

	// blank is what a new record starts as, for a kind that is not a
	// provider
	blank func() T
	// defaults are set on a provider's blank before what a call asks for:
	// Sonarr's blank of a kind has everything off, and its own settings page
	// turns on what a person adding one means to have
	defaults func(blank map[string]any)

	// what each tool says of itself; a tool with nothing said is not made
	// (indexers are listed and tested by tools of their own, and a metadata
	// consumer is neither added nor deleted)
	listSays, kindsSays, addSays, editSays, deleteSays, testSays string
}

// The properties of a record that are Sonarr's to set, or that say nothing a
// caller can use: left out of an answer, and refused in set.
var recordNoise = map[string]bool{
	"implementationName": true, "infoLink": true, "configContract": true, "presets": true, "message": true,
}

// recordLocked are the properties set may not change: what a record is, and
// what has a place of its own in the tool's arguments.
var recordLocked = map[string]bool{"id": true, "implementation": true, "fields": true}

// A record held as its JSON form has values of the few kinds JSON has; each
// of these is one of them, or its zero when the value is of another kind.
func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}

	return s
}

func asBool(v any) bool {
	b, ok := v.(bool)

	return ok && b
}

func asNumber(v any) float64 {
	n, ok := v.(float64)
	if !ok {
		return 0
	}

	return n
}

func asList(v any) []any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}

	return list
}

func asObject(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}

	return m
}

// fold is a name as names are compared: without case, and with the
// underscores a caller writing snake_case puts in.
func fold(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", ""))
}

// fromMap makes the SDK's type of a record held as its JSON form.
func fromMap[T any](m map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(m)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// the decoder names the field and the kind of value it takes
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return out, fmt.Errorf("%s takes a %s, not a %s", te.Field, te.Type, te.Value)
		}
		return out, err
	}

	return out, nil
}

// propertyNames are the properties a record of a type has, by their JSON
// names, sorted.
func propertyNames(t reflect.Type) []string {
	var names []string
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	return names
}

// settable are the properties of a record's type that set may change.
func settable[T any]() []string {
	return slices.DeleteFunc(propertyNames(reflect.TypeFor[T]()), func(name string) bool { return recordLocked[name] || recordNoise[name] })
}

// fieldSecret reports whether one of a provider's settings is a credential.
// Sonarr marks each setting's privacy, and only the ordinary ones are shown;
// but it does not mark them all - the API key an import list keeps for
// another Sonarr is ordinary to it - so one named as a credential is
// (secretSetting) is one too. And so is a list of keys and values, which is
// a webhook's own headers: where its token goes, as Authorization, and
// ordinary to Sonarr as well. It is hidden whole rather than shown with its
// values blanked, so that what a caller reads back is never a shape it
// cannot send.
func fieldSecret(field map[string]any) bool {
	privacy := asString(field["privacy"])
	kind := asString(field["type"])

	return (privacy != "" && privacy != string(sonarr.PrivacyLevelNormal)) || kind == "password" || kind == keyValueList || secretSetting(asString(field["name"]))
}

// keyValueList is the kind of setting that is a list of keys and values.
const keyValueList = "keyValueList"

// fieldsOf are a provider's settings as Sonarr sends them: a list, each with
// its name and value.
func fieldsOf(rec map[string]any) []map[string]any {
	list := asList(rec["fields"])
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		if field, ok := f.(map[string]any); ok {
			out = append(out, field)
		}
	}

	return out
}

// ownFields gives a record a copy of its settings to change, and answers
// them: a record cloned shares its settings with the one it was cloned
// from, which is what a change is told apart from.
func ownFields(rec map[string]any) []map[string]any {
	fields := fieldsOf(rec)
	cloned := make([]any, 0, len(fields))
	for i := range fields {
		fields[i] = maps.Clone(fields[i])
		cloned = append(cloned, fields[i])
	}
	if len(cloned) > 0 {
		rec["fields"] = cloned
	}

	return fields
}

// settingsOf are a provider's settings by name, without its credentials and
// without the ones left empty.
func settingsOf(rec map[string]any) map[string]any {
	out := map[string]any{}
	for _, field := range fieldsOf(rec) {
		name := asString(field["name"])
		if name == "" || fieldSecret(field) {
			continue
		}
		switch v := field["value"].(type) {
		case nil:
		case string:
			if v != "" {
				out[name] = v
			}
		case []any:
			if len(v) > 0 {
				out[name] = v
			}
		default:
			out[name] = v
		}
	}

	return out
}

// recordRow is a record as a tool answers it: Sonarr's own properties, less
// its credentials and what says nothing, with its tags by label, its
// settings by name, and each of its conditions the same way.
func recordRow(rec map[string]any, tags map[int]string) map[string]any {
	out := map[string]any{}
	for key, value := range rec {
		switch {
		case secretSetting(key), recordNoise[key]:
		case key == "fields":
			out["settings"] = settingsOf(rec)
		case key == "tags":
			out[key] = tagsByLabel(value, tags)
		case key == "specifications":
			list := asList(value)
			specs := make([]map[string]any, 0, len(list))
			for _, s := range list {
				if spec, ok := s.(map[string]any); ok {
					specs = append(specs, recordRow(spec, tags))
				}
			}
			out[key] = specs
		default:
			out[key] = value
		}
	}

	return out
}

// tagsByLabel are a record's tags by label, where it was sent their ids.
func tagsByLabel(value any, tags map[int]string) []string {
	list := asList(value)
	ids := make([]int, 0, len(list))
	for _, v := range list {
		if n, ok := v.(float64); ok {
			ids = append(ids, int(n))
		}
	}

	return labels(ids, tags)
}

// recordID is a record's id.
func recordID(rec map[string]any) int {
	n := asNumber(rec["id"])

	return int(n)
}

// all reads every record of a kind, as its JSON form.
func (k *recordKind[T]) all(ctx context.Context) ([]map[string]any, error) {
	list, err := k.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		m, err := asMap(list[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}

	return out, nil
}

// label is what a record is called in an answer: its name, or its id where
// the kind has no names.
func (k *recordKind[T]) label(rec map[string]any) string {
	if name := asString(rec[k.nameKey]); name != "" {
		return name
	}

	return k.one + " " + strconv.Itoa(recordID(rec))
}

// find is the record a caller means: by its id, or by its name where the
// kind has names. A name two records share is refused with their ids.
func (k *recordKind[T]) find(ctx context.Context, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("say which of the %s: its id%s", k.many, k.orName())
	}
	all, err := k.all(ctx)
	if err != nil {
		return nil, err
	}

	var found []map[string]any
	id, numeric := strconv.Atoi(ref)
	for _, rec := range all {
		name := asString(rec[k.nameKey])
		if (numeric == nil && recordID(rec) == id) || (name != "" && strings.EqualFold(name, ref)) {
			found = append(found, rec)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		have := make([]string, 0, len(all))
		for _, rec := range all {
			have = append(have, fmt.Sprintf("%s (id %d)", k.label(rec), recordID(rec)))
		}
		if len(have) == 0 {
			return nil, fmt.Errorf("no %s %q: Sonarr has none", k.one, ref)
		}
		return nil, fmt.Errorf("no %s %q (have: %s)", k.one, ref, strings.Join(have, ", "))
	default:
		ids := make([]string, 0, len(found))
		for _, rec := range found {
			ids = append(ids, strconv.Itoa(recordID(rec)))
		}
		return nil, fmt.Errorf("%q names %d of the %s; say which by its id: %s", ref, len(found), k.many, strings.Join(ids, ", "))
	}
}

func (k *recordKind[T]) orName() string {
	if k.nameKey == "" {
		return ""
	}

	return " or its " + k.nameKey
}

// apply makes of a record the one a call asks for: its properties changed
// by set, its own settings by settings, and its conditions rebuilt from the
// kinds there are. It answers the record as the SDK takes it and what
// changed, from and to, with a credential's value left out.
func (k *recordKind[T]) apply(ctx context.Context, r *registry, base, set, settings map[string]any) (typed T, changed []string, err error) {
	var none T
	rec := maps.Clone(base)
	allowed := settable[T]()
	for _, key := range slices.Sorted(maps.Keys(set)) {
		i := slices.IndexFunc(allowed, func(name string) bool { return fold(name) == fold(key) })
		if i < 0 {
			return none, nil, fmt.Errorf("%s has no property %q (it has: %s)", k.one, key, strings.Join(allowed, ", "))
		}
		value, valueErr := k.value(ctx, r, allowed[i], set[key])
		if valueErr != nil {
			return none, nil, valueErr
		}
		rec[allowed[i]] = value
	}

	if len(settings) > 0 {
		fields := ownFields(rec)
		if len(fields) == 0 {
			return none, nil, fmt.Errorf("%s has no settings of its own; change its properties with set", k.one)
		}
		if err := setFieldValues(k.one, fields, settings); err != nil {
			return none, nil, err
		}
	}

	if typed, err = fromMap[T](rec); err != nil {
		return none, nil, err
	}
	after, err := asMap(typed)
	if err != nil {
		return none, nil, err
	}

	return typed, recordChanges(base, after), nil
}

// value is what a property is set to, from what a caller gave: tags by
// label become their ids (and are made when new), a quality profile by name
// its id, and conditions by kind the records Sonarr takes.
func (k *recordKind[T]) value(ctx context.Context, r *registry, name string, given any) (any, error) {
	switch {
	case name == "tags":
		list := asList(given)
		var labelled []string
		ids := make([]any, 0, len(list))
		for _, v := range list {
			switch tag := v.(type) {
			case string:
				labelled = append(labelled, tag)
			case float64:
				ids = append(ids, tag)
			default:
				return nil, fmt.Errorf("a tag is its label or its id, not %v", v)
			}
		}
		made, err := r.makeTags(ctx, labelled)
		if err != nil {
			return nil, err
		}
		for _, id := range made {
			ids = append(ids, float64(id))
		}
		return ids, nil
	case name == "qualityProfileId":
		ref, named := given.(string)
		if !named {
			return given, nil
		}
		profile, err := r.resolveProfile(ctx, ref)
		if err != nil {
			return nil, err
		}
		return float64(profile.Id), nil
	case name == "specifications" && k.specs != nil:
		return k.conditions(ctx, given)
	default:
		return given, nil
	}
}

// conditions builds a record's conditions from what a caller gave: each one
// its kind, a name, whether it is negated or required, and its settings by
// name, set on the blank condition of that kind.
func (k *recordKind[T]) conditions(ctx context.Context, given any) (any, error) {
	list, ok := given.([]any)
	if !ok {
		return nil, errors.New("specifications is a list of conditions, each with its implementation, a name and its settings")
	}
	blanks, err := k.specs(ctx)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, 0, len(blanks))
	for _, b := range blanks {
		name := asString(b["implementation"])
		kinds = append(kinds, name)
	}

	out := make([]any, 0, len(list))
	for _, item := range list {
		want, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("a condition is an object: its implementation, a name and its settings")
		}
		impl := asString(want["implementation"])
		i := slices.IndexFunc(blanks, func(b map[string]any) bool { name := asString(b["implementation"]); return fold(name) == fold(impl) })
		if i < 0 {
			return nil, fmt.Errorf("no kind of condition %q (have: %s)", impl, strings.Join(kinds, ", "))
		}
		spec := maps.Clone(blanks[i])
		fields := ownFields(spec)
		for key, value := range want {
			switch key {
			case "implementation":
			case "settings":
				own := asObject(value)
				if err := setFieldValues("a "+kinds[i], fields, own); err != nil {
					return nil, err
				}
			case "name", "negate", "required":
				spec[key] = value
			default:
				return nil, fmt.Errorf("a condition has no %q: give its implementation, name, negate, required and settings", key)
			}
		}
		if name := asString(spec["name"]); name == "" {
			spec["name"] = kinds[i]
		}
		out = append(out, spec)
	}

	return out, nil
}

// setFieldValues sets a provider's own settings by name. A name it does not
// have is refused with the ones it has, credentials among them, since those
// are set here too.
func setFieldValues(one string, fields []map[string]any, settings map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		i := slices.IndexFunc(fields, func(f map[string]any) bool { name := asString(f["name"]); return fold(name) == fold(key) })
		if i < 0 {
			names := make([]string, 0, len(fields))
			for _, f := range fields {
				name := asString(f["name"])
				names = append(names, name)
			}
			return fmt.Errorf("%s has no setting %q (it has: %s)", one, key, strings.Join(names, ", "))
		}
		fields[i]["value"] = settings[key]
	}

	return nil
}

// recordChanges says what differs between a record and what it was, each
// property from and to; a credential is said to be set, and never shown.
func recordChanges(before, after map[string]any) []string {
	var out []string
	keys := slices.Sorted(maps.Keys(after))
	for _, key := range keys {
		if key == "fields" || recordNoise[key] || reflect.DeepEqual(before[key], after[key]) {
			continue
		}
		if secretSetting(key) {
			out = append(out, key+": set")
			continue
		}
		// a property at its zero is left out of a record's JSON form, so one
		// that was 0 or false is said to have been that, not nothing
		was, had := before[key]
		if !had {
			was = zeroLike(after[key])
		}
		out = append(out, fmt.Sprintf("%s: %v -> %v", key, shown(was), shown(after[key])))
	}

	was := map[string]map[string]any{}
	for _, f := range fieldsOf(before) {
		name := asString(f["name"])
		was[name] = f
	}
	for _, f := range fieldsOf(after) {
		name := asString(f["name"])
		old := was[name]
		if reflect.DeepEqual(old["value"], f["value"]) {
			continue
		}
		if fieldSecret(f) {
			out = append(out, name+": set")
			continue
		}
		out = append(out, fmt.Sprintf("%s: %v -> %v", name, shown(old["value"]), shown(f["value"])))
	}

	return out
}

// zeroLike is the zero of a value's kind, for a number, a flag or text; a
// list or an object has none worth saying, and is nothing.
func zeroLike(v any) any {
	switch v.(type) {
	case float64:
		return float64(0)
	case bool:
		return false
	case string:
		return ""
	default:
		return nil
	}
}

// shown is a value as a change names it: compact, and "nothing" for none.
func shown(v any) string {
	if v == nil {
		return "nothing"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}

	return string(raw)
}

// The arguments and answers the record tools share.
type (
	recordsOut struct {
		pageOut
		Items []map[string]any `json:"items" jsonschema:"each as Sonarr holds it, by its own names for the properties: what set takes"`
	}
	recordOut struct {
		Record  map[string]any `json:"record"            jsonschema:"the record as Sonarr now holds it"`
		Changed []string       `json:"changed,omitempty" jsonschema:"what changed, from and to; a credential is only said to be set"`
	}
	recordAddIn struct {
		Kind     string         `json:"kind,omitempty"     jsonschema:"for a kind of record that comes in several implementations: which one, by the name the kinds tool lists"`
		Name     string         `json:"name,omitempty"     jsonschema:"what to call it"`
		Set      map[string]any `json:"set,omitempty"      jsonschema:"its properties by name, as the list tool shows them; tags by label"`
		Settings map[string]any `json:"settings,omitempty" jsonschema:"for a provider: its own settings by name - a URL, a host, an API key, a category - as the kinds tool lists them"`
		Force    bool           `json:"force,omitempty"    jsonschema:"for a provider: save it even if Sonarr's test of it fails"`
	}
	recordEditIn struct {
		Which    string         `json:"which"              jsonschema:"the one to change: its id, or its name where it has one"`
		Set      map[string]any `json:"set,omitempty"      jsonschema:"the properties to change, by name, as the list tool shows them; tags by label"`
		Settings map[string]any `json:"settings,omitempty" jsonschema:"for a provider: its own settings to change, by name"`
		Force    bool           `json:"force,omitempty"    jsonschema:"for a provider: save it even if Sonarr's test of it fails"`
	}
	recordDeleteIn struct {
		Which   string `json:"which"             jsonschema:"the one to delete: its id, or its name where it has one"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to delete; without it nothing changes and the answer says what a confirmed call would delete"`
	}
	recordDeleteOut struct {
		Deleted     string `json:"deleted,omitempty"      jsonschema:"what was deleted"`
		WouldDelete string `json:"would_delete,omitempty" jsonschema:"without confirm: what a confirmed call deletes; nothing has changed"`
		Loses       string `json:"loses,omitempty"        jsonschema:"what goes with it, and cannot be had back"`
	}
	recordTestIn struct {
		Which string `json:"which,omitempty" jsonschema:"one to test, by its id or name; default every one that is enabled"`
	}
	recordTestOut struct {
		Results []testRow `json:"results"`
	}
	recordKindRow struct {
		Kind     string           `json:"kind"     jsonschema:"what add takes as kind"`
		Settings []map[string]any `json:"settings" jsonschema:"the settings one of this kind takes: each one's name, what it is for, its kind of value, its default and its choices"`
	}
	recordKindsOut struct {
		pageOut
		Kinds []recordKindRow `json:"kinds"`
	}
)

// registerRecords makes the tools for one kind of record: each of list,
// kinds, add, edit, delete and test that the kind has words for.
func registerRecords[T any](r *registry, k recordKind[T]) {
	if k.nameKey == "" && k.blank == nil && k.kinds == nil {
		panic(k.prefix + ": a kind of record with no names needs a blank to start from")
	}

	if k.listSays != "" {
		add(r, readTool, &mcp.Tool{Name: k.prefix + "_list", Description: k.listSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, recordsOut, error) {
			all, err := k.all(ctx)
			if err != nil {
				return nil, recordsOut{}, err
			}
			tags, err := r.tagLabels(ctx)
			if err != nil {
				return nil, recordsOut{}, err
			}
			rows, paged := pageRows(all, in)
			out := recordsOut{pageOut: paged, Items: make([]map[string]any, 0, len(rows))}
			for _, rec := range rows {
				out.Items = append(out.Items, recordRow(rec, tags))
			}

			return nil, out, nil
		})
	}

	if k.kindsSays != "" {
		add(r, readTool, &mcp.Tool{Name: k.prefix + "_kinds", Description: k.kindsSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, recordKindsOut, error) {
			blanks, err := k.kinds(ctx)
			if err != nil {
				return nil, recordKindsOut{}, err
			}
			rows, paged := pageRows(blanks, in)
			out := recordKindsOut{pageOut: paged, Kinds: make([]recordKindRow, 0, len(rows))}
			for i := range rows {
				m, err := asMap(rows[i])
				if err != nil {
					return nil, recordKindsOut{}, err
				}
				impl := asString(m["implementation"])
				out.Kinds = append(out.Kinds, recordKindRow{Kind: impl, Settings: describeFields(fieldsOf(m))})
			}

			return nil, out, nil
		})
	}

	// what a write answers: the record as Sonarr holds it once written
	answer := func(ctx context.Context, written *T, changed []string) (recordOut, error) {
		m, err := asMap(written)
		if err != nil {
			return recordOut{}, err
		}
		tags, err := r.tagLabels(ctx)
		if err != nil {
			return recordOut{}, err
		}

		return recordOut{Record: recordRow(m, tags), Changed: changed}, nil
	}

	if k.addSays != "" {
		add(r, writeTool, &mcp.Tool{Name: k.prefix + "_add", Description: k.addSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordAddIn) (*mcp.CallToolResult, recordOut, error) {
			base, err := k.start(ctx, in.Kind)
			if err != nil {
				return nil, recordOut{}, err
			}
			if k.nameKey != "" {
				name := strings.TrimSpace(in.Name)
				if name == "" {
					name = asString(base["implementation"])
				}
				if name == "" {
					return nil, recordOut{}, fmt.Errorf("give %s a name", k.one)
				}
				base[k.nameKey] = name
			}
			rec, _, err := k.apply(ctx, r, base, in.Set, in.Settings)
			if err != nil {
				return nil, recordOut{}, err
			}
			var made *T
			if in.Force && k.on != nil {
				made, err = k.forced(ctx, rec)
			} else {
				made, err = k.create(ctx, rec, in.Force)
			}
			if err != nil {
				return nil, recordOut{}, k.refused(err, in.Force)
			}
			out, err := answer(ctx, made, nil)

			return nil, out, err
		})
	}

	if k.editSays != "" {
		add(r, writeTool, &mcp.Tool{Name: k.prefix + "_edit", Description: k.editSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordEditIn) (*mcp.CallToolResult, recordOut, error) {
			if len(in.Set) == 0 && len(in.Settings) == 0 {
				return nil, recordOut{}, errors.New("nothing to change: give set, or settings")
			}
			cur, err := k.find(ctx, in.Which)
			if err != nil {
				return nil, recordOut{}, err
			}
			rec, changed, err := k.apply(ctx, r, cur, in.Set, in.Settings)
			if err != nil {
				return nil, recordOut{}, err
			}
			if len(changed) == 0 {
				same, sameErr := answer(ctx, &rec, nil)
				return nil, same, sameErr
			}
			saved, err := k.update(ctx, recordID(cur), rec, in.Force)
			if err != nil {
				return nil, recordOut{}, k.refused(err, in.Force)
			}
			out, err := answer(ctx, saved, changed)

			return nil, out, err
		})
	}

	if k.deleteSays != "" {
		add(r, deleteTool, &mcp.Tool{Name: k.prefix + "_delete", Description: k.deleteSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordDeleteIn) (*mcp.CallToolResult, recordDeleteOut, error) {
			cur, err := k.find(ctx, in.Which)
			if err != nil {
				return nil, recordDeleteOut{}, err
			}
			out := recordDeleteOut{Loses: k.loses}
			if !in.Confirm {
				out.WouldDelete = k.label(cur)

				return nil, out, nil
			}
			if err := k.remove(ctx, recordID(cur)); err != nil {
				return nil, recordDeleteOut{}, err
			}
			out.Deleted = k.label(cur)

			return nil, out, nil
		})
	}

	if k.testSays != "" {
		add(r, readTool, &mcp.Tool{Name: k.prefix + "_test", Description: k.testSays}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordTestIn) (*mcp.CallToolResult, recordTestOut, error) {
			if strings.TrimSpace(in.Which) == "" {
				all, err := k.all(ctx)
				if err != nil {
					return nil, recordTestOut{}, err
				}
				names := map[int]string{}
				for _, rec := range all {
					names[recordID(rec)] = k.label(rec)
				}
				// Sonarr answers 400 when any fails, with every result all
				// the same; the SDK takes that as an answer, not an error
				results, err := k.testAll(ctx)
				if err == nil {
					return nil, recordTestOut{Results: testResults(results, names)}, nil
				}
				// a test that throws rather than fails - a webhook nothing
				// answers - leaves Sonarr with no answer for any of them, so
				// each that is switched on is tried on its own, as its test
				// of them all tries them
				if client.StatusCode(err) < http.StatusInternalServerError || k.on == nil {
					return nil, recordTestOut{}, err
				}
				out := recordTestOut{Results: []testRow{}}
				for _, rec := range all {
					if len(k.on(rec)) == 0 {
						continue
					}
					typed, err := fromMap[T](rec)
					if err != nil {
						return nil, recordTestOut{}, err
					}
					row, err := testOne(k.label(rec), k.test(ctx, typed))
					if err != nil {
						return nil, recordTestOut{}, err
					}
					out.Results = append(out.Results, row)
				}
				slices.SortFunc(out.Results, func(a, b testRow) int { return strings.Compare(a.Name, b.Name) })

				return nil, out, nil
			}
			cur, err := k.find(ctx, in.Which)
			if err != nil {
				return nil, recordTestOut{}, err
			}
			rec, err := fromMap[T](cur)
			if err != nil {
				return nil, recordTestOut{}, err
			}
			row, err := testOne(k.label(cur), k.test(ctx, rec))

			return nil, recordTestOut{Results: []testRow{row}}, err
		})
	}
}

// start is what a new record begins as: the blank of the kind asked for,
// for a provider, and the kind's own blank otherwise.
func (k *recordKind[T]) start(ctx context.Context, kind string) (map[string]any, error) {
	if k.kinds == nil {
		return asMap(k.blank())
	}
	blanks, err := k.kinds(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(blanks))
	for i := range blanks {
		m, err := asMap(blanks[i])
		if err != nil {
			return nil, err
		}
		impl := asString(m["implementation"])
		if kind != "" && fold(impl) == fold(kind) {
			delete(m, "id")
			if k.defaults != nil {
				k.defaults(m)
			}
			return m, nil
		}
		names = append(names, impl)
	}
	slices.Sort(names)
	if strings.TrimSpace(kind) == "" {
		return nil, fmt.Errorf("say which kind of %s to add: %s", strings.TrimPrefix(strings.TrimPrefix(k.one, "an "), "a "), strings.Join(names, ", "))
	}

	return nil, fmt.Errorf("no kind of %s %q (have: %s)", strings.TrimPrefix(strings.TrimPrefix(k.one, "an "), "a "), kind, strings.Join(names, ", "))
}

// forced adds a provider whether or not Sonarr's test of it passes. Sonarr
// tries one that is switched on as it is added whatever force is - there it
// only lets a warning through - never tries one that is switched off, and
// saves an edit with force untried. So it goes in switched off, and an edit
// switches it on.
func (k *recordKind[T]) forced(ctx context.Context, rec T) (*T, error) {
	off, err := asMap(rec)
	if err != nil {
		return nil, err
	}
	on := k.on(off)
	for _, name := range on {
		off[name] = false
	}
	typed, err := fromMap[T](off)
	if err != nil {
		return nil, err
	}
	made, err := k.create(ctx, typed, true)
	if err != nil || len(on) == 0 {
		return made, err
	}

	added, err := asMap(made)
	if err != nil {
		return nil, err
	}
	for _, name := range on {
		added[name] = true
	}
	if typed, err = fromMap[T](added); err != nil {
		return nil, err
	}
	saved, err := k.update(ctx, recordID(added), typed, true)
	if err != nil {
		return nil, fmt.Errorf("%s was added switched off, and switching it on was refused: %w", k.label(added), err)
	}

	return saved, nil
}

// switchedOn are those of some properties a record has set true.
func switchedOn(names ...string) func(rec map[string]any) []string {
	return func(rec map[string]any) []string {
		return slices.DeleteFunc(slices.Clone(names), func(name string) bool { return !asBool(rec[name]) })
	}
}

// refused explains a write Sonarr turned down. Sonarr tries a provider
// before it saves one, and says what failed; force saves it all the same.
func (k *recordKind[T]) refused(err error, forced bool) error {
	if k.test == nil || forced {
		return err
	}

	return fmt.Errorf("%w (Sonarr tries %s before saving it; force saves one whose test fails)", err, k.one)
}

// describeFields are a kind's settings as the kinds tool lists them.
func describeFields(fields []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		if hidden := asString(f["hidden"]); hidden == "hidden" {
			continue
		}
		row := map[string]any{"name": f["name"]}
		for from, to := range map[string]string{"label": "label", "helpText": "help", "type": "type", "advanced": "advanced"} {
			if v, ok := f[from]; ok && v != "" && !reflect.DeepEqual(v, false) {
				row[to] = v
			}
		}
		if v, ok := f["value"]; ok && v != nil && v != "" && !fieldSecret(f) {
			row["default"] = v
		}
		if fieldSecret(f) {
			row["credential"] = true
		}
		if options := asList(f["selectOptions"]); len(options) > 0 {
			choices := make([]map[string]any, 0, len(options))
			for _, o := range options {
				if opt, ok := o.(map[string]any); ok {
					choices = append(choices, map[string]any{"value": opt["value"], "name": opt["name"]})
				}
			}
			row["options"] = choices
		}
		out = append(out, row)
	}

	return out
}
