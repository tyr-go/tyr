package plan

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/tyr-go/tyr/internal/jsonschema"
	"github.com/tyr-go/tyr/internal/plan/plantest"
)

var update = flag.Bool("update", false, "update the golden files in testdata")

// compact returns the JSON of v, or fails t.
func compact(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// selfJSON is a type that writes and reads itself as JSON.
type selfJSON struct{}

func (s selfJSON) MarshalJSON() ([]byte, error) { return []byte("[]"), nil }
func (s *selfJSON) UnmarshalJSON([]byte) error  { return nil }

func TestSchemaOfTypes(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[bool](), `{"type":"boolean"}`},
		{reflect.TypeFor[string](), `{"type":"string"}`},
		{reflect.TypeFor[int](), `{"type":"integer"}`},
		{reflect.TypeFor[int8](), `{"type":"integer","minimum":-128,"maximum":127}`},
		{reflect.TypeFor[int32](), `{"type":"integer","minimum":-2147483648,"maximum":2147483647}`},
		{reflect.TypeFor[uint16](), `{"type":"integer","minimum":0,"maximum":65535}`},
		{reflect.TypeFor[uint64](), `{"type":"integer","minimum":0}`},
		{reflect.TypeFor[float32](), `{"type":"number"}`},
		{reflect.TypeFor[time.Time](), `{"type":"string","format":"date-time"}`},
		{reflect.TypeFor[uuid.UUID](), `{"type":"string","format":"uuid"}`},
		{reflect.TypeFor[netip.Addr](), `{"type":"string"}`},
		{reflect.TypeFor[selfJSON](), `{}`},
		{reflect.TypeFor[jsontext.Value](), `{}`},
		{reflect.TypeFor[any](), `{}`},
		{reflect.TypeFor[[]byte](), `{"type":"string","contentEncoding":"base64"}`},
		{reflect.TypeFor[[2]byte](), `{"type":"string","minLength":4,"maxLength":4,"contentEncoding":"base64"}`},
		{reflect.TypeFor[[2]int](), `{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2}`},
		{reflect.TypeFor[[]*string](), `{"type":"array","items":{"type":["string","null"]}}`},
		{reflect.TypeFor[map[string]bool](), `{"type":"object","additionalProperties":{"type":"boolean"}}`},
		{reflect.TypeFor[map[int]string](), `{"type":"object","additionalProperties":{"type":"string"}}`},
		{reflect.TypeFor[*int](), `{"type":["integer","null"]}`},
		{reflect.TypeFor[struct {
			A int `json:"a"`
		}](), `{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]}`},
	}
	for _, tt := range tests {
		s := NewSchemas("#/$defs/")
		sch, err := s.Of(tt.typ, Output)
		if err != nil {
			t.Errorf("Of(%v) error = %v", tt.typ, err)
			continue
		}
		if got := compact(t, sch); got != tt.want {
			t.Errorf("Of(%v) = %s, want %s", tt.typ, got, tt.want)
		}
	}
}

// field returns a struct type with one field F of type typ with the tag,
// and the member of F in the direction dir.
func field(t *testing.T, typ reflect.Type, tag string, dir Direction) Member {
	t.Helper()
	st := reflect.StructOf([]reflect.StructField{{Name: "F", Type: typ, Tag: reflect.StructTag(`json:"f` + tag)}})
	s := NewSchemas("#/$defs/")
	ms, err := s.Members(st, dir)
	if err != nil {
		t.Fatalf("Members(%v) error = %v", st, err)
	}
	s.Defs() // names the references
	return ms[0]
}

func TestSchemaKeywords(t *testing.T) {
	tests := []struct {
		name     string
		typ      reflect.Type
		tag      string // after the name in the json tag
		want     string
		required bool
	}{
		{"min of a string", reflect.TypeFor[string](), `" validate:"min=4"`, `{"type":"string","minLength":4}`, true},
		{"required and a longer min", reflect.TypeFor[string](), `" validate:"required,min=4,max=16"`, `{"type":"string","minLength":4,"maxLength":16}`, true},
		{"omitempty", reflect.TypeFor[string](), `" validate:"omitempty,min=4"`, `{"type":"string","minLength":4}`, false},
		{"lt=0 of a string", reflect.TypeFor[string](), `" validate:"omitempty,lt=0"`, `{"type":"string","not":{}}`, false},
		{"gt of a slice", reflect.TypeFor[[]string](), `" validate:"gt=2"`, `{"type":"array","items":{"type":"string"},"minItems":3}`, true},
		{"max of a map", reflect.TypeFor[map[string]int](), `" validate:"max=2"`, `{"type":"object","additionalProperties":{"type":"integer"},"maxProperties":2}`, false},
		{"min of bytes", reflect.TypeFor[[]byte](), `" validate:"omitempty,min=4"`, `{"type":"string","contentEncoding":"base64"}`, false},
		{"gt of an int", reflect.TypeFor[int](), `" validate:"gt=0"`, `{"type":"integer","exclusiveMinimum":0}`, true},
		{"lt of a float", reflect.TypeFor[float64](), `" validate:"lt=10.5"`, `{"type":"number","exclusiveMaximum":10.5}`, false},
		{"len of an int", reflect.TypeFor[int](), `" validate:"len=5"`, `{"type":"integer","const":5}`, true},
		{"min above the type's", reflect.TypeFor[uint8](), `" validate:"min=5"`, `{"type":"integer","minimum":5,"maximum":255}`, true},
		{"max beyond the type's", reflect.TypeFor[uint8](), `" validate:"max=300"`, `{"type":"integer","minimum":0,"maximum":255}`, false},
		{"min=NaN", reflect.TypeFor[float64](), `" validate:"min=NaN"`, `{"type":"number","not":{}}`, true},
		{"max=+Inf", reflect.TypeFor[float64](), `" validate:"max=+Inf"`, `{"type":"number"}`, false},
		{"min=+Inf", reflect.TypeFor[float64](), `" validate:"min=+Inf"`, `{"type":"number","not":{}}`, true},
		{"required bool", reflect.TypeFor[bool](), `" validate:"required"`, `{"type":"boolean","const":true}`, true},
		{"required time", reflect.TypeFor[time.Time](), `" validate:"required"`, `{"type":"string","format":"date-time","not":{"const":"0001-01-01T00:00:00Z"}}`, true},
		{"required float", reflect.TypeFor[float64](), `" validate:"required"`, `{"type":"number","not":{"const":0}}`, true},
		{"required pointer", reflect.TypeFor[*int](), `" validate:"required"`, `{"type":"integer"}`, true},
		{"optional pointer", reflect.TypeFor[*int](), `" validate:"omitempty,min=1"`, `{"type":["integer","null"],"minimum":1}`, false},
		{"pointer without rules", reflect.TypeFor[*string](), `"`, `{"type":["string","null"]}`, false},
		{"oneof of strings", reflect.TypeFor[string](), `" validate:"oneof=red 'light blue'"`, `{"type":"string","enum":["red","light blue"]}`, true},
		{"oneof of ints", reflect.TypeFor[int](), `" validate:"omitempty,oneof=1 01 2"`, `{"type":"integer","enum":[1,2]}`, false},
		{"oneof of a pointer", reflect.TypeFor[*int](), `" validate:"omitempty,oneof=1 2"`, `{"type":["integer","null"],"enum":[1,2,null]}`, false},
		{"oneof of quoted ints", reflect.TypeFor[int](), `,string" validate:"omitempty,oneof=1 2"`, `{"type":"string","enum":["1","2"],"pattern":"^-?(0|[1-9][0-9]*)$"}`, false},
		{"min of a quoted int", reflect.TypeFor[int64](), `,string" validate:"min=3"`, `{"type":"string","pattern":"^-?(0|[1-9][0-9]*)$"}`, true},
		{"email", reflect.TypeFor[string](), `" validate:"omitempty,email"`, `{"type":"string","format":"email"}`, false},
		{"url", reflect.TypeFor[string](), `" validate:"omitempty,url"`, `{"type":"string","format":"uri"}`, false},
		{"http_url", reflect.TypeFor[string](), `" validate:"omitempty,http_url"`, `{"type":"string","format":"uri","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://[^/?#]"}`, false},
		{"uuid", reflect.TypeFor[string](), `" validate:"omitempty,uuid"`, `{"type":"string","format":"uuid"}`, false},
		{"uuid of a Stringer", reflect.TypeFor[uuid.UUID](), `" validate:"uuid"`, `{"type":"string","format":"uuid"}`, false},
		// The rules check the value of a type with methods of JSON or text,
		// whose JSON may be anything: they add no keywords.
		{"oneof of an int of text", reflect.TypeFor[plantest.Level](), `" validate:"oneof=1 2"`, `{"type":"string"}`, true},
		{"min of an int of JSON", reflect.TypeFor[plantest.Cents](), `" validate:"omitempty,min=100"`, `{}`, false},
		{"oneof of a pointer to an int of text", reflect.TypeFor[*plantest.Level](), `" validate:"omitempty,oneof=1 2"`, `{"type":["string","null"]}`, false},
		{"required struct", reflect.TypeFor[plantest.Inner](), `" validate:"required"`, `{"$ref":"#/$defs/Inner"}`, true},
		{"struct with required members", reflect.TypeFor[plantest.Profile](), `"`, `{"$ref":"#/$defs/Profile"}`, true},
		{"described", reflect.TypeFor[string](), `" doc:"The code."`, `{"type":"string"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := field(t, tt.typ, tt.tag, Input)
			if got := compact(t, m.Schema); got != tt.want || m.Required != tt.required {
				t.Errorf("schema %s, required %v; want %s, %v", got, m.Required, tt.want, tt.required)
			}
		})
	}

	// The description goes beside the schema, and into its property.
	m := field(t, reflect.TypeFor[plantest.Inner](), `" doc:"The inner struct."`, Output)
	want := `{"type":"object","properties":{"f":{"description":"The inner struct.","allOf":[{"$ref":"#/$defs/Inner"}]}},"required":["f"]}`
	if m.Description != "The inner struct." || compact(t, Object([]Member{m})) != want {
		t.Errorf("described member %q, object %s; want %s", m.Description, compact(t, Object([]Member{m})), want)
	}
}

func TestRuleKeywords(t *testing.T) {
	// Every rule adds its keywords, next to its check, for every type it
	// applies to.
	params := map[string]string{"min": "1", "max": "1", "len": "1", "gt": "1", "gte": "1", "lt": "1", "lte": "1", "oneof": "1 2"}
	types := []reflect.Type{
		reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[uint](), reflect.TypeFor[float64](),
		reflect.TypeFor[[]int](), reflect.TypeFor[[]byte](), reflect.TypeFor[map[string]int](), reflect.TypeFor[uuid.UUID](),
	}
	for _, name := range ruleNames {
		applied := 0
		for _, typ := range types {
			r, err := newRule(name, params[name], typ)
			if err != nil {
				continue // the rule doesn't apply to typ
			}
			applied++
			if r.keywords == nil {
				t.Errorf("rule %s for %v has no keywords", name, typ)
			}
		}
		if applied == 0 {
			t.Errorf("rule %s applies to none of %v", name, types)
		}
	}
}

// Base has the name of plantest.Base.
type Base struct {
	X int `json:"x"`
}

// page is a generic type.
type page[T any] struct {
	Items []T `json:"items"`
}

// nodeInput has the name that the input definition of node gets.
type nodeInput struct {
	Name string `json:"name"`
}

// node is a recursive type whose input and output schemas differ.
type node struct {
	Name     string `json:"name" validate:"required"`
	Children []node `json:"children,omitempty"`
}

// opts is a type whose input and output schemas are the same.
type opts struct {
	A string `json:"a,omitempty"`
}

// both returns the names of the definitions that the schemas of types
// make, in both directions for those in both, and the references to them.
func both(t *testing.T, s *Schemas, inputs, outputs []reflect.Type) (names []string, refs map[string]string) {
	t.Helper()
	refs = make(map[string]string)
	schemas := make(map[string]*jsonschema.Schema)
	for dir, types := range [][]reflect.Type{inputs, outputs} {
		for _, typ := range types {
			sch, err := s.Of(typ, Direction(dir))
			if err != nil {
				t.Fatal(err)
			}
			schemas[typ.String()+"/"+Direction(dir).String()] = sch
		}
	}
	for _, d := range s.Defs() {
		names = append(names, d.Name)
	}
	for key, sch := range schemas {
		refs[key] = sch.Ref.Ref
	}
	return names, refs
}

func TestSchemaNames(t *testing.T) {
	s := NewSchemas("#/components/schemas/")
	names, refs := both(t, s,
		[]reflect.Type{reflect.TypeFor[node](), reflect.TypeFor[opts]()},
		[]reflect.Type{
			reflect.TypeFor[Base](), reflect.TypeFor[plantest.Base](), reflect.TypeFor[page[plantest.Inner]](),
			reflect.TypeFor[node](), reflect.TypeFor[opts](), reflect.TypeFor[nodeInput](),
		})
	// Types of one name get their packages; the input of node and the type
	// nodeInput share a path too, so they get numbers.
	want := []string{
		"Inner",
		"github.com.tyr-go.tyr.internal.plan.nodeInput",
		"github.com.tyr-go.tyr.internal.plan.nodeInput_2",
		"node",
		"opts",
		"page_Inner",
		"plan.Base",
		"plantest.Base",
	}
	if !slices.Equal(names, want) {
		t.Errorf("Defs() names:\n%q\nwant:\n%q", names, want)
	}
	for key, want := range map[string]string{
		"plan.node/output":     "#/components/schemas/node",
		"plan.node/input":      "#/components/schemas/github.com.tyr-go.tyr.internal.plan.nodeInput",
		"plan.opts/input":      "#/components/schemas/opts", // the same as the output: one definition
		"plan.opts/output":     "#/components/schemas/opts",
		"plan.Base/output":     "#/components/schemas/plan.Base",
		"plantest.Base/output": "#/components/schemas/plantest.Base",
	} {
		if refs[key] != want {
			t.Errorf("reference of %s = %q, want %q", key, refs[key], want)
		}
	}

	// A name of one's own.
	s = NewSchemas("#/$defs/")
	s.Name(reflect.TypeFor[Base](), "Point")
	if names, _ := both(t, s, nil, []reflect.Type{reflect.TypeFor[Base]()}); !slices.Equal(names, []string{"Point"}) {
		t.Errorf("Defs() with Name = %q, want [Point]", names)
	}
}

func TestSchemaErrors(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		dir  Direction
		want string
	}{
		{reflect.TypeFor[struct {
			D time.Duration `json:"d"`
		}](), Output, ".D: json/v2 has no representation of time.Duration"},
		{reflect.TypeFor[struct {
			F func() `json:"f"`
		}](), Output, ".F: json/v2 has no representation of func()"},
		{reflect.TypeFor[struct {
			C chan int `json:"c"`
		}](), Input, ".C: json/v2 has no representation of chan int"},
		{reflect.TypeFor[struct {
			B bool `json:"b,string"`
		}](), Output, ".B: the string option of json/v2 is for numbers, not bool"},
		{reflect.TypeFor[map[bool]int](), Output, "json/v2 can't write the keys of map[bool]int as the names of members"},
		{reflect.TypeFor[struct {
			N struct {
				X int `json:"x" validate:"maxx=1"`
			} `json:"n"`
		}](), Input, `unknown rule "maxx"`},
		{reflect.TypeFor[struct {
			A string `json:"B"` // B's name
			B string
		}](), Output, "json/v2 can't use"},
	}
	for _, tt := range tests {
		_, err := NewSchemas("#/$defs/").Of(tt.typ, tt.dir)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Of(%v) error = %v, want one with %q", tt.typ, err, tt.want)
		}
	}
}

// sample has members of each kind of requiredness.
type sample struct {
	Code    string    `json:"code" validate:"required,min=4,max=16" doc:"The code of the link."`
	URL     string    `json:"url" validate:"required,http_url"`
	Note    string    `json:"note,omitempty"`
	Count   *int      `json:"count" validate:"omitempty,min=1"`
	Tags    []string  `json:"tags" validate:"max=3"`
	Level   uint8     `json:"level" validate:"oneof=1 2"`
	ID      int64     `json:"id,string"`
	Created time.Time `json:"created,omitzero"`
	Inner   *plantest.Inner
	Skip    string `json:"-"`
}

// extra is embedded through a pointer.
type extra struct {
	Note string `json:"note" validate:"required"`
}

// embedded promotes members of an embedded struct and one behind a
// pointer, and keeps unknown members.
type embedded struct {
	plantest.Paging
	*extra
	Own  string         `json:"own"`
	Rest map[string]int `json:",embed"` //nolint:staticcheck // SA5008 doesn't know the embed option of json/v2
}

func TestSchemaGolden(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[sample](), reflect.TypeFor[embedded](), reflect.TypeFor[node]()} {
		for _, dir := range []Direction{Input, Output} {
			s := NewSchemas("#/$defs/")
			sch, err := s.Of(typ, dir)
			if err != nil {
				t.Fatalf("Of(%v, %v) error = %v", typ, dir, err)
			}
			var defs jsonschema.Properties
			for _, d := range s.Defs() {
				defs = append(defs, jsonschema.Property{Name: d.Name, Schema: d.Schema})
			}
			got, err := json.Marshal(struct {
				Schema *jsonschema.Schema    `json:"schema"`
				Defs   jsonschema.Properties `json:"$defs"`
			}{sch, defs}, jsontext.WithIndent("  "))
			if err != nil {
				t.Fatal(err)
			}
			golden(t, "schema_"+typ.Name()+"_"+dir.String(), got)
		}
	}
}

// golden compares got with the golden file testdata/name.json, or writes
// the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	got = append(bytes.Clone(got), '\n')
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s:\ngot  %s\nwant %s", path, got, want)
	}
}
