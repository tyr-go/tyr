package plan

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/tyr-go/tyr/internal/jsonschema"
)

// Direction is the way that the values a schema describes go.
type Direction int

const (
	// Input is what the server reads, such as a request. A member is
	// required if its field fails validation without it, a pointer may be
	// null if nil passes validation, and the validate tags add keywords.
	Input Direction = iota
	// Output is what the server writes, such as a result. A member is
	// required if json/v2 always writes it, and a pointer may be null
	// unless omitzero or omitempty leaves nil out. The validate tags add
	// nothing: the server doesn't check what it writes.
	Output
)

// String returns "input" or "output".
func (d Direction) String() string {
	if d == Input {
		return "input"
	}
	return "output"
}

// Schemas builds the JSON Schemas of the values of Go types, as
// encoding/json/v2 writes and reads them: the members of a struct are
// those of members, the plan that binding and validation follow too.
// A named struct type becomes a definition per direction, and an enum type
// one definition for both, as its values are the same both ways; schemas
// reference them. See Defs.
type Schemas struct {
	prefix  string // of the references to definitions
	defs    map[defKey]*jsonschema.Def
	order   []defKey // of the definitions, as they were made
	names   map[reflect.Type]string
	failing func(t reflect.Type) ([][]string, error) // see SetFailing
}

// defKey identifies a definition.
type defKey struct {
	t   reflect.Type
	dir Direction
}

// NewSchemas returns an empty set of schemas, which reference definitions
// with prefix and their names, such as "#/components/schemas/".
func NewSchemas(prefix string) *Schemas {
	return &Schemas{prefix: prefix, defs: make(map[defKey]*jsonschema.Def), names: make(map[reflect.Type]string)}
}

// SetFailing makes the schemas of requests take their required members from
// failing rather than from the validation of the core: failing returns the
// paths, as Pointer takes them, of the values in a zero value of the struct
// type t that fail its validate tags, as the validator of an API reports
// them. The keywords of the tags come from the rules the core knows either
// way.
func (s *Schemas) SetFailing(failing func(t reflect.Type) ([][]string, error)) {
	s.failing = failing
}

// Name makes name the base name of the definitions of the struct type t,
// instead of that of its method SchemaName or of the type, for a type of
// the transports, such as the problem of rest.
func (s *Schemas) Name(t reflect.Type, name string) {
	s.names[t] = name
}

// Of returns the schema of the values of type t that go the way dir says.
// It fails on a type that json/v2 can't write or read, such as a func or a
// time.Duration.
func (s *Schemas) Of(t reflect.Type, dir Direction) (*jsonschema.Schema, error) {
	return s.value(t, dir, false, t.String())
}

// Member is a member of the JSON object of a struct type, with its schema.
type Member struct {
	Name        string
	Index       []int              // of the field, through embedded structs
	Schema      *jsonschema.Schema // of its values, without the description
	Description string             // the doc tag of the field
	Required    bool
}

// Members returns the members of the JSON object of the struct type t,
// with their schemas, in the direction dir; see Direction for which are
// required.
func (s *Schemas) Members(t reflect.Type, dir Direction) ([]Member, error) {
	ms, _, err := s.members(t, dir, t.String())
	return ms, err
}

// Object returns the schema of an object of members, with the description
// of every member in its property.
func Object(members []Member) *jsonschema.Schema {
	obj := &jsonschema.Schema{Type: jsonschema.Types{"object"}}
	for _, m := range members {
		obj.Properties = append(obj.Properties, jsonschema.Property{Name: m.Name, Schema: jsonschema.Describe(m.Schema, m.Description)})
		if m.Required {
			obj.Required = append(obj.Required, m.Name)
		}
	}
	return obj
}

var (
	stringType  = jsonschema.Types{"string"}
	integerType = jsonschema.Types{"integer"}
)

// value returns the schema of the values of type t; quoted is the string
// option of their field, and path names t in errors.
func (s *Schemas) value(t reflect.Type, dir Direction, quoted bool, path string) (*jsonschema.Schema, error) {
	switch t.Kind() {
	case reflect.Pointer:
		elem, err := s.value(t.Elem(), dir, quoted, path)
		if err != nil {
			return nil, err
		}
		return jsonschema.Nullable(elem), nil
	case reflect.Interface:
		return &jsonschema.Schema{}, nil // any value, null too
	}
	if e, err := enumOf(t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	} else if e != nil {
		return s.enumRef(e), nil
	}
	switch t {
	case reflect.TypeFor[time.Time]():
		return &jsonschema.Schema{Type: stringType, Format: "date-time"}, nil
	case reflect.TypeFor[uuid.UUID]():
		return &jsonschema.Schema{Type: stringType, Format: "uuid"}, nil
	case reflect.TypeFor[jsontext.Value]():
		return &jsonschema.Schema{}, nil
	case reflect.TypeFor[time.Duration]():
		return nil, fmt.Errorf("%s: json/v2 has no representation of time.Duration", path)
	}
	switch methodsOf(t, dir) {
	case jsonMethods:
		return &jsonschema.Schema{}, nil // whatever its methods write
	case textMethods:
		return &jsonschema.Schema{Type: stringType}, nil
	}

	k := t.Kind()
	if quoted && !isNumberKind(k) {
		return nil, fmt.Errorf("%s: the string option of json/v2 is for numbers, not %v", path, t)
	}
	switch {
	case k == reflect.Bool:
		return &jsonschema.Schema{Type: jsonschema.Types{"boolean"}}, nil
	case k == reflect.String:
		return &jsonschema.Schema{Type: stringType}, nil
	case isInt(k):
		if quoted {
			return &jsonschema.Schema{Type: stringType, Pattern: `^-?(0|[1-9][0-9]*)$`}, nil
		}
		sch := &jsonschema.Schema{Type: integerType}
		if bits := t.Bits(); bits < 64 {
			sch.Minimum = jsontext.Value(strconv.FormatInt(-1<<(bits-1), 10))
			sch.Maximum = jsontext.Value(strconv.FormatInt(1<<(bits-1)-1, 10))
		}
		return sch, nil
	case isUint(k):
		if quoted {
			return &jsonschema.Schema{Type: stringType, Pattern: `^(0|[1-9][0-9]*)$`}, nil
		}
		sch := &jsonschema.Schema{Type: integerType, Minimum: jsontext.Value("0")}
		if bits := t.Bits(); bits < 64 {
			sch.Maximum = jsontext.Value(strconv.FormatUint(1<<bits-1, 10))
		}
		return sch, nil
	case k == reflect.Float32 || k == reflect.Float64:
		if quoted {
			return &jsonschema.Schema{Type: stringType, Pattern: `^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`}, nil
		}
		return &jsonschema.Schema{Type: jsonschema.Types{"number"}}, nil
	case (k == reflect.Slice || k == reflect.Array) && t.Elem() == reflect.TypeFor[byte]():
		sch := &jsonschema.Schema{Type: stringType, ContentEncoding: "base64"}
		if k == reflect.Array { // padded base64 of exactly t.Len() bytes
			sch.MinLength, sch.MaxLength = new((t.Len()+2)/3*4), new((t.Len()+2)/3*4)
		}
		return sch, nil
	case k == reflect.Slice || k == reflect.Array:
		items, err := s.value(t.Elem(), dir, false, path+"[]")
		if err != nil {
			return nil, err
		}
		sch := &jsonschema.Schema{Type: jsonschema.Types{"array"}, Items: items}
		if k == reflect.Array {
			sch.MinItems, sch.MaxItems = new(t.Len()), new(t.Len())
		}
		return sch, nil
	case k == reflect.Map:
		if kk := t.Key().Kind(); kk != reflect.String && !isNumberKind(kk) && methodsOf(t.Key(), dir) == noMethods {
			return nil, fmt.Errorf("%s: json/v2 can't write the keys of %v as the names of members", path, t)
		}
		keys, err := enumOf(t.Key())
		switch {
		case err != nil:
			return nil, fmt.Errorf("%s: %w", path, err)
		case keys != nil && keys.schema[0] != "string":
			return nil, fmt.Errorf("%s: the keys of %v are of %v, an enum of numbers, which can't name the members of a JSON object", path, t, t.Key())
		}
		values, err := s.value(t.Elem(), dir, false, path+"[]")
		if err != nil {
			return nil, err
		}
		sch := &jsonschema.Schema{Type: jsonschema.Types{"object"}, AdditionalProperties: values}
		if keys != nil {
			sch.PropertyNames = s.enumRef(keys)
		}
		return sch, nil
	case k == reflect.Struct && t.Name() == "":
		return s.object(t, dir, path)
	case k == reflect.Struct:
		return s.ref(t, dir, path)
	}
	return nil, fmt.Errorf("%s: json/v2 has no representation of %v", path, t)
}

// isNumberKind reports whether values of kind k are JSON numbers.
func isNumberKind(k reflect.Kind) bool {
	return isInt(k) || isUint(k) || k == reflect.Float32 || k == reflect.Float64
}

// methods are the methods by which json/v2 writes or reads a type.
type methods int

const (
	noMethods   methods = iota
	jsonMethods         // of JSON: any JSON value
	textMethods         // of text: a JSON string
)

// methodsOf returns the methods of t, or of *T, by which json/v2 writes
// values of t for Output, or reads them for Input.
func methodsOf(t reflect.Type, dir Direction) methods {
	has := func(m reflect.Type) bool {
		return t.Implements(m) || reflect.PointerTo(t).Implements(m)
	}
	if dir == Output {
		switch {
		case has(reflect.TypeFor[json.MarshalerTo]()) || has(reflect.TypeFor[json.Marshaler]()):
			return jsonMethods
		case has(reflect.TypeFor[encoding.TextAppender]()) || has(reflect.TypeFor[encoding.TextMarshaler]()):
			return textMethods
		}
		return noMethods
	}
	switch {
	case has(reflect.TypeFor[json.UnmarshalerFrom]()) || has(reflect.TypeFor[json.Unmarshaler]()):
		return jsonMethods
	case has(reflect.TypeFor[encoding.TextUnmarshaler]()):
		return textMethods
	}
	return noMethods
}

// enumRef returns a reference to the definition of the enum e, and makes
// the definition the first time: one for both directions, under the key of
// Output, which Defs names without a suffix, as the values are the same
// both ways.
func (s *Schemas) enumRef(e *enum) *jsonschema.Schema {
	key := defKey{e.typ, Output}
	d, ok := s.defs[key]
	if !ok {
		d = &jsonschema.Def{Schema: &jsonschema.Schema{Type: e.schema, Enum: e.json}}
		s.defs[key] = d
		s.order = append(s.order, key)
	}
	return &jsonschema.Schema{Ref: d}
}

// ref returns a reference to the definition of the named struct type t in
// the direction dir, and makes the definition the first time.
func (s *Schemas) ref(t reflect.Type, dir Direction, path string) (*jsonschema.Schema, error) {
	key := defKey{t, dir}
	d, ok := s.defs[key]
	if !ok {
		d = &jsonschema.Def{}
		s.defs[key] = d // a recursive type finds it while it's made
		s.order = append(s.order, key)
		obj, err := s.object(t, dir, path)
		if err != nil {
			return nil, err
		}
		d.Schema = obj
	}
	return &jsonschema.Schema{Ref: d}, nil
}

// object returns the schema of the JSON object of the struct type t.
func (s *Schemas) object(t reflect.Type, dir Direction, path string) (*jsonschema.Schema, error) {
	ms, fallback, err := s.members(t, dir, path)
	if err != nil {
		return nil, err
	}
	obj := Object(ms)
	if fallback != nil && fallback.Kind() == reflect.Map {
		values, err := s.value(fallback.Elem(), dir, false, path+"[]")
		if err != nil {
			return nil, err
		}
		if !reflect.ValueOf(*values).IsZero() { // {} is the default
			obj.AdditionalProperties = values
		}
	}
	return obj, nil
}

// members returns the members of the JSON object of the struct type t with
// their schemas, and the fallback of t for unknown members, if any.
func (s *Schemas) members(t reflect.Type, dir Direction, path string) ([]Member, reflect.Type, error) {
	layout, fallback, err := objectOf(t)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	// The pointers that a zero value of t fails at: a member absent from
	// the input leaves its field zero.
	var failing []string
	if dir == Input {
		if failing, err = s.failingOf(t); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	out := make([]Member, 0, len(layout))
	for _, m := range layout {
		var required bool
		if dir == Input {
			p := pointer("", m.name)
			required = !m.indirect && slices.ContainsFunc(failing, func(f string) bool {
				return f == p || strings.HasPrefix(f, p+"/")
			})
		} else {
			required = !m.omit && !m.indirect
		}
		sch, err := s.member(m, dir, required, path+"."+m.field.Name)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, Member{
			Name:        m.name,
			Index:       m.index,
			Schema:      sch,
			Description: m.field.Tag.Get("doc"),
			Required:    required,
		})
	}
	return out, fallback, nil
}

// failingOf returns the JSON Pointers of the values in a zero value of the
// struct type t that fail validation: its validate tags, as the validator
// of SetFailing or the core checks them.
func (s *Schemas) failingOf(t reflect.Type) ([]string, error) {
	var out []string
	if s.failing != nil {
		paths, err := s.failing(t)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			p, _, err := Pointer(t, path)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	v, err := NewValidation(t)
	if err != nil || v == nil {
		return nil, err
	}
	for _, x := range v.Validate(reflect.Zero(t)) {
		out = append(out, x.Pointer)
	}
	return out, nil
}

// member returns the schema of the values of the member m, with the
// keywords of its validate tag for Input.
func (s *Schemas) member(m member, dir Direction, required bool, path string) (*jsonschema.Schema, error) {
	t := m.field.Type
	f := form{quoted: m.quoted}
	nullable := false
	if t.Kind() == reflect.Pointer {
		t, f.pointer = t.Elem(), true
		nullable = dir == Output && !m.omit || dir == Input && !required
	}
	sch, err := s.value(t, dir, m.quoted, path)
	if err != nil {
		return nil, err
	}
	if tag := m.field.Tag.Get("validate"); dir == Input && tag != "" && tag != "-" {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		rules := keywordRules(tag, t)
		// The rules check the Go value. A type that JSON carries by
		// methods of its own may write it in any way, such as an int as
		// its name, so the keywords of the rules would demand of its JSON
		// what they demand of the value: they add nothing. A time.Time is
		// the exception, whose JSON the keywords know.
		if methodsOf(t, dir) == noMethods || t == reflect.TypeFor[time.Time]() {
			sch = withRules(sch, rules, f)
		}
	}
	if nullable {
		sch = jsonschema.Nullable(sch)
	}
	return sch, nil
}

// withRules returns sch with the keywords of rules. Beside a reference,
// they go into an allOf, which Draft 7 doesn't ignore.
func withRules(sch *jsonschema.Schema, rules []rule, f form) *jsonschema.Schema {
	if sch.Ref != nil {
		var extra jsonschema.Schema
		for _, r := range rules {
			r.keywords(&extra, f)
		}
		if reflect.ValueOf(extra).IsZero() {
			return sch
		}
		extra.AllOf = []*jsonschema.Schema{sch}
		return &extra
	}
	c := *sch
	for _, r := range rules {
		r.keywords(&c, f)
	}
	return &c
}

// Defs completes the definitions of the schemas built so far and returns
// them in the order of their names. A definition is named after its type
// and its direction only, so that adding a schema renames none and a
// change of a validate tag renames nothing: the base name, which Name or
// the method SchemaName of the type gives, or else the name of the type,
// and for Input the suffix Input, which the one definition of an enum type
// goes without. Defs fails on a base name that isn't
// letters, digits, '.', '-' and '_', and on two definitions of one name.
// Build every schema before Defs: the references get their names from it.
func (s *Schemas) Defs() ([]*jsonschema.Def, error) {
	byName := make(map[string]defKey, len(s.order))
	defs := make([]*jsonschema.Def, 0, len(s.order))
	for _, key := range s.order {
		base, err := s.baseName(key.t)
		if err != nil {
			return nil, err
		}
		name := base
		if key.dir == Input {
			name += "Input"
		}
		if other, ok := byName[name]; ok {
			return nil, fmt.Errorf("two types have the schema name %q: %s; give one of them a method SchemaName() string",
				name, describeKeys(other, key))
		}
		byName[name] = key
		d := s.defs[key]
		d.Name, d.Ref = name, s.prefix+name
		defs = append(defs, d)
	}
	slices.SortFunc(defs, func(a, b *jsonschema.Def) int { return strings.Compare(a.Name, b.Name) })
	return defs, nil
}

// schemaNamer is the method by which a type names its schemas, as
// tyr.SchemaNamer documents it.
type schemaNamer interface {
	SchemaName() string
}

// baseName returns the base name of the definitions of the named struct
// type t, as described at Defs.
func (s *Schemas) baseName(t reflect.Type) (string, error) {
	if name, ok := s.names[t]; ok {
		return name, nil
	}
	if !reflect.PointerTo(t).Implements(reflect.TypeFor[schemaNamer]()) {
		return typeName(t), nil
	}
	name := reflect.New(t).Interface().(schemaNamer).SchemaName()
	if name == "" || safeName(name) != name {
		return "", fmt.Errorf("%s.SchemaName() = %q, want letters, digits, '.', '-' and '_'", qualifiedName(t), name)
	}
	return name, nil
}

// describeKeys describes the definitions a and b, of one name, for an
// error: by their types with their import paths, and by their directions
// if those differ.
func describeKeys(a, b defKey) string {
	if a.dir == b.dir {
		return qualifiedName(a.t) + " and " + qualifiedName(b.t)
	}
	of := func(k defKey) string {
		if k.dir == Input {
			return "the requests of " + qualifiedName(k.t)
		}
		return "the results of " + qualifiedName(k.t)
	}
	return of(a) + " and " + of(b)
}

// qualifiedName returns the name of the named type t with its import path,
// such as github.com/acme/links.Link.
func qualifiedName(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}

// typeName returns the name of the named type t for its definition: that of
// the type, with those of its type arguments, if it has any, after an
// underscore each: Page[x.Link] is Page_Link.
func typeName(t reflect.Type) string {
	name := t.Name()
	i := strings.IndexByte(name, '[')
	if i < 0 {
		return name
	}
	parts := []string{name[:i]}
	for tok := range strings.FieldsFuncSeq(name[i:], func(r rune) bool { return strings.ContainsRune("[]*, ", r) }) {
		if j := strings.LastIndexByte(tok, '.'); j >= 0 {
			tok = tok[j+1:]
		}
		parts = append(parts, tok)
	}
	return safeName(strings.Join(parts, "_"))
}

// safeName returns name with the characters that the names of definitions
// can't have, per OpenAPI, as underscores.
func safeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}
