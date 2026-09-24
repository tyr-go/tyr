package playground_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
	"github.com/tyr-go/tyr/validate/playground"
)

var codeRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// validCode is a rule of our own.
func validCode(fl validator.FieldLevel) bool {
	return codeRe.MatchString(fl.Field().String())
}

type item struct {
	Name string `json:"name" validate:"required"`
}

type page struct {
	Limit int `json:"limit" validate:"gte=1"`
}

// signupReq has rules of the subset of the core and beyond.
type signupReq struct {
	page
	Code    string            `json:"code" validate:"required,min=4,code"`
	Phone   string            `json:"phone" validate:"omitempty,e164"`
	Pass    string            `json:"pass"`
	Again   string            `json:"again" validate:"eqfield=Pass"`
	Color   string            `json:"color" validate:"omitempty,oneof=red green"`
	Tags    []string          `json:"tags" validate:"dive,min=2"`
	Items   []item            `json:"items" validate:"dive"`
	Labels  map[string]string `json:"labels" validate:"dive,keys,min=2,endkeys,required"`
	Contact string            `json:"contact" validate:"omitempty,email|e164"`
}

// newAPI returns an API that checks tags with all of go-playground and the
// rule code, and doesn't log.
func newAPI() *tyr.API {
	return tyr.New(
		tyr.WithLogger(slog.New(slog.DiscardHandler)),
		tyr.WithValidator(playground.New(playground.Rule("code", validCode, "only a-z, 0-9 and '-'"))),
	)
}

// call calls op with req and returns the violations of the error, or fails
// t if it isn't invalid_argument.
func call[Req any](t *testing.T, op *tyr.Operation, req Req) tyr.Violations {
	t.Helper()
	_, err := op.Call(t.Context(), func(dst any) error {
		*dst.(*Req) = req
		return nil
	})
	if err == nil {
		return nil
	}
	e, ok := errors.AsType[*tyr.Error](err)
	if !ok || e.Kind != tyr.KindInvalidArgument {
		t.Fatalf("Call() error = %v, want invalid_argument", err)
	}
	vs, _ := e.Details.(tyr.Violations)
	return vs
}

func TestViolations(t *testing.T) {
	op := newAPI().Handle("users.signup", func(ctx context.Context, req signupReq) (struct{}, error) { return struct{}{}, nil })
	req := signupReq{
		Code:    "Go_",
		Phone:   "555",
		Pass:    "p",
		Again:   "q",
		Color:   "blue",
		Tags:    []string{"ok", "x"},
		Items:   []item{{Name: "a"}, {}},
		Labels:  map[string]string{"en": "", "x": "y", "de": "z"},
		Contact: "nobody",
	}
	want := tyr.Violations{
		{Pointer: "/limit", Detail: "must be at least 1"},
		{Pointer: "/code", Detail: "must be at least 4 characters"},
		{Pointer: "/phone", Detail: "must satisfy e164"},
		{Pointer: "/again", Detail: "must satisfy eqfield=Pass"},
		{Pointer: "/color", Detail: "must be one of: red, green"},
		{Pointer: "/tags/1", Detail: "must be at least 2 characters"},
		{Pointer: "/items/1/name", Detail: "is required"},
		{Pointer: "/labels/en", Detail: "is required"},
		{Pointer: "/labels/x", Detail: "must be at least 2 characters"},
		{Pointer: "/contact", Detail: "must satisfy email|e164"},
	}
	// Twice as many runs as it takes the random order of a map to show.
	for range 20 {
		if got := call(t, op, req); !slices.Equal(got, want) {
			t.Fatalf("violations =\n%v\nwant\n%v", got, want)
		}
	}

	// A rule of our own, after those of the core passed.
	req = signupReq{page: page{Limit: 1}, Code: "Go_Dev", Pass: "p", Again: "p"}
	if got, want := call(t, op, req), (tyr.Violations{{Pointer: "/code", Detail: "only a-z, 0-9 and '-'"}}); !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
	req.Code = "go-dev"
	if got := call(t, op, req); got != nil {
		t.Errorf("a valid request: violations = %v", got)
	}
}

func TestKeysOfMaps(t *testing.T) {
	// A key may have the characters that go-playground puts around keys.
	type req struct {
		Labels map[string]string `json:"labels" validate:"dive,required"`
		Groups map[string][]item `json:"groups" validate:"dive,dive"`
	}
	op := newAPI().Handle("labels.set", func(ctx context.Context, req req) (struct{}, error) { return struct{}{}, nil })
	got := call(t, op, req{
		Labels: map[string]string{"a": "x", "a].b": "", "c[0]": ""},
		Groups: map[string][]item{"g.1": {{}, {Name: "n"}}},
	})
	want := tyr.Violations{
		{Pointer: "/labels/a].b", Detail: "is required"},
		{Pointer: "/labels/c[0]", Detail: "is required"},
		{Pointer: "/groups/g.1/0/name", Detail: "is required"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations =\n%v\nwant\n%v", got, want)
	}
}

// Page is a generic request, whose name has dots and brackets.
type Page[T any] struct {
	Items []T `json:"items" validate:"required,dive"`
}

func TestGenericRequest(t *testing.T) {
	op := newAPI().Handle("items.put", func(ctx context.Context, req Page[item]) (struct{}, error) { return struct{}{}, nil })
	got := call(t, op, Page[item]{Items: []item{{Name: "a"}, {}}})
	if want := (tyr.Violations{{Pointer: "/items/1/name", Detail: "is required"}}); !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

func TestUnknownRules(t *testing.T) {
	// go-playground panics at the first value that meets an unknown rule;
	// Plan finds them in every struct type of a request, at registration.
	type deep struct {
		X string `validate:"nosuchrule"`
	}
	type viaPointer struct {
		Deep *deep `json:"deep"`
	}
	type viaSlice struct {
		Deep []deep `json:"deep" validate:"dive"`
	}
	type viaMap struct {
		Deep map[string]*deep `json:"deep"`
	}
	tests := []struct {
		name string
		f    func()
	}{
		{"top", func() {
			newAPI().Handle("a.b", func(ctx context.Context, req deep) (struct{}, error) { return struct{}{}, nil })
		}},
		{"behind a pointer", func() {
			newAPI().Handle("a.b", func(ctx context.Context, req viaPointer) (struct{}, error) { return struct{}{}, nil })
		}},
		{"in a slice", func() {
			newAPI().Handle("a.b", func(ctx context.Context, req viaSlice) (struct{}, error) { return struct{}{}, nil })
		}},
		{"in a map", func() {
			newAPI().Handle("a.b", func(ctx context.Context, req viaMap) (struct{}, error) { return struct{}{}, nil })
		}},
	}
	for _, tt := range tests {
		got := panicValue(tt.f)
		if s, _ := got.(string); !strings.HasPrefix(s, `tyr: Handle("a.b"): `) || !strings.Contains(s, "Undefined validation function 'nosuchrule'") {
			t.Errorf("%s: Handle() panicked with %v", tt.name, got)
		}
	}
}

func TestNothingToCheck(t *testing.T) {
	type req struct {
		A string `json:"a"`
		B struct {
			C int `json:"c"`
		} `json:"b"`
	}
	check, err := playground.New().Plan(reflectType[req]())
	if check != nil || err != nil {
		t.Errorf("Plan() of a request without tags = %v, %v; want nil, nil", check != nil, err)
	}
	if _, err := playground.New().Plan(reflectType[int]()); err == nil {
		t.Error("Plan() of an int error = <nil>")
	}
}

func TestRulePanics(t *testing.T) {
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"a rule of go-playground", func() { playground.New(playground.Rule("min", validCode, "")) }, `playground: Rule("min"): go-playground has the rule; give yours another name`},
		{"a rule of go-playground only", func() { playground.New(playground.Rule("e164", validCode, "")) }, `playground: Rule("e164"): go-playground has the rule; give yours another name`},
		{"a tag go-playground refuses", func() { playground.New(playground.Rule("a,b", validCode, "")) }, `playground: Rule("a,b"): `},
		{"nil func", func() { playground.New(playground.Rule("code", nil, "")) }, `playground: Rule("code"): nil func`},
		{"nil option", func() { playground.New(nil) }, `playground: New: nil option`},
	}
	for _, tt := range tests {
		if got, _ := panicValue(tt.f).(string); !strings.HasPrefix(got, tt.want) {
			t.Errorf("%s: panicked with %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestBadParameter(t *testing.T) {
	// go-playground reads a parameter as it checks a value: past omitempty,
	// a bad one panics only then, which Call reports as internal.
	type req struct {
		N int `json:"n" validate:"omitempty,min=x"`
	}
	op := newAPI().Handle("n.set", func(ctx context.Context, req req) (struct{}, error) { return struct{}{}, nil })
	if _, err := op.Call(t.Context(), nil); err != nil {
		t.Errorf("Call() of a zero request error = %v, want <nil>", err)
	}
	_, err := op.Call(t.Context(), func(dst any) error { dst.(*req).N = 5; return nil })
	if e, ok := errors.AsType[*tyr.Error](err); !ok || e.Kind != tyr.KindInternal {
		t.Errorf("Call() error = %v, want internal", err)
	}
}

func TestDocuments(t *testing.T) {
	// The documents get the keywords of the rules of the core only, and a
	// member is required as a zero value fails go-playground in it.
	type req struct {
		Phone string   `json:"phone" validate:"e164"`
		Code  string   `json:"code" validate:"omitempty,min=4,code"`
		Tags  []string `json:"tags" validate:"min=1,dive,min=2"`
	}
	api := newAPI()
	api.Handle("calls.start", func(ctx context.Context, req req) (struct{}, error) { return struct{}{}, nil }, rest.Route("POST /calls"))
	rec := httptest.NewRecorder()
	rest.Mount(http.NewServeMux(), api).OpenAPI(tyr.Info{Title: "calls", Version: "1.0.0"}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema jsontext.Value `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	got, _ := jsontext.AppendFormat(nil, doc.Paths["/calls"]["post"].RequestBody.Content["application/json"].Schema)
	const want = `{"type":"object","properties":{"phone":{"type":"string"},"code":{"type":"string","minLength":4},` +
		`"tags":{"type":"array","items":{"type":"string"},"minItems":1}},"required":["phone","tags"]}`
	if string(got) != want {
		t.Errorf("the body =\n%s\nwant\n%s", got, want)
	}
}

// panicValue returns what f panics with, nil if it doesn't.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}
