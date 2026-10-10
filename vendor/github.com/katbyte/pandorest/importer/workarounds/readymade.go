package workarounds

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/pandorest/openapi"
)

// The ready-made workarounds are for the bugs every document seems to have:
// an operation that declares no answer, or the wrong one, or the wrong
// status; a parameter the server reads or a field it sends that the document
// leaves out; a path that is not the API's. A repository says which
// operations and why, and the patching and the check that the bug is still
// there are here, once, in place of a copy in each repository. Each is a
// Workaround like one written by hand, and passes Verify the same way.

// About is what a ready-made workaround is called, the service it is for and
// the bug it fixes: what one written by hand says with its Name, Service and
// Bug methods.
type About struct {
	Name, Service, Bug string
}

type readyMade struct {
	about About
	apply func(spec *openapi.Spec) error
}

func (r readyMade) Name() string                   { return r.about.Name }
func (r readyMade) Service() string                { return r.about.Service }
func (r readyMade) Bug() string                    { return r.about.Bug }
func (r readyMade) Apply(spec *openapi.Spec) error { return r.apply(spec) }

// Answer is what an operation answers, as a ready-made workaround says it:
// one of JSON, JSONString, Model, ListOf, Shape, File and Text.
type Answer struct {
	kind   answerKind
	name   string
	schema *openapi.Schema
}

type answerKind int

const (
	answersJSON answerKind = iota + 1
	answersJSONString
	answersModel
	answersList
	answersShape
	answersFile
	answersText
)

// JSON is JSON of no declared shape, which a method holds raw.
func JSON() Answer { return Answer{kind: answersJSON} }

// JSONString is a JSON string: text in quotes.
func JSONString() Answer { return Answer{kind: answersJSONString} }

// Model is JSON that decodes into one of the document's component schemas.
func Model(schema string) Answer { return Answer{kind: answersModel, name: schema} }

// ListOf is a JSON list of one of the document's component schemas.
func ListOf(schema string) Answer { return Answer{kind: answersList, name: schema} }

// Shape is JSON of a shape the workaround gives, for a document that keeps
// its schemas with its operations and has none to name.
func Shape(schema *openapi.Schema) Answer { return Answer{kind: answersShape, schema: schema} }

// File is an answer of a media type that is not JSON, left for the caller to
// read: an image, a download, a log.
func File(mediaType string) Answer { return Answer{kind: answersFile, name: mediaType} }

// Text is short text that is not JSON, read whole into a string: a path, a
// version, a pong. It is held as it was sent whatever the server calls it,
// since one that sends bare text may label it JSON. A long text is a File.
func Text(mediaType string) Answer { return Answer{kind: answersText, name: mediaType} }

func (a Answer) String() string {
	switch a.kind {
	case answersJSON:
		return "JSON of no declared shape"
	case answersJSONString:
		return "a JSON string"
	case answersModel:
		return "one " + a.name
	case answersList:
		return "a list of " + a.name
	case answersShape:
		return "JSON of a shape of its own"
	case answersFile:
		return "a file (" + a.name + ")"
	case answersText:
		return "text (" + a.name + ")"
	default:
		return "no answer"
	}
}

// content is the answer as a response's content.
func (a Answer) content(spec *openapi.Spec) (map[string]*openapi.MediaType, error) {
	ref := func() (*openapi.Schema, error) {
		if spec.Components.Schemas[a.name] == nil {
			return nil, fmt.Errorf("schema %s is not in the document", a.name)
		}

		return &openapi.Schema{Ref: openapi.SchemaRefPrefix + a.name}, nil
	}
	asJSON := func(schema *openapi.Schema) map[string]*openapi.MediaType {
		return map[string]*openapi.MediaType{jsonMedia: {Schema: schema}}
	}

	switch a.kind {
	case answersJSON:
		return asJSON(nil), nil
	case answersJSONString:
		return asJSON(&openapi.Schema{Type: openapi.TypeString}), nil
	case answersModel:
		schema, err := ref()
		if err != nil {
			return nil, err
		}

		return asJSON(schema), nil
	case answersList:
		schema, err := ref()
		if err != nil {
			return nil, err
		}

		return asJSON(&openapi.Schema{Type: openapi.TypeArray, Items: schema}), nil
	case answersShape:
		if a.schema == nil {
			return nil, errors.New("a shape with no schema")
		}

		return asJSON(a.schema), nil
	case answersFile, answersText:
		if !strings.Contains(a.name, "/") || openapi.JSONMedia(a.name) {
			return nil, fmt.Errorf("%q is not a media type other than JSON's", a.name)
		}

		return map[string]*openapi.MediaType{a.name: {Schema: &openapi.Schema{Type: openapi.TypeString, Format: binaryFormat}, Text: a.kind == answersText}}, nil
	default:
		return nil, errors.New("no answer is given")
	}
}

// declaredBy reports whether a response's content declares this answer.
func (a Answer) declaredBy(content map[string]*openapi.MediaType) bool {
	mediaType, hasJSON := openapi.PickJSON(content)
	if a.kind == answersFile || a.kind == answersText {
		media := content[a.name]

		return media != nil && !hasJSON && media.Text == (a.kind == answersText)
	}
	if !hasJSON || content[mediaType] == nil {
		return false
	}

	schema := content[mediaType].Schema
	switch a.kind {
	case answersJSON:
		return schema == nil || reflect.DeepEqual(schema, &openapi.Schema{})
	case answersJSONString:
		return schema != nil && schema.Type == openapi.TypeString && schema.Format != binaryFormat && schema.RefName() == ""
	case answersModel:
		return schema.RefName() == a.name
	case answersList:
		return schema != nil && schema.Type == openapi.TypeArray && schema.Items.RefName() == a.name
	case answersShape:
		return reflect.DeepEqual(schema, a.schema)
	default:
		return false
	}
}

const (
	jsonMedia    = "application/json"
	binaryFormat = "binary"
)

// targeted finds the operation a target names, a method and a path
// ("GET /Items").
func targeted(spec *openapi.Spec, target string) (*openapi.Operation, error) {
	method, path, ok := strings.Cut(target, " ")
	if !ok {
		return nil, fmt.Errorf("%q is not a method and a path", target)
	}

	return Operation(spec, method, path)
}

// success is the response an operation documents for success: its lowest
// 2xx.
func success(op *openapi.Operation) *openapi.Response {
	for _, code := range openapi.SortedKeys(op.Responses) {
		if len(code) == 3 && code[0] == '2' {
			return op.Responses[code]
		}
	}

	return nil
}

// UndeclaredAnswers is for operations that declare a success with no
// content, so that nothing says whether they answer JSON, and in what shape,
// or a file: answers says what each does, by method and path. It fails for
// any that declares its answer now.
func UndeclaredAnswers(about About, answers map[string]Answer) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		if len(answers) == 0 {
			return errors.New("it names no operations")
		}

		var declared []string
		for _, target := range openapi.SortedKeys(answers) {
			op, err := targeted(spec, target)
			if err != nil {
				return err
			}
			resp := success(op)
			if resp == nil {
				return fmt.Errorf("%s has no success response", target)
			}
			if len(resp.Content) > 0 {
				declared = append(declared, target)

				continue
			}

			content, err := answers[target].content(spec)
			if err != nil {
				return fmt.Errorf("%s: %w", target, err)
			}
			resp.Content = content
		}
		if len(declared) > 0 {
			return fmt.Errorf("these now declare their answer, so take them out: %s", strings.Join(declared, ", "))
		}

		return nil
	}}
}

// Correction is an answer a document gets wrong: what it declares, and what
// the server answers.
type Correction struct {
	Declared Answer
	Answers  Answer
}

// ListDeclaredAsOne is the commonest correction: the document declares one
// of a schema, and the server answers a list of them (an update of many that
// answers every record it changed).
func ListDeclaredAsOne(schema string) Correction {
	return Correction{Declared: Model(schema), Answers: ListOf(schema)}
}

// WrongAnswers is for operations that declare one answer where the server
// gives another: a JSON string that is sent as bare text, one record where
// the server sends a list of them. corrections says, by method and path,
// what each declares and what it answers. It fails for any that no longer
// declares what its correction says, which is how a document that was fixed,
// or changed again, is noticed.
func WrongAnswers(about About, corrections map[string]Correction) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		if len(corrections) == 0 {
			return errors.New("it names no operations")
		}

		for _, target := range openapi.SortedKeys(corrections) {
			op, err := targeted(spec, target)
			if err != nil {
				return err
			}
			resp := success(op)
			if resp == nil {
				return fmt.Errorf("%s has no success response", target)
			}

			c := corrections[target]
			if !c.Declared.declaredBy(resp.Content) {
				return fmt.Errorf("%s no longer declares %s", target, c.Declared)
			}
			content, err := c.Answers.content(spec)
			if err != nil {
				return fmt.Errorf("%s: %w", target, err)
			}
			if c.Declared.declaredBy(content) {
				return fmt.Errorf("%s: the correction changes nothing: both are %s", target, c.Declared)
			}
			resp.Content = content
		}

		return nil
	}}
}

// WrongStatus is for operations documented as answering one status that
// answer another with the same body: a create documented as 200 that answers
// 201 Created. answers lists, under each status the server really gives, the
// operations that give it, by method and path. It fails for any that no
// longer documents the declared status, or documents the other already.
func WrongStatus(about About, declared int, answers map[int][]string) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		from, moved := strconv.Itoa(declared), 0
		for _, status := range slices.Sorted(maps.Keys(answers)) {
			to := strconv.Itoa(status)
			for _, target := range answers[status] {
				op, err := targeted(spec, target)
				if err != nil {
					return err
				}
				resp := op.Responses[from]
				if resp == nil || op.Responses[to] != nil {
					return fmt.Errorf("%s no longer documents a %s and no %s", target, from, to)
				}

				delete(op.Responses, from)
				op.Responses[to] = resp
				moved++
			}
		}
		if moved == 0 {
			return errors.New("it names no operations")
		}

		return nil
	}}
}

// UndeclaredParameters is for operations that read a parameter their
// document does not declare: targets are the operations, by method and path,
// and params what each of them is given. It fails when any already declares
// one of them. A header named Accept gives the caller the say over what one
// call asks for (see Param).
func UndeclaredParameters(about About, targets []string, params ...Param) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		if len(targets) == 0 || len(params) == 0 {
			return errors.New("it names no operations or no parameters")
		}

		return AddParameters(spec, targets, params...)
	}}
}

// UndeclaredProperties is for the fields a server sends, and reads back,
// that its document does not declare, so that a model drops them without a
// word: properties says, by component schema, what to add. It fails for any
// the schema declares now.
func UndeclaredProperties(about About, properties map[string]map[string]*openapi.Schema) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		added := 0
		for _, name := range openapi.SortedKeys(properties) {
			schema := spec.Components.Schemas[name]
			if schema == nil {
				return fmt.Errorf("no schema %s", name)
			}
			for _, property := range openapi.SortedKeys(properties[name]) {
				if schema.Properties[property] != nil {
					return fmt.Errorf("%s declares %s now", name, property)
				}
				if schema.Properties == nil {
					schema.Properties = map[string]*openapi.Schema{}
				}

				schema.Properties[property] = properties[name][property]
				added++
			}
		}
		if added == 0 {
			return errors.New("it names no properties")
		}

		return nil
	}}
}

// NotAPI is for the paths a document lists that are not the API's: the web
// interface's page, its static files, its login form, which answer HTML to a
// browser and nothing a client reads. They are taken out of the document. It
// fails for any that is not there.
func NotAPI(about About, paths ...string) Workaround {
	return readyMade{about, func(spec *openapi.Spec) error {
		if len(paths) == 0 {
			return errors.New("it names no paths")
		}

		for _, path := range paths {
			if spec.Paths[path] == nil {
				return fmt.Errorf("%s is not in the document", path)
			}
			delete(spec.Paths, path)
		}

		return nil
	}}
}
