package tyr

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/tyr-go/tyr/internal/plan"
)

// TagValidator checks requests by their validate tags in place of the
// subset of go-playground/validator that the core implements, with rules
// the subset lacks or rules of your own; [WithValidator] sets it for an
// API, and github.com/tyr-go/tyr/validate/playground implements it with
// all of go-playground/validator.
//
// The core turns the rules it reports into [Violations]: a JSON Pointer
// made of the path, and the detail that the core has for a rule of its
// subset, so that clients get the same violations of the rules that the
// validators share. The schemas of the documents of transports get the
// keywords of the rules of the subset only, since the core can't vouch for
// what others demand, and a member is required if a zero value fails the
// validator in it, as without one.
type TagValidator interface {
	// Plan returns the check of the values of t, a struct type, when an
	// operation with requests of t is registered, or when a transport
	// describes t in a request. check gets a pointer to a value of t and
	// returns the rules that its values fail, at most one per value, in
	// the order of the fields; a nil check means t has nothing to check.
	// An error, such as of an unknown rule, makes the registration panic.
	Plan(t reflect.Type) (check func(req any) []FailedRule, err error)
}

// FailedRule is a rule of a validate tag that a value in a request fails,
// as a [TagValidator] reports it.
type FailedRule struct {
	// Path leads from the request to the value: the Go names of fields,
	// the embedded ones among them, and the indexes of the elements of
	// slices and arrays and the keys of maps, such as Items, 1 and Name.
	Path []string

	// Rule is the rule as the tag has it, such as "min" or "e164", and
	// Param its parameter, such as "4", empty without one.
	Rule, Param string

	// Detail is what the violation of a rule outside the subset of the core
	// says; if it's empty, the violation says "must satisfy" and the rule.
	// The rules of the subset have details of the core.
	Detail string
}

// WithValidator makes the API check the validate tags of requests with v
// rather than with the subset of go-playground/validator that the core
// implements; see [TagValidator]. It panics if v is nil.
func WithValidator(v TagValidator) Option {
	if v == nil {
		panic("tyr: WithValidator: nil validator")
	}
	return func(a *API) { a.validator = v }
}

// Validator returns the [TagValidator] of the API: the one [WithValidator]
// set, or the subset of the core. Transports ask it which members of the
// requests they describe are required.
func (a *API) Validator() TagValidator {
	if a.validator != nil {
		return a.validator
	}
	return coreValidator{}
}

// coreValidator is the subset of go-playground/validator that the core
// implements, as a TagValidator.
type coreValidator struct{}

func (coreValidator) Plan(t reflect.Type) (func(req any) []FailedRule, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%v isn't a struct", t)
	}
	v, err := plan.NewValidation(t)
	if err != nil || v == nil {
		return nil, err
	}
	return func(req any) []FailedRule {
		fs := v.Failures(reflect.ValueOf(req).Elem())
		out := make([]FailedRule, len(fs))
		for i, f := range fs {
			out[i] = FailedRule{Path: f.Path, Rule: f.Rule, Param: f.Param}
		}
		return out
	}, nil
}

// tagCheck returns the check of the validate tags of requests of the
// struct type t, with the validator of the API, or nil if they have none to
// check. The check gets a pointer to a request; an error means that the
// validator reported a rule of a value that t doesn't have.
func (a *API) tagCheck(t reflect.Type) (func(req any) (Violations, error), error) {
	if a.validator == nil {
		v, err := plan.NewValidation(t)
		if err != nil || v == nil {
			return nil, err
		}
		return func(req any) (Violations, error) {
			if vs := v.Validate(reflect.ValueOf(req).Elem()); len(vs) > 0 {
				return violationsOf(vs), nil
			}
			return nil, nil
		}, nil
	}
	check, err := a.validator.Plan(t)
	if err != nil || check == nil {
		return nil, err
	}
	return func(req any) (Violations, error) {
		return violationsFrom(t, check(req))
	}, nil
}

// violationsFrom returns the violations of the rules that a request of the
// struct type t fails, as a TagValidator reports them.
func violationsFrom(t reflect.Type, failed []FailedRule) (Violations, error) {
	if len(failed) == 0 {
		return nil, nil
	}
	vs := make(Violations, 0, len(failed))
	for _, f := range failed {
		p, vt, err := plan.Pointer(t, f.Path)
		if err != nil {
			return nil, fmt.Errorf("tyr: the validator reported the rule %q at %q: %w", f.Rule, strings.Join(f.Path, "."), err)
		}
		vs = append(vs, Violation{Pointer: p, Detail: detailOf(f, vt)})
	}
	return vs, nil
}

// detailOf returns what the violation of f says of a value of type t: the
// detail of the core for a rule of its subset, or else f.Detail, or else
// that the value must satisfy the rule.
func detailOf(f FailedRule, t reflect.Type) string {
	if d, ok := plan.Detail(f.Rule, f.Param, t); ok {
		return d
	}
	if f.Detail != "" {
		return f.Detail
	}
	if f.Param != "" {
		return "must satisfy " + f.Rule + "=" + f.Param
	}
	return "must satisfy " + f.Rule
}
