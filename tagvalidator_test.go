package tyr_test

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
)

// fakeValidator is a TagValidator that reports what failed says.
type fakeValidator struct {
	err     error            // of Plan
	nilFn   bool             // Plan returns a nil check
	failed  []tyr.FailedRule // what the check reports
	got     []any            // the requests the check got
	planned []reflect.Type   // the types Plan got
}

func (v *fakeValidator) Plan(t reflect.Type) (func(req any) []tyr.FailedRule, error) {
	v.planned = append(v.planned, t)
	if v.err != nil || v.nilFn {
		return nil, v.err
	}
	return func(req any) []tyr.FailedRule {
		v.got = append(v.got, req)
		return v.failed
	}, nil
}

// signupReq has values of every kind that a path leads to.
type signupReq struct {
	pageOf
	Code    string   `json:"code"`
	Phone   string   `json:"phone"`
	Pass    string   `json:"pass"`
	Again   string   `json:"again"`
	Profile *profile `json:"profile"`
	Tags    []string `json:"tags"`
	Labels  map[string]string
	Secret  string `json:"-"`
}

type pageOf struct {
	Limit int `json:"limit"`
}

type profile struct {
	Color string `json:"color"`
}

func TestWithValidator(t *testing.T) {
	v := &fakeValidator{failed: []tyr.FailedRule{
		{Path: []string{"pageOf", "Limit"}, Rule: "gte", Param: "1"},
		{Path: []string{"Code"}, Rule: "min", Param: "4"},
		{Path: []string{"Phone"}, Rule: "e164"},
		{Path: []string{"Again"}, Rule: "eqfield", Param: "Pass"},
		{Path: []string{"Pass"}, Rule: "strong", Detail: "must have a digit"},
		{Path: []string{"Profile", "Color"}, Rule: "oneof", Param: "red green"},
		{Path: []string{"Tags", "1"}, Rule: "min", Param: "2"},
		{Path: []string{"Labels", "a/b"}, Rule: "required"},
		{Path: []string{"Secret"}, Rule: "required"},
	}}
	api := tyr.New(tyr.WithValidator(v))
	called := false
	op := api.Handle("users.signup", func(ctx context.Context, req signupReq) (struct{}, error) {
		called = true
		return struct{}{}, nil
	})

	_, err := op.Call(t.Context(), nil)
	e, ok := err.(*tyr.Error)
	if !ok || e.Kind != tyr.KindInvalidArgument || called {
		t.Fatalf("Call() error = %v, handler called %v; want invalid_argument and no call", err, called)
	}
	// The pointers and the details of the core, the detail of the
	// validator for a rule outside the subset, or the generic one.
	want := tyr.Violations{
		{Pointer: "/limit", Detail: "must be at least 1"},
		{Pointer: "/code", Detail: "must be at least 4 characters"},
		{Pointer: "/phone", Detail: "must satisfy e164"},
		{Pointer: "/again", Detail: "must satisfy eqfield=Pass"},
		{Pointer: "/pass", Detail: "must have a digit"},
		{Pointer: "/profile/color", Detail: "must be one of: red, green"},
		{Pointer: "/tags/1", Detail: "must be at least 2 characters"},
		{Pointer: "/Labels/a~1b", Detail: "is required"},
		{Pointer: "/Secret", Detail: "is required"},
	}
	if got, _ := e.Details.(tyr.Violations); !slices.Equal(got, want) {
		t.Errorf("violations =\n%v\nwant\n%v", got, want)
	}
	// The check gets a pointer to the request.
	if len(v.got) != 1 || reflect.TypeOf(v.got[0]) != reflect.TypeFor[*signupReq]() {
		t.Errorf("the check got %v, want one *signupReq", v.got)
	}

	// No failed rules: the handler runs.
	v.failed = nil
	if _, err := op.Call(t.Context(), nil); err != nil || !called {
		t.Errorf("Call() error = %v, handler called %v; want <nil> and a call", err, called)
	}
}

func TestWithValidatorBadPath(t *testing.T) {
	// A path to no value of the request is a bug of the validator: an
	// internal error, with the path in the log, rather than a violation.
	rec := &recorder{}
	v := &fakeValidator{failed: []tyr.FailedRule{{Path: []string{"Nope"}, Rule: "required"}}}
	op := tyr.New(tyr.WithValidator(v), tyr.WithLogger(slog.New(rec))).Handle("users.signup",
		func(ctx context.Context, req signupReq) (struct{}, error) { return struct{}{}, nil })

	_, err := op.Call(t.Context(), nil)
	if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindInternal {
		t.Fatalf("Call() error = %v, want internal", err)
	}
	if len(rec.logs) != 1 || !strings.Contains(rec.logs[0].attrs["err"], `the validator reported the rule "required" at "Nope"`) {
		t.Errorf("logged %+v, want the path", rec.logs)
	}
}

func TestWithValidatorPlan(t *testing.T) {
	// An error of Plan makes the registration panic, with it.
	api := tyr.New(tyr.WithValidator(&fakeValidator{err: errors.New("unknown rule \"nope\"")}))
	got := panicValue(func() {
		api.Handle("users.signup", func(ctx context.Context, req signupReq) (struct{}, error) { return struct{}{}, nil })
	})
	if got != `tyr: Handle("users.signup"): unknown rule "nope"` {
		t.Errorf("Handle() panicked with %v", got)
	}

	// A nil check: nothing to check.
	v := &fakeValidator{nilFn: true}
	op := tyr.New(tyr.WithValidator(v)).Handle("users.signup", func(ctx context.Context, req signupReq) (struct{}, error) { return struct{}{}, nil })
	if _, err := op.Call(t.Context(), nil); err != nil || !slices.Equal(v.planned, []reflect.Type{reflect.TypeFor[signupReq]()}) {
		t.Errorf("Call() error = %v, Plan got %v; want <nil> and signupReq", err, v.planned)
	}

	if got := panicValue(func() { tyr.WithValidator(nil) }); got != "tyr: WithValidator: nil validator" {
		t.Errorf("WithValidator(nil) panicked with %v", got)
	}
}

func TestWithValidatorExamples(t *testing.T) {
	// The validator checks the examples of a contract too.
	v := &fakeValidator{failed: []tyr.FailedRule{{Path: []string{"Phone"}, Rule: "e164"}}}
	c := tyr.Define[signupReq, struct{}]("users.signup").Example("an example", signupReq{Phone: "555"}, struct{}{})
	got := panicValue(func() {
		tyr.New(tyr.WithValidator(v)).Implement(c, func(ctx context.Context, req signupReq) (struct{}, error) { return struct{}{}, nil })
	})
	if s, _ := got.(string); s != `tyr: Implement("users.signup"): example "an example": the request fails validation: /phone: must satisfy e164` {
		t.Errorf("Implement() panicked with %v", got)
	}
}

func TestValidator(t *testing.T) {
	// The validator that WithValidator sets, or the subset of the core,
	// which reports the rules that fail as another validator would.
	v := &fakeValidator{}
	if got := tyr.New(tyr.WithValidator(v)).Validator(); got != v {
		t.Errorf("Validator() = %v, want the one set", got)
	}

	type req struct {
		Code  string `json:"code" validate:"required,min=4"`
		Inner struct {
			Size int `json:"size" validate:"max=10"`
		} `json:"inner"`
	}
	check, err := tyr.New().Validator().Plan(reflect.TypeFor[req]())
	if err != nil {
		t.Fatal(err)
	}
	r := req{Code: "go"}
	r.Inner.Size = 11
	want := []tyr.FailedRule{
		{Path: []string{"Code"}, Rule: "min", Param: "4"},
		{Path: []string{"Inner", "Size"}, Rule: "max", Param: "10"},
	}
	if got := check(&r); !reflect.DeepEqual(got, want) {
		t.Errorf("the check of the core = %+v, want %+v", got, want)
	}
	if check, err := tyr.New().Validator().Plan(reflect.TypeFor[struct{ A int }]()); check != nil || err != nil {
		t.Errorf("Plan() of a struct without tags = %v, %v; want nil, nil", check != nil, err)
	}
	if _, err := tyr.New().Validator().Plan(reflect.TypeFor[int]()); err == nil {
		t.Error("Plan() of an int error = <nil>")
	}
}
