package plan

import (
	"bytes"
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
// A named struct type becomes a definition, one per direction unless both
// come out the same, and schemas reference it; see Defs.
type Schemas struct {
	prefix string // of the references to definitions
	defs   map[defKey]*jsonschema.Def
	order  []defKey // of the definitions, as they were made
	names  map[reflect.Type]string
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

// Name makes name the name of the definition of the struct type t, instead
// of the name of the type.
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
		values, err := s.value(t.Elem(), dir, false, path+"[]")
		if err != nil {
			return nil, err
		}
		return &jsonschema.Schema{Type: jsonschema.Types{"object"}, AdditionalProperties: values}, nil
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
		v, err := NewValidation(t)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if v != nil {
			for _, x := range v.Validate(reflect.Zero(t)) {
				failing = append(failing, x.Pointer)
			}
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
		rules, err := parseRules(path, tag, t)
		if err != nil {
			return nil, err
		}
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
// them in the order of their names. A struct type whose input and output
// schemas come out the same has one definition, named after the type; if
// they differ, the input one gets the suffix Input. Types of one name get
// the name of their package, and then their whole import path, in front.
// Build every schema before Defs: the references get their names from it.
func (s *Schemas) Defs() []*jsonschema.Def {
	merged := s.merge()

	// The definitions to name: the input one of a merged type is the
	// output one.
	type entry struct {
		key   defKey
		base  string
		input bool
	}
	var entries []entry
	for _, key := range s.order {
		_, both := s.defs[defKey{key.t, Output}]
		if key.dir == Input && merged[key.t] {
			continue
		}
		base, ok := s.names[key.t]
		if !ok {
			base = typeName(key.t)
		}
		entries = append(entries, entry{key: key, base: base, input: key.dir == Input && both})
	}

	names := make([]string, len(entries))
	levels := make([]int, len(entries))
	for {
		byName := make(map[string][]int)
		for i, e := range entries {
			names[i] = qualified(e.key.t, e.base, levels[i])
			if e.input {
				names[i] += "Input"
			}
			byName[names[i]] = append(byName[names[i]], i)
		}
		collided := false
		for _, same := range byName {
			if len(same) > 1 {
				for _, i := range same {
					if levels[i] < 2 {
						levels[i]++
						collided = true
					}
				}
			}
		}
		if !collided {
			break
		}
	}
	// Still the same: the same name and path, as with a type named after
	// the Input definition of another. Number them.
	seen := make(map[string]int)
	for i := range names {
		seen[names[i]]++
		if n := seen[names[i]]; n > 1 {
			names[i] += "_" + strconv.Itoa(n)
		}
	}

	defs := make([]*jsonschema.Def, len(entries))
	for i, e := range entries {
		d := s.defs[e.key]
		d.Name, d.Ref = names[i], s.prefix+names[i]
		defs[i] = d
	}
	for t := range merged {
		in, out := s.defs[defKey{t, Input}], s.defs[defKey{t, Output}]
		in.Name, in.Ref = out.Name, out.Ref
	}
	slices.SortFunc(defs, func(a, b *jsonschema.Def) int { return strings.Compare(a.Name, b.Name) })
	return defs
}

// merge returns the struct types whose input and output schemas are the
// same, those of the types they reference being the same too: it starts
// from every type with both and drops those that differ until none does,
// so that types which reference each other stay together if they can.
func (s *Schemas) merge() map[reflect.Type]bool {
	ids := make(map[reflect.Type]int)
	merged := make(map[reflect.Type]bool)
	for _, key := range s.order {
		if _, ok := ids[key.t]; !ok {
			ids[key.t] = len(ids)
		}
		if _, both := s.defs[defKey{key.t, Output}]; both && key.dir == Input {
			merged[key.t] = true
		}
	}
	for changed := true; changed; {
		// References stand for their definitions: the type, and the
		// direction unless it's merged.
		for key, d := range s.defs {
			d.Ref = strconv.Itoa(ids[key.t])
			if key.dir == Input && !merged[key.t] {
				d.Ref += "/input"
			}
		}
		changed = false
		for t := range merged {
			in, _ := json.Marshal(s.defs[defKey{t, Input}].Schema)
			out, _ := json.Marshal(s.defs[defKey{t, Output}].Schema)
			if !bytes.Equal(in, out) {
				delete(merged, t)
				changed = true
			}
		}
	}
	return merged
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

// qualified returns base with, at level 1, the name of the package of t in
// front and, at level 2, its whole import path.
func qualified(t reflect.Type, base string, level int) string {
	path := t.PkgPath()
	switch {
	case level == 0 || path == "":
		return base
	case level == 1:
		return safeName(path[strings.LastIndexByte(path, '/')+1:]) + "." + base
	}
	return safeName(strings.ReplaceAll(path, "/", ".")) + "." + base
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
