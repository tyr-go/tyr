package plan

import (
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tyr-go/tyr/internal/plan/plantest"
)

// Types of enums that aren't, or aren't right, for TestEnumOf.
type (
	ptrEnum    string
	badSig     string
	sliceEnum  []int
	noValues   string
	twice      string
	sameJSON   int
	mixedJSON  int
	boolEnum   bool
	boomEnum   string
	notEnum    string
	floatsEnum float64
)

func (*ptrEnum) EnumValues() []ptrEnum    { return []ptrEnum{"a", "b"} }
func (badSig) EnumValues() []string       { return nil }
func (sliceEnum) EnumValues() []sliceEnum { return nil }
func (noValues) EnumValues() []noValues   { return nil }
func (twice) EnumValues() []twice         { return []twice{"a", "a"} }
func (sameJSON) EnumValues() []sameJSON   { return []sameJSON{1, 2} }
func (sameJSON) MarshalText() ([]byte, error) {
	return []byte("same"), nil
}
func (mixedJSON) EnumValues() []mixedJSON { return []mixedJSON{1, 2} }
func (m mixedJSON) MarshalJSON() ([]byte, error) {
	if m == 1 {
		return []byte(`"one"`), nil
	}
	return []byte("2"), nil
}
func (boolEnum) EnumValues() []boolEnum     { return []boolEnum{true, false} }
func (boomEnum) EnumValues() []boomEnum     { panic("boom") }
func (floatsEnum) EnumValues() []floatsEnum { return []floatsEnum{0.5, 1} }

func TestEnumOf(t *testing.T) {
	tests := []struct {
		typ    reflect.Type
		json   string // of the values
		detail string
		schema string
	}{
		{reflect.TypeFor[plantest.Status](), `["todo","doing","done"]`, "must be one of: todo, doing, done", "string"},
		{reflect.TypeFor[plantest.Priority](), `[1,2,3]`, "must be one of: 1, 2, 3", "integer"},
		{reflect.TypeFor[plantest.Size](), `["small","medium","large"]`, "must be one of: small, medium, large", "string"},
		{reflect.TypeFor[ptrEnum](), `["a","b"]`, "must be one of: a, b", "string"},
		{reflect.TypeFor[floatsEnum](), `[0.5,1]`, "must be one of: 0.5, 1", "number"},
	}
	for _, tt := range tests {
		e, err := enumOf(tt.typ)
		if err != nil || e == nil {
			t.Errorf("enumOf(%v) = %v, %v", tt.typ, e, err)
			continue
		}
		data, _ := json.Marshal(e.json)
		if string(data) != tt.json || e.detail != tt.detail || e.schema[0] != tt.schema {
			t.Errorf("enumOf(%v) = %s %q %v, want %s %q %s", tt.typ, data, e.detail, e.schema, tt.json, tt.detail, tt.schema)
		}
	}

	for _, typ := range []reflect.Type{reflect.TypeFor[notEnum](), reflect.TypeFor[string](), reflect.TypeFor[*plantest.Status](), reflect.TypeFor[struct{}]()} {
		if e, err := enumOf(typ); e != nil || err != nil {
			t.Errorf("enumOf(%v) = %v, %v; want no enum", typ, e, err)
		}
	}

	errs := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[badSig](), "plan.badSig has a method EnumValues, but not EnumValues() []badSig, as tyr.Enum has it"},
		{reflect.TypeFor[sliceEnum](), "plan.sliceEnum has a method EnumValues, but isn't comparable, as the type of an enum must be"},
		{reflect.TypeFor[noValues](), "plan.noValues.EnumValues returns no values"},
		{reflect.TypeFor[twice](), `plan.twice.EnumValues has "a" twice`},
		{reflect.TypeFor[sameJSON](), `two values of plan.sameJSON.EnumValues write "same"`},
		{reflect.TypeFor[mixedJSON](), "plan.mixedJSON.EnumValues: the values write strings and numbers both, and those of an enum are all strings or all numbers"},
		{reflect.TypeFor[boolEnum](), "plan.boolEnum.EnumValues: true writes true, and the values of an enum write JSON strings or numbers"},
		{reflect.TypeFor[boomEnum](), "plan.boomEnum.EnumValues panicked: boom"},
	}
	for _, tt := range errs {
		if _, err := enumOf(tt.typ); err == nil || err.Error() != tt.want {
			t.Errorf("enumOf(%v) error = %v, want %q", tt.typ, err, tt.want)
		}
	}
}

// violations returns the failures as the violations of plantest.
func violations(fs []EnumFailure) []plantest.Violation {
	var out []plantest.Violation
	for _, f := range fs {
		out = append(out, plantest.Violation{Pointer: f.Pointer, Detail: f.Detail})
	}
	return out
}

func TestEnumCheckInput(t *testing.T) {
	check, err := NewEnumCheck(reflect.TypeFor[plantest.Enums](), Input)
	if err != nil || check == nil {
		t.Fatalf("NewEnumCheck = %v, %v", check, err)
	}
	for _, c := range plantest.EnumChecks() {
		v := reflect.ValueOf(c.Value)
		if got := violations(check.Failures(v, false)); !slices.Equal(got, c.Want) {
			t.Errorf("%s: failures\n%v\nwant\n%v", c.Name, got, c.Want)
		}
		if ok := check.OK(v); ok != (len(c.Want) == 0) {
			t.Errorf("%s: OK = %v, want %v", c.Name, ok, len(c.Want) == 0)
		}
		if first := check.Failures(v, true); len(c.Want) > 0 && (len(first) != 1 || first[0].Pointer != c.Want[0].Pointer) {
			t.Errorf("%s: the first failure = %v, want %v", c.Name, violations(first), c.Want[:1])
		}
	}
}

// result has values of enums in the ways a result writes them.
type result struct {
	Status   plantest.Status   `json:"status"`
	Omitted  plantest.Status   `json:"omitted,omitzero"`
	Empty    plantest.Status   `json:"empty,omitempty"`
	Priority plantest.Priority `json:"priority,omitempty"`
	Ptr      *plantest.Status  `json:"ptr"`
	List     []plantest.Status `json:"list"`
	Header   plantest.Status   `json:"-" header:"X-Status"`
	Hidden   plantest.Status   `json:"-"`
}

func TestEnumCheckOutput(t *testing.T) {
	check, err := NewEnumCheck(reflect.TypeFor[result](), Output)
	if err != nil || check == nil {
		t.Fatalf("NewEnumCheck = %v, %v", check, err)
	}
	valid := result{Status: plantest.StatusDone, Priority: 1, List: []plantest.Status{plantest.StatusTodo}, Header: plantest.StatusDoing}
	if !check.OK(reflect.ValueOf(valid)) || check.Failures(reflect.ValueOf(valid), false) != nil {
		t.Errorf("%+v fails, want valid: json/v2 leaves out Omitted and Empty, and Hidden isn't written", valid)
	}

	// What json/v2 writes counts, zero values too.
	bad := result{List: []plantest.Status{"later"}, Ptr: new(plantest.Status)}
	got := check.Failures(reflect.ValueOf(bad), false)
	want := []struct {
		pointer string
		field   bool
		value   string
	}{
		{"/status", true, `""`},
		{"/priority", true, "0"}, // omitempty leaves out an empty JSON value, and 0 isn't one
		{"/ptr", true, `""`},
		{"/list/0", false, `"later"`},
		{"/Header", true, `""`},
	}
	if len(got) != len(want) {
		t.Fatalf("failures %v, want %d", violations(got), len(want))
	}
	for i, f := range got {
		value, _ := json.Marshal(f.Value.Interface())
		if f.Pointer != want[i].pointer || f.Field != want[i].field || string(value) != want[i].value {
			t.Errorf("failure %d = %s field %v value %s, want %+v", i, f.Pointer, f.Field, value, want[i])
		}
	}
	if first := check.Failures(reflect.ValueOf(bad), true); len(first) != 1 || first[0].Pointer != "/status" {
		t.Errorf("the first failure = %v, want /status", violations(first))
	}

	// The result itself may be of an enum type.
	whole, err := NewEnumCheck(reflect.TypeFor[plantest.Status](), Output)
	if err != nil || whole == nil {
		t.Fatal(err)
	}
	if fs := whole.Failures(reflect.ValueOf(plantest.Status("x")), true); len(fs) != 1 || fs[0].Pointer != "" || fs[0].Field {
		t.Errorf("the failure of a result of an enum = %+v, want one at \"\", not of a field", fs)
	}
}

// enumTree is a recursive type with an enum, and plainTree one without.
type (
	enumTree struct {
		Status plantest.Status `json:"status"`
		Kids   []enumTree      `json:"kids"`
		Next   *enumTree       `json:"next"`
	}
	plainTree struct {
		Name string      `json:"name"`
		Kids []plainTree `json:"kids"`
	}
	// deepEnum reaches its enum through a cycle it enters first.
	deepEnum struct {
		A *deepA `json:"a"`
	}
	deepA struct {
		B *deepB `json:"b"`
	}
	deepB struct {
		A      *deepA          `json:"a"`
		Status plantest.Status `json:"status"`
	}
)

func TestEnumCheckTypes(t *testing.T) {
	// Types without enums, recursive ones too, need no check.
	for _, typ := range []reflect.Type{reflect.TypeFor[plainTree](), reflect.TypeFor[plantest.Strings](), reflect.TypeFor[plantest.Methods]()} {
		for _, dir := range []Direction{Input, Output} {
			if c, err := NewEnumCheck(typ, dir); c != nil || err != nil {
				t.Errorf("NewEnumCheck(%v, %v) = %v, %v; want none", typ, dir, c, err)
			}
		}
	}

	// A recursive type is checked as deep as the value goes.
	check, err := NewEnumCheck(reflect.TypeFor[enumTree](), Output)
	if err != nil || check == nil {
		t.Fatal(err)
	}
	tree := enumTree{Status: "todo", Kids: []enumTree{{Status: "done"}}, Next: &enumTree{Status: "done", Kids: []enumTree{{Status: "odd"}}}}
	if got := violations(check.Failures(reflect.ValueOf(tree), false)); len(got) != 1 || got[0].Pointer != "/next/kids/0/status" {
		t.Errorf("failures of a tree = %v, want /next/kids/0/status", got)
	}

	// A type whose enum is behind a cycle finds it.
	deep, err := NewEnumCheck(reflect.TypeFor[deepEnum](), Input)
	if err != nil || deep == nil {
		t.Fatalf("NewEnumCheck(deepEnum) = %v, %v; want a check", deep, err)
	}
	v := deepEnum{A: &deepA{B: &deepB{Status: "odd"}}}
	if got := violations(deep.Failures(reflect.ValueOf(v), false)); len(got) != 1 || got[0].Pointer != "/a/b/status" {
		t.Errorf("failures of deepEnum = %v, want /a/b/status", got)
	}

	// Numbers can't name the members of a JSON object.
	_, err = NewEnumCheck(reflect.TypeFor[map[plantest.Priority]string](), Output)
	if want := "the keys of map[plantest.Priority]string are of plantest.Priority, an enum of numbers, which can't name the members of a JSON object"; err == nil || err.Error() != want {
		t.Errorf("an enum of numbers as keys: %v, want %q", err, want)
	}
	// An enum that makes no enum fails the check of a type that has it.
	_, err = NewEnumCheck(reflect.TypeFor[struct{ T twice }](), Input)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a bad enum in a field: %v, want its error", err)
	}
}

func TestEnumCheckAllocs(t *testing.T) {
	// A valid value, such as every result that a server sends, costs no
	// allocation, but for the keys and values of maps.
	check, err := NewEnumCheck(reflect.TypeFor[enumTree](), Output)
	if err != nil {
		t.Fatal(err)
	}
	tree := reflect.ValueOf(enumTree{Status: "todo", Kids: []enumTree{{Status: "done"}, {Status: "doing"}}, Next: &enumTree{Status: "done"}})
	if n := testing.AllocsPerRun(100, func() { check.Failures(tree, true) }); n != 0 {
		t.Errorf("a check of a valid value allocates %v times, want 0", n)
	}
}

func ExampleEnumCheck() {
	check, _ := NewEnumCheck(reflect.TypeFor[plantest.Enums](), Input)
	req := plantest.Enums{Status: "archived", List: []plantest.Status{"todo", "later"}}
	for _, f := range check.Failures(reflect.ValueOf(req), false) {
		fmt.Println(f.Pointer, f.Detail)
	}
	// Output:
	// /status must be one of: todo, doing, done
	// /list/1 must be one of: todo, doing, done
}
