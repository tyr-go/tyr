package plan_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tyr-go/tyr/internal/plan"
)

// code is a type that parses itself.
type code string

func (c *code) UnmarshalText(b []byte) error {
	if len(b) < 2 {
		return errors.New("must be at least 2 characters")
	}
	*c = code(b)
	return nil
}

type bound struct {
	Str     string     `json:"str" query:"str"`
	Bool    bool       `json:"bool" query:"bool"`
	Int8    int8       `json:"int8" query:"int8"`
	Int     int        `json:"int" query:"int"`
	Uint8   uint8      `json:"uint8" query:"uint8"`
	Float   float64    `json:"float" query:"float"`
	Time    time.Time  `json:"time" query:"time"`
	Code    code       `json:"code" query:"code"`
	Ints    []int      `json:"ints" query:"ints"`
	Path    string     `json:"path" path:"path"`
	Head    string     `json:"head" header:"x-head"`
	NoTag   string     `json:"no_tag"`
	GoNamed string     `query:"go_named"`
	IntPtr  *int       `json:"int_ptr" query:"int_ptr"`
	StrPtr  *string    `json:"str_ptr" query:"str_ptr"`
	TimePtr *time.Time `json:"time_ptr" query:"time_ptr"`
	CodePtr *code      `json:"code_ptr" query:"code_ptr"`
}

func TestNewBinding(t *testing.T) {
	b, err := plan.NewBinding(reflect.TypeFor[bound]())
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}

	type field struct {
		Source             plan.Source
		Name, GoName, JSON string
	}
	var got []field
	for _, f := range b.Fields {
		got = append(got, field{f.Source, f.Name, f.GoName, f.JSON})
	}
	want := []field{
		{plan.Query, "str", "Str", "str"},
		{plan.Query, "bool", "Bool", "bool"},
		{plan.Query, "int8", "Int8", "int8"},
		{plan.Query, "int", "Int", "int"},
		{plan.Query, "uint8", "Uint8", "uint8"},
		{plan.Query, "float", "Float", "float"},
		{plan.Query, "time", "Time", "time"},
		{plan.Query, "code", "Code", "code"},
		{plan.Query, "ints", "Ints", "ints"},
		{plan.Path, "path", "Path", "path"},
		{plan.Header, "X-Head", "Head", "head"},        // canonical header name
		{plan.Query, "go_named", "GoNamed", "GoNamed"}, // no json tag: the Go name
		{plan.Query, "int_ptr", "IntPtr", "int_ptr"},
		{plan.Query, "str_ptr", "StrPtr", "str_ptr"},
		{plan.Query, "time_ptr", "TimePtr", "time_ptr"},
		{plan.Query, "code_ptr", "CodePtr", "code_ptr"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("NewBinding().Fields =\n%v\nwant\n%v", got, want)
	}
}

type Pagination struct {
	Limit int `json:"limit" query:"limit"`
}

type withNested struct {
	Page Pagination `json:"page"`
}

type withIgnored struct {
	A string `json:"-" query:"a"`
}

type withShadowed struct {
	Pagination
	Limit int `json:"limit"`
}

type withEmbeddedPointer struct {
	*Pagination
	Own string `json:"own" query:"own"`
}

func TestNewBindingErrors(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			"unsupported type",
			reflect.TypeFor[struct {
				M map[string]int `query:"m"`
			}](),
			`field M has query:"m", but its type map[string]int can't be bound`,
		},
		{
			"pointer to a pointer",
			reflect.TypeFor[struct {
				P **int `query:"p"`
			}](),
			`field P has query:"p", but its type **int can't be bound`,
		},
		{
			"pointer to a slice",
			reflect.TypeFor[struct {
				P *[]int `query:"p"`
			}](),
			`field P has query:"p", but its type *[]int can't be bound`,
		},
		{
			"slice in the path",
			reflect.TypeFor[struct {
				S []int `path:"s"`
			}](),
			`field S has path:"s", but its type []int can't be bound: slices can be bound only from the query`,
		},
		{
			"slice of an unsupported type",
			reflect.TypeFor[struct {
				S [][]int `query:"s"`
			}](),
			`field S has query:"s", but its type [][]int can't be bound`,
		},
		{
			"two tags",
			reflect.TypeFor[struct {
				A string `path:"a" query:"a"`
			}](),
			"field A has both path and query tags",
		},
		{
			"empty tag",
			reflect.TypeFor[struct {
				A string `query:""`
			}](),
			"field A has an empty query tag",
		},
		{
			"option in a query tag",
			reflect.TypeFor[struct {
				Tag string `query:"tag,omitempty"`
			}](),
			`field Tag has query:"tag,omitempty", which isn't a query parameter name: it has a comma or a space`,
		},
		{
			"space in a query tag",
			reflect.TypeFor[struct {
				Q string `query:" q"`
			}](),
			`field Q has query:" q", which isn't a query parameter name: it has a comma or a space`,
		},
		{
			"header tag that isn't a header name",
			reflect.TypeFor[struct {
				Token string `header:"X Token"`
			}](),
			`field Token has header:"X Token", which isn't a header name`,
		},
		{
			"repeated name",
			reflect.TypeFor[struct {
				A string `query:"x"`
				B int    `query:"x"`
			}](),
			`fields A and B both have query:"x"`,
		},
		{
			"repeated header in another case",
			reflect.TypeFor[struct {
				A string `header:"X-Id"`
				B string `header:"x-id"`
			}](),
			`fields A and B both have header:"x-id"`,
		},
		{
			"unexported field",
			reflect.TypeFor[struct {
				a string `query:"a"`
			}](),
			`field a has query:"a", but it is unexported`,
		},
		{
			"field of a nested struct",
			reflect.TypeFor[withNested](),
			`field Page.Limit has query:"limit", but only fields of plan_test.withNested and of structs embedded in it can be bound`,
		},
		{
			"field JSON leaves out",
			reflect.TypeFor[withIgnored](),
			`field A has query:"a", but the JSON object of plan_test.withIgnored has no member for it`,
		},
		{
			"field another field hides",
			reflect.TypeFor[withShadowed](),
			`field Pagination.Limit has query:"limit", but the JSON object of plan_test.withShadowed has no member for it`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := plan.NewBinding(tt.typ); err == nil || err.Error() != tt.want {
				t.Errorf("NewBinding() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBind(t *testing.T) {
	b, err := plan.NewBinding(reflect.TypeFor[bound]())
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	bind := func(values map[string][]string) (bound, []string) {
		v := bound{Str: "unchanged", Int: 7}
		problems := b.Bind(reflect.ValueOf(&v).Elem(), func(src plan.Source, name string) ([]string, bool) {
			vs, ok := values[src.Tag()+":"+name]
			return vs, ok
		})
		var details []string
		for _, p := range problems {
			details = append(details, p.Field.GoName+": "+p.Detail)
		}
		return v, details
	}

	t.Run("values", func(t *testing.T) {
		got, problems := bind(map[string][]string{
			"query:str":      {"first", "second"},
			"query:bool":     {"true"},
			"query:int8":     {"-5"},
			"query:uint8":    {"7"},
			"query:float":    {"1.5"},
			"query:time":     {"2026-01-02T03:04:05Z"},
			"query:code":     {"go"},
			"query:ints":     {"1", "2"},
			"path:path":      {"p"},
			"header:X-Head":  {"h"},
			"query:int_ptr":  {"3"},
			"query:time_ptr": {"2026-01-02T03:04:05Z"},
			"query:code_ptr": {"go"},
		})
		want := bound{
			Str:   "first", // a field that isn't a slice gets the first value
			Bool:  true,
			Int8:  -5,
			Int:   7, // missing: unchanged
			Uint8: 7,
			Float: 1.5,
			Time:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Code:  "go",
			Ints:  []int{1, 2},
			Path:  "p",
			Head:  "h",
			// Pointers get a value only when there is one: StrPtr stays nil.
			IntPtr:  new(3),
			TimePtr: new(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			CodePtr: new(code("go")),
		}
		if problems != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Bind() = %+v, %q; want %+v, no problems", got, problems, want)
		}
	})

	t.Run("problems", func(t *testing.T) {
		got, problems := bind(map[string][]string{
			"query:bool":     {"yes"},
			"query:int8":     {"300"},
			"query:int":      {"x"},
			"query:uint8":    {"-1"},
			"query:float":    {"x"},
			"query:time":     {"yesterday"},
			"query:code":     {"x"},
			"query:ints":     {"1", "x"},
			"query:int_ptr":  {"x"},
			"query:code_ptr": {"x"},
		})
		want := []string{
			"Bool: must be true or false",
			"Int8: must be an integer from -128 to 127",
			"Int: must be an integer",
			"Uint8: must be a non-negative integer",
			"Float: must be a number",
			"Time: must be an RFC 3339 time",
			"Code: must be at least 2 characters", // the type's own words
			"Ints: must be an integer",
			"IntPtr: must be an integer",
			"CodePtr: must be at least 2 characters",
		}
		if !slices.Equal(problems, want) {
			t.Errorf("Bind() problems =\n%q\nwant\n%q", problems, want)
		}
		if got.Int != 7 || got.Ints != nil || got.IntPtr != nil || got.CodePtr != nil {
			t.Errorf("Bind() changed fields with bad values: Int = %d, Ints = %v, IntPtr = %v, CodePtr = %v",
				got.Int, got.Ints, got.IntPtr, got.CodePtr)
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		// As in JSON strings, from every source and for every type.
		got, problems := bind(map[string][]string{
			"query:str":     {"\xff"},
			"query:int":     {"\xff"},
			"query:ints":    {"1", "\xfe"},
			"path:path":     {"a\xffb"},
			"header:X-Head": {"\xc3"},
		})
		want := []string{
			"Str: must be valid UTF-8",
			"Int: must be valid UTF-8",
			"Ints: must be valid UTF-8",
			"Path: must be valid UTF-8",
			"Head: must be valid UTF-8",
		}
		if !slices.Equal(problems, want) {
			t.Errorf("Bind() problems =\n%q\nwant\n%q", problems, want)
		}
		if got.Str != "unchanged" || got.Ints != nil || got.Path != "" || got.Head != "" {
			t.Errorf("Bind() changed fields with invalid values: %+v", got)
		}
	})

	t.Run("numbers that JSON doesn't have", func(t *testing.T) {
		for _, s := range []string{
			"NaN", "nan", "Inf", "-Inf", "infinity", "0x10", "0x1p-2", "0b1", "0o7", "1_000",
			"+1", "01", "-01", ".5", "1.", "1.e3", "1e", "1e+", "-", "", " 1", "1 ",
		} {
			_, problems := bind(map[string][]string{"query:int": {s}, "query:uint8": {s}, "query:float": {s}})
			want := []string{"Int: must be an integer", "Uint8: must be a non-negative integer", "Float: must be a number"}
			if !slices.Equal(problems, want) {
				t.Errorf("Bind(%q) problems = %q, want %q", s, problems, want)
			}
		}
		// A number of JSON that isn't an integer is no integer here either,
		// and one out of the range of a float is no float.
		for _, s := range []string{"1.5", "1e3"} {
			want := []string{"Int: must be an integer", "Uint8: must be a non-negative integer"}
			if _, problems := bind(map[string][]string{"query:int": {s}, "query:uint8": {s}}); !slices.Equal(problems, want) {
				t.Errorf("Bind(%q) problems = %q, want %q", s, problems, want)
			}
		}
		if _, problems := bind(map[string][]string{"query:uint8": {"-1"}, "query:float": {"1e400"}}); !slices.Equal(problems, []string{
			"Uint8: must be a non-negative integer", "Float: must be a number",
		}) {
			t.Errorf("Bind(-1, 1e400) problems = %q", problems)
		}
		got, problems := bind(map[string][]string{"query:int": {"-0"}, "query:uint8": {"0"}, "query:float": {"-1.25E+2"}})
		if problems != nil || got.Int != 0 || got.Uint8 != 0 || got.Float != -125 {
			t.Errorf("Bind() = Int %d, Uint8 %d, Float %v, %q; want 0, 0, -125 and no problems", got.Int, got.Uint8, got.Float, problems)
		}
	})

	t.Run("unsigned range", func(t *testing.T) {
		if _, problems := bind(map[string][]string{"query:uint8": {"256"}}); !slices.Equal(problems, []string{"Uint8: must be an integer from 0 to 255"}) {
			t.Errorf("Bind() problems = %q", problems)
		}
	})
}

func TestBindEmbedded(t *testing.T) {
	b, err := plan.NewBinding(reflect.TypeFor[withEmbeddedPointer]())
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	if got := b.Fields[0]; got.GoName != "Pagination.Limit" || got.JSON != "limit" {
		t.Errorf("NewBinding().Fields[0] = %s, JSON %s; want Pagination.Limit, JSON limit", got.GoName, got.JSON)
	}

	bind := func(limit string) (withEmbeddedPointer, int) {
		var v withEmbeddedPointer
		problems := b.Bind(reflect.ValueOf(&v).Elem(), func(src plan.Source, name string) ([]string, bool) {
			return []string{limit}, name == "limit" && limit != ""
		})
		return v, len(problems)
	}
	// The embedded struct is allocated only for a value that fits.
	if v, n := bind("5"); v.Pagination == nil || v.Limit != 5 || n != 0 {
		t.Errorf("Bind(limit=5) = %+v with %d problems, want Limit 5", v.Pagination, n)
	}
	if v, n := bind("x"); v.Pagination != nil || n != 1 {
		t.Errorf("Bind(limit=x) = %+v with %d problems, want nil with 1 problem", v.Pagination, n)
	}
	if v, n := bind(""); v.Pagination != nil || n != 0 {
		t.Errorf("Bind(no limit) = %+v with %d problems, want nil", v.Pagination, n)
	}
}

type selfDecoding struct {
	Code string `json:"code"`
}

func (s *selfDecoding) UnmarshalJSON([]byte) error { return nil }

type recursive struct {
	Next *recursive `json:"next"`
	A    string     `json:"a" query:"a"`
}

type selfDecodingTagged struct {
	Code string `json:"code" query:"code"`
}

func (s *selfDecodingTagged) UnmarshalJSON([]byte) error { return nil }

func TestNewBindingTypes(t *testing.T) {
	if b, err := plan.NewBinding(reflect.TypeFor[recursive]()); err != nil || len(b.Fields) != 1 {
		t.Errorf("NewBinding(recursive type) = %v, %v; want one field", b, err)
	}
	// A type that decodes itself has no JSON object to bind fields of.
	_, err := plan.NewBinding(reflect.TypeFor[selfDecodingTagged]())
	if want := "plan_test.selfDecodingTagged has JSON methods, so its fields aren't members of a JSON object"; err == nil || err.Error() != want {
		t.Errorf("NewBinding(type with JSON methods) error = %v, want %q", err, want)
	}
}

func TestNewBindingWithoutTags(t *testing.T) {
	// A type without binding tags needs no JSON object, even one with JSON
	// methods.
	if b, err := plan.NewBinding(reflect.TypeFor[selfDecoding]()); err != nil || len(b.Fields) != 0 {
		t.Errorf("NewBinding() = %v, %v; want no fields", b, err)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[string](), "must be a string"},
		{reflect.TypeFor[code](), "must be a string"},
		{reflect.TypeFor[bool](), "must be true or false"},
		{reflect.TypeFor[int16](), "must be an integer"},
		{reflect.TypeFor[uint](), "must be a non-negative integer"},
		{reflect.TypeFor[float32](), "must be a number"},
		{reflect.TypeFor[time.Time](), "must be an RFC 3339 time"},
		{reflect.TypeFor[*time.Time](), "must be an RFC 3339 time"},
		{reflect.TypeFor[[]int](), "must be an array"},
		{reflect.TypeFor[[2]int](), "must be an array"},
		{reflect.TypeFor[map[string]int](), "must be an object"},
		{reflect.TypeFor[bound](), "must be an object"},
		{reflect.TypeFor[chan int](), "has an invalid value"},
		{nil, "has an invalid value"},
	}
	for _, tt := range tests {
		if got := plan.Describe(tt.typ); got != tt.want {
			t.Errorf("Describe(%v) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestSource(t *testing.T) {
	tests := []struct {
		src       plan.Source
		tag, text string
	}{
		{plan.Path, "path", "path parameter"},
		{plan.Query, "query", "query parameter"},
		{plan.Header, "header", "header"},
	}
	for _, tt := range tests {
		if tt.src.Tag() != tt.tag || tt.src.String() != tt.text {
			t.Errorf("Source %d: Tag() = %q, String() = %q; want %q, %q", tt.src, tt.src.Tag(), tt.src.String(), tt.tag, tt.text)
		}
	}
}
