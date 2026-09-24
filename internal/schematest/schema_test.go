package schematest

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/tyr-go/tyr/internal/plan"
	"github.com/tyr-go/tyr/internal/plan/plantest"
)

// dialect is a draft of JSON Schema and where its definitions live.
type dialect struct {
	name   string
	draft  *jsonschema.Draft
	schema string // its $schema
	defs   string // the member that holds the definitions
}

// dialects are those of OpenRPC and of OpenAPI 3.1, which the schemas of
// tyr must read alike.
var dialects = []dialect{
	{"Draft 7", jsonschema.Draft7, "http://json-schema.org/draft-07/schema#", "definitions"},
	{"2020-12", jsonschema.Draft2020, "https://json-schema.org/draft/2020-12/schema", "$defs"},
}

// compile returns the schema of the values of typ that go the way dir says,
// as d reads it, with the formats asserted.
func compile(t *testing.T, typ reflect.Type, dir plan.Direction, d dialect) *jsonschema.Schema {
	t.Helper()
	s := plan.NewSchemas("#/" + d.defs + "/")
	sch, err := s.Of(typ, dir)
	if err != nil {
		t.Fatalf("Of(%v, %v) error = %v", typ, dir, err)
	}
	defs := make(map[string]any)
	for _, def := range s.Defs() { // names the references first
		defs[def.Name] = decode(t, marshal(t, def.Schema))
	}
	root := decode(t, marshal(t, sch))
	// allOf, not the root itself: Draft 7 would ignore $schema beside $ref.
	doc := map[string]any{"$schema": d.schema, "allOf": []any{root}, d.defs: defs}

	c := jsonschema.NewCompiler()
	c.DefaultDraft(d.draft)
	c.AssertFormat()
	const url = "https://tyr.test/schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile(url)
	if err != nil {
		t.Fatalf("%s: Compile(%s) error = %v", d.name, marshal(t, doc), err)
	}
	return compiled
}

// marshal returns the JSON of v, or fails t.
func marshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decode returns the JSON value data for the validator, or fails t.
func decode(t *testing.T, data []byte) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// failures returns the JSON Pointers of the values in data that fail sch,
// sorted: where the core points its violations, so that a missing member
// is at its own pointer, not at its object's.
func failures(t *testing.T, sch *jsonschema.Schema, data []byte) []string {
	t.Helper()
	err := sch.Validate(decode(t, data))
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate() error = %v", err)
	}
	found := make(map[string]bool)
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			at := pointer(e.InstanceLocation)
			if r, ok := e.ErrorKind.(*kind.Required); ok {
				for _, name := range r.Missing {
					found[at+pointer([]string{name})] = true
				}
			} else {
				found[at] = true
			}
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	var out []string
	for p := range found {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// pointer returns the JSON Pointer of tokens.
func pointer(tokens []string) string {
	var p jsontext.Pointer
	for _, tok := range tokens {
		p = p.AppendToken(tok)
	}
	return string(p)
}

// Types for the output: every form of value that json/v2 writes.
type (
	selfJSON struct{}

	page[T any] struct {
		Items []T `json:"items"`
	}

	tree struct {
		Name string `json:"name"`
		Kids []tree `json:"kids,omitempty"`
	}

	everything struct {
		Bool    bool           `json:"bool"`
		Int8    int8           `json:"int8"`
		Uint    uint           `json:"uint"`
		Float   float32        `json:"float"`
		Quoted  int64          `json:"quoted,string"`
		QPtr    *uint16        `json:"qptr,string"`
		Bytes   []byte         `json:"bytes"`
		Arr     [3]byte        `json:"arr"`
		Ints    [2]int         `json:"ints"`
		Map     map[string]int `json:"map"`
		IntMap  map[int]string `json:"int_map"`
		Ptr     *string        `json:"ptr"`
		OmitPtr *string        `json:"omit_ptr,omitempty"`
		Empty   string         `json:"empty,omitempty"`
		Zero    time.Time      `json:"zero,omitzero"`
		Any     any            `json:"any"`
		At      time.Time      `json:"at"`
		ID      uuid.UUID      `json:"id"`
		Addr    netip.Addr     `json:"addr"`
		Raw     jsontext.Value `json:"raw"`
		Self    selfJSON       `json:"self"`
		Nested  plantest.Inner `json:"nested"`
		NestPtr *plantest.Inner
		Anon    struct {
			A int `json:"a"`
		} `json:"anon"`
		Page page[plantest.Inner] `json:"page"`
		Tree tree                 `json:"tree"`
		*plantest.Base
		Rest map[string]any `json:",embed"` //nolint:staticcheck // SA5008 doesn't know the embed option of json/v2
	}
)

func (s selfJSON) MarshalJSON() ([]byte, error) { return []byte(`[1,"two"]`), nil }
func (s *selfJSON) UnmarshalJSON([]byte) error  { return nil }

// fill sets every field of v that it can set, through pointers, slices,
// maps and structs, down to depth levels, to a value that no other field
// has.
func fill(v reflect.Value, n *int, depth int) {
	if depth == 0 {
		return
	}
	switch v.Type() {
	case reflect.TypeFor[time.Time]():
		*n++
		v.Set(reflect.ValueOf(time.Date(2026, 9, 24, 12, 0, *n, 0, time.UTC)))
		return
	case reflect.TypeFor[uuid.UUID]():
		v.Set(reflect.ValueOf(uuid.MustParse("f81d4fae-7dec-11d0-a765-00a0c91e6bf6")))
		return
	case reflect.TypeFor[jsontext.Value]():
		v.Set(reflect.ValueOf(jsontext.Value(`{"raw":true}`)))
		return
	case reflect.TypeFor[netip.Addr]():
		v.Set(reflect.ValueOf(netip.MustParseAddr("192.0.2.1")))
		return
	}
	*n++
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n, depth)
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() || v.Type().Field(i).Anonymous {
				fill(f, n, depth-1)
			}
		}
	case reflect.String:
		v.SetString("v" + strings.Repeat("x", *n%3))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n % 100))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n % 100))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n) + 0.5)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte("bytes"))
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), n, depth-1)
		v.Set(s)
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), n, depth-1)
		}
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		switch key.Kind() {
		case reflect.String:
			key.SetString("k" + string(rune('a'+*n%26)))
		case reflect.Int:
			key.SetInt(int64(*n))
		default:
			return
		}
		m := reflect.MakeMap(v.Type())
		elem := reflect.New(v.Type().Elem()).Elem()
		fill(elem, n, depth-1)
		m.SetMapIndex(key, elem)
		v.Set(m)
	case reflect.Interface:
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf("any"))
		}
	}
}

// outputs returns the values whose JSON must fit their output schemas: the
// zero and a filled value of every type of plantest and of everything,
// and the values that plantest checks.
func outputs() []any {
	var types []reflect.Type
	for _, v := range plantest.Layouts() {
		types = append(types, reflect.TypeOf(v))
	}
	for _, c := range plantest.Checks() {
		types = append(types, reflect.TypeOf(c.Value))
	}
	types = append(types, reflect.TypeFor[everything](), reflect.TypeFor[tree]())
	var values []any
	for _, typ := range types {
		zero, filled := reflect.New(typ).Elem(), reflect.New(typ).Elem()
		fill(filled, new(int), 4)
		values = append(values, zero.Interface(), filled.Interface())
	}
	for _, c := range plantest.Checks() {
		values = append(values, c.Value)
	}
	return values
}

func TestWrite(t *testing.T) {
	// Whatever json/v2 writes of a type fits its output schema.
	compiled := make(map[reflect.Type][]*jsonschema.Schema)
	for _, v := range outputs() {
		typ := reflect.TypeOf(v)
		data, err := json.Marshal(v)
		if err != nil {
			continue // NaN, ±Inf or a time.Duration, which JSON can't carry
		}
		if _, ok := compiled[typ]; !ok {
			for _, d := range dialects {
				compiled[typ] = append(compiled[typ], compile(t, typ, plan.Output, d))
			}
		}
		for i, sch := range compiled[typ] {
			if got := failures(t, sch, data); len(got) > 0 {
				t.Errorf("%s: %v writes %s, which fails its output schema at %q", dialects[i].name, typ, data, got)
			}
		}
	}
}

// Where the input schemas and the core differ, as check name → pointer →
// why. Looser: the core rejects the member and the schema accepts it, so a
// request that fits the schema may still fail; for the values that json/v2
// writes, the list is the whole of it, and TestDecodeGaps holds the JSON
// that fits a schema but doesn't decode. Stricter: the schema rejects what
// the core accepts.
var (
	looser = map[string]map[string]string{
		"invalid formats": {
			// Format uri is RFC 3986, and these are URIs; the url rule of
			// go-playground wants a host, a fragment or an opaque part, or a
			// path of a file URL.
			"/file":     "file:/// has an empty path",
			"/opaque":   "mailto: has an empty opaque part",
			"/fragment": "x:/ has neither a host nor a fragment",
		},
		"skipped fields": {
			// A field that JSON leaves out can't be sent, so no schema has it.
			"/Ignored": `json:"-" with required`,
		},
		"invalid methods": {
			// A type with methods of text or JSON writes its value as it
			// likes, such as a level as its name, and the rules check the
			// value, which its schema can't see.
			"/level": "oneof of an int of text",
			"/cents": "min of an int of JSON",
		},
	}
	stricter = map[string]map[string]string{
		"nesting": {
			// Without dive, the core doesn't check the elements of a slice,
			// and the schema of an element is that of its type.
			"/items/0/color": "an element of a slice",
		},
	}
)

func TestRead(t *testing.T) {
	// The input schema of a type rejects the members of a request that the
	// core rejects, at the member or within it, but for the lists above.
	// What the core reads is the JSON of the value, decoded as a request
	// is, so that nil slices are empty ones, as json/v2 writes them.
	for _, c := range plantest.Checks() {
		t.Run(c.Name, func(t *testing.T) {
			typ := reflect.TypeOf(c.Value)
			data, err := json.Marshal(c.Value)
			if err != nil {
				t.Skipf("json/v2 can't write it: %v", err) // NaN, ±Inf, time.Duration
			}
			req := reflect.New(typ)
			if err := json.Unmarshal(data, req.Interface()); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", data, err)
			}
			validation, err := plan.NewValidation(typ)
			if err != nil {
				t.Fatal(err)
			}
			var core []string
			for _, v := range validation.Validate(req.Elem()) {
				core = append(core, v.Pointer)
			}
			for _, d := range dialects {
				schema := deepest(failures(t, compile(t, typ, plan.Input, d), data))
				for _, p := range core {
					if !slices.ContainsFunc(schema, func(s string) bool { return within(s, p) }) && looser[c.Name][p] == "" {
						t.Errorf("%s: the core rejects %s in %s, the schema doesn't", d.name, p, data)
					}
				}
				for _, s := range schema {
					if !slices.ContainsFunc(core, func(p string) bool { return within(s, p) }) && stricter[c.Name][s] == "" {
						t.Errorf("%s: the schema rejects %s in %s, the core doesn't", d.name, s, data)
					}
				}
			}
		})
	}
}

// within reports whether the pointer p is q or points into it.
func within(p, q string) bool {
	return p == q || strings.HasPrefix(p, q+"/")
}

// deepest returns the pointers without those that others point into: a
// failure within a value explains the failure of the value, such as that
// of a branch of anyOf.
func deepest(pointers []string) []string {
	var out []string
	for _, p := range pointers {
		if !slices.ContainsFunc(pointers, func(q string) bool { return q != p && within(q, p) }) {
			out = append(out, p)
		}
	}
	return out
}
