package plan_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr/internal/plan"
	"github.com/tyr-go/tyr/internal/plan/plantest"
)

func TestValidate(t *testing.T) {
	for _, c := range plantest.Checks() {
		t.Run(c.Name, func(t *testing.T) {
			v, err := plan.NewValidation(reflect.TypeOf(c.Value))
			if err != nil {
				t.Fatalf("NewValidation() error = %v", err)
			}
			var got []plantest.Violation
			for _, x := range v.Validate(reflect.ValueOf(c.Value)) {
				got = append(got, plantest.Violation{Pointer: x.Pointer, Detail: x.Detail})
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("Validate() =\n%q\nwant\n%q", got, c.Want)
			}
		})
	}
}

func TestNewValidationNothingToValidate(t *testing.T) {
	type noTags struct {
		A string `json:"a"`
		B struct {
			C int `json:"c"`
		} `json:"b"`
	}
	if v, err := plan.NewValidation(reflect.TypeFor[noTags]()); v != nil || err != nil {
		t.Errorf("NewValidation() = %v, %v; want nil, nil", v, err)
	}
}

type node struct {
	Name string `json:"name" validate:"required"`
	Next *node  `json:"next"`
}

func TestValidateRecursiveType(t *testing.T) {
	v, err := plan.NewValidation(reflect.TypeFor[node]())
	if err != nil {
		t.Fatalf("NewValidation() error = %v", err)
	}
	got := v.Validate(reflect.ValueOf(node{Name: "a", Next: &node{Next: &node{}}}))
	want := []plan.Violation{
		{Pointer: "/next/name", Detail: "is required"},
		{Pointer: "/next/next/name", Detail: "is required"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Validate() = %q, want %q", got, want)
	}
}

// code is a fmt.Stringer, which uuid checks.
type id [2]byte

func (id) String() string { return "f81d4fae-7dec-11d0-a765-00a0c91e6bf6" }

func TestValidateStringer(t *testing.T) {
	type withID struct {
		ID id `json:"id" validate:"uuid"`
	}
	v, err := plan.NewValidation(reflect.TypeFor[withID]())
	if err != nil {
		t.Fatalf("NewValidation() error = %v", err)
	}
	if got := v.Validate(reflect.ValueOf(withID{})); got != nil {
		t.Errorf("Validate() = %q, want no violations", got)
	}
}

func TestValidateDurationInNanoseconds(t *testing.T) {
	// As in go-playground, a duration's parameter that isn't a duration is
	// read as nanoseconds.
	type withDuration struct {
		D time.Duration `json:"d" validate:"max=1000"`
	}
	v, err := plan.NewValidation(reflect.TypeFor[withDuration]())
	if err != nil {
		t.Fatalf("NewValidation() error = %v", err)
	}
	got := v.Validate(reflect.ValueOf(withDuration{D: 2 * time.Microsecond}))
	if want := []plan.Violation{{Pointer: "/d", Detail: "must be at most 1µs"}}; !slices.Equal(got, want) {
		t.Errorf("Validate() = %q, want %q", got, want)
	}
}

func TestNewValidationErrors(t *testing.T) {
	const hint = "; check the tags with tyr.WithValidator(playground.New()) of the module validate/playground for more rules, or move the check to Validate()"
	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			"unknown rule",
			reflect.TypeFor[struct {
				A string `validate:"required,maxx=4"`
			}](),
			`field A: validate:"required,maxx=4": unknown rule "maxx"` + hint,
		},
		{
			"alternatives",
			reflect.TypeFor[struct {
				A string `validate:"email|url"`
			}](),
			`field A: validate:"email|url": alternatives with | aren't supported` + hint,
		},
		{
			"rule for another type",
			reflect.TypeFor[struct {
				A int `validate:"email"`
			}](),
			`field A: validate:"email": rule "email" doesn't apply to int`,
		},
		{
			"comparison of a bool",
			reflect.TypeFor[struct {
				A bool `validate:"min=1"`
			}](),
			`field A: validate:"min=1": rule "min" doesn't apply to bool`,
		},
		{
			"comparison of a time",
			reflect.TypeFor[struct {
				At time.Time `validate:"gt"`
			}](),
			`field At: validate:"gt": rule "gt" doesn't apply to time.Time: the core supports only required and omitempty for times`,
		},
		{
			"oneof of a float",
			reflect.TypeFor[struct {
				A float64 `validate:"oneof=1 2"`
			}](),
			`field A: validate:"oneof=1 2": rule "oneof" doesn't apply to float64`,
		},
		{
			"oneof without values",
			reflect.TypeFor[struct {
				A string `validate:"oneof="`
			}](),
			`field A: validate:"oneof=": rule oneof needs values`,
		},
		{
			"bad parameter",
			reflect.TypeFor[struct {
				A string `validate:"min=two"`
			}](),
			`field A: validate:"min=two": rule min=two: bad parameter for string: ...`,
		},
		{
			"bad integer",
			reflect.TypeFor[struct {
				A int `validate:"min=x"`
			}](),
			`field A: validate:"min=x": rule min=x: bad parameter for int: ...`,
		},
		{
			"bad unsigned integer",
			reflect.TypeFor[struct {
				A uint `validate:"max=-1"`
			}](),
			`field A: validate:"max=-1": rule max=-1: bad parameter for uint: ...`,
		},
		{
			"bad float",
			reflect.TypeFor[struct {
				A float32 `validate:"lt=x"`
			}](),
			`field A: validate:"lt=x": rule lt=x: bad parameter for float32: ...`,
		},
		{
			"parameter of required",
			reflect.TypeFor[struct {
				A string `validate:"required=true"`
			}](),
			`field A: validate:"required=true": rule "required" takes no parameter`,
		},
		{
			"parameter of email",
			reflect.TypeFor[struct {
				A string `validate:"email=x"`
			}](),
			`field A: validate:"email=x": rule "email" takes no parameter`,
		},
		{
			"field of a nested struct",
			reflect.TypeFor[struct {
				Profile struct {
					Color string `validate:"colour"`
				}
			}](),
			`field Profile.Color: validate:"colour": unknown rule "colour"` + hint,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := plan.NewValidation(tt.typ)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if prefix, ok := strings.CutSuffix(tt.want, "..."); ok && !strings.HasPrefix(got, prefix) || !ok && got != tt.want {
				t.Errorf("NewValidation() error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFailures(t *testing.T) {
	// Failures, with the pointers of Pointer and the details of Detail, says
	// what Validate says: a validator of its own that reports what the core
	// reports gets the violations of the core.
	for _, c := range plantest.Checks() {
		t.Run(c.Name, func(t *testing.T) {
			typ := reflect.TypeOf(c.Value)
			v, err := plan.NewValidation(typ)
			if err != nil {
				t.Fatalf("NewValidation() error = %v", err)
			}
			var got []plantest.Violation
			for _, f := range v.Failures(reflect.ValueOf(c.Value)) {
				p, vt, err := plan.Pointer(typ, f.Path)
				if err != nil {
					t.Fatalf("Pointer(%v) error = %v", f.Path, err)
				}
				d, ok := plan.Detail(f.Rule, f.Param, vt)
				if !ok {
					t.Fatalf("Detail(%q, %q, %v) is unknown", f.Rule, f.Param, vt)
				}
				got = append(got, plantest.Violation{Pointer: p, Detail: d})
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("Failures() =\n%q\nwant\n%q", got, c.Want)
			}
		})
	}
}

func TestPointer(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	type page struct {
		Limit int `json:"limit"`
	}
	type named struct {
		Size int `json:"size"`
	}
	type req struct {
		page
		named   `json:"named"`
		Profile *struct {
			Color string `json:"color"`
		} `json:"profile"`
		Items  []*item            `json:"items"`
		Grid   [2][]int           `json:"grid"`
		Labels map[string]*string `json:"labels"`
		Secret string             `json:"-"`
		Plain  bool
	}
	tests := []struct {
		path []string
		want string
		typ  reflect.Type
	}{
		{nil, "", reflect.TypeFor[req]()},
		{[]string{"page", "Limit"}, "/limit", reflect.TypeFor[int]()},
		{[]string{"named", "Size"}, "/named/size", reflect.TypeFor[int]()},
		{[]string{"Profile", "Color"}, "/profile/color", reflect.TypeFor[string]()},
		{[]string{"Items", "3", "Name"}, "/items/3/name", reflect.TypeFor[string]()},
		{[]string{"Items", "0"}, "/items/0", reflect.TypeFor[item]()},
		{[]string{"Grid", "1", "0"}, "/grid/1/0", reflect.TypeFor[int]()},
		{[]string{"Labels", "a/b~c"}, "/labels/a~1b~0c", reflect.TypeFor[string]()},
		{[]string{"Secret"}, "/Secret", reflect.TypeFor[string]()},
		{[]string{"Plain"}, "/Plain", reflect.TypeFor[bool]()},
	}
	for _, tt := range tests {
		got, typ, err := plan.Pointer(reflect.TypeFor[req](), tt.path)
		if err != nil || got != tt.want || typ != tt.typ {
			t.Errorf("Pointer(%q) = %q, %v, %v; want %q, %v", tt.path, got, typ, err, tt.want, tt.typ)
		}
	}

	for _, path := range [][]string{
		{"Nope"},              // no such field
		{"Limit"},             // promoted, not a field of req itself
		{"Items", "x"},        // not an index
		{"Items", "-1"},       // not an index
		{"Plain", "Deeper"},   // past a bool
		{"Profile", "Colour"}, // no such field below
	} {
		if got, _, err := plan.Pointer(reflect.TypeFor[req](), path); err == nil {
			t.Errorf("Pointer(%q) = %q, want an error", path, got)
		}
	}
}

func TestDetail(t *testing.T) {
	tests := []struct {
		rule, param string
		typ         reflect.Type
		want        string
		ok          bool
	}{
		{"required", "", reflect.TypeFor[string](), "is required", true},
		{"min", "4", reflect.TypeFor[string](), "must be at least 4 characters", true},
		{"min", "2", reflect.TypeFor[[]int](), "must have at least 2 items", true},
		{"max", "10", reflect.TypeFor[int](), "must be at most 10", true},
		{"oneof", "red green", reflect.TypeFor[string](), "must be one of: red, green", true},
		{"omitempty", "", reflect.TypeFor[string](), "", false}, // it never fails
		{"e164", "", reflect.TypeFor[string](), "", false},      // not a rule of the core
		{"min", "1", reflect.TypeFor[time.Time](), "", false},   // not for times in the core
		{"min", "x", reflect.TypeFor[string](), "", false},      // a parameter the core can't read
	}
	for _, tt := range tests {
		if got, ok := plan.Detail(tt.rule, tt.param, tt.typ); got != tt.want || ok != tt.ok {
			t.Errorf("Detail(%q, %q, %v) = %q, %v; want %q, %v", tt.rule, tt.param, tt.typ, got, ok, tt.want, tt.ok)
		}
	}
}
