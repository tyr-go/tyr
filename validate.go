package tyr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/tyr-go/tyr/internal/plan"
)

// Validator is implemented by requests that check themselves, for rules
// that struct tags can't express: cross-field checks, regular expressions,
// lookups in memory. Validate gets no context, so it must not do I/O:
// checks that need it belong in the handler.
//
// [Operation.Call] calls Validate after the interceptors, right before the
// handler, if *Req implements Validator; Validate may have a value or a
// pointer receiver, and it isn't called for nested structs. Call calls it
// only for a request that passed the checks of its validate tags, so
// Validate may rely on them, e.g. on a required field being set.
//
// A Validate of a struct that Req embeds is Req's own, as Go promotes it.
// Of two structs embedded at the same depth that both have one, Go promotes
// neither, so [API.Handle] panics rather than skip their checks, unless Req
// has a Validate of its own, which may call theirs.
//
// An error of Validate that contains an [Error], such as one from
// [Violations.Err], is used as is.
// Any other error becomes [KindInvalidArgument] with the error's text as
// its message, since Validate writes its errors for the client, and the
// error stays its cause. The mappers of [API.MapError] don't see errors of
// Validate.
type Validator interface {
	Validate() error
}

// requestCheck returns the check of requests of the struct type t: their
// validate tags, with the validator of the API, and then their values of
// enums (see Enum), or nil if they have nothing to check. The check gets a
// pointer to a request; an error means that the validator reported a rule
// of a value that t doesn't have.
func (a *API) requestCheck(t reflect.Type) (func(req any) (Violations, error), error) {
	tags, err := a.tagCheck(t)
	if err != nil {
		return nil, err
	}
	enums, err := plan.NewEnumCheck(t, plan.Input)
	if err != nil {
		return nil, fmt.Errorf("%v: %w", t, err)
	}
	if enums == nil {
		return tags, nil
	}
	return func(req any) (Violations, error) {
		var vs Violations
		if tags != nil {
			var err error
			if vs, err = tags(req); err != nil {
				return nil, err
			}
		}
		for _, f := range enums.Failures(reflect.ValueOf(req).Elem(), false) {
			// A field that fails a tag adds no second violation.
			if !slices.ContainsFunc(vs, func(v Violation) bool { return v.Pointer == f.Pointer }) {
				vs = append(vs, Violation{Pointer: f.Pointer, Detail: f.Detail})
			}
		}
		return vs, nil
	}, nil
}

// violationsOf returns the violations of validate tags as Violations.
func violationsOf(vs []plan.Violation) Violations {
	v := make(Violations, len(vs))
	for i, x := range vs {
		v[i] = Violation{Pointer: x.Pointer, Detail: x.Detail}
	}
	return v
}

// conflictingValidate describes how the Validate methods of structs that
// the struct type t embeds conflict, so that Go promotes none of them to t
// and Call would skip their checks. It returns "" if they don't conflict or
// t has a Validate method of its own.
func conflictingValidate(t reflect.Type) string {
	path, names := validateConflict(t, map[reflect.Type]bool{})
	if len(names) == 0 {
		return ""
	}
	var embeds strings.Builder
	for _, name := range path {
		embeds.WriteString(name)
		embeds.WriteString(", which embeds ")
	}
	embeds.WriteString(strings.Join(names[:len(names)-1], ", "))
	embeds.WriteString(" and ")
	embeds.WriteString(names[len(names)-1])
	return fmt.Sprintf("request type %v embeds %s, whose Validate methods conflict, so none of them is called; "+
		"give %v a Validate method of its own that calls theirs", t, &embeds, t)
}

// validateConflict returns the names of the structs embedded in the struct
// type t, or in the structs embedded in it on path, whose Validate methods
// conflict. names is empty if t has a Validate method or no two of the
// structs that it embeds at one depth have one.
func validateConflict(t reflect.Type, seen map[reflect.Type]bool) (path, names []string) {
	if _, ok := reflect.PointerTo(t).MethodByName("Validate"); ok || seen[t] {
		return nil, nil
	}
	seen[t] = true
	var validator bool              // whether one of names implements Validator
	var inner []reflect.StructField // embedded structs without a Validate method
	for f := range t.Fields() {
		if !f.Anonymous {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		methods := ft // an interface has the methods of its own type
		if ft.Kind() != reflect.Interface {
			methods = reflect.PointerTo(ft)
		}
		if _, ok := methods.MethodByName("Validate"); ok {
			names = append(names, f.Name)
			validator = validator || methods.Implements(reflect.TypeFor[Validator]())
		} else if ft.Kind() == reflect.Struct {
			f.Type = ft
			inner = append(inner, f)
		}
	}
	if len(names) > 1 {
		if !validator {
			return nil, nil // no Validate that Call would call is lost
		}
		return nil, names
	}
	for _, f := range inner {
		if p, n := validateConflict(f.Type, seen); len(n) > 0 {
			return append([]string{f.Name}, p...), n
		}
	}
	return nil, nil
}

// validationError turns an error of Validate into an Error, as described
// at Validator. ctx is the context of the handler.
func (a *API) validationError(ctx context.Context, err error) *Error {
	if _, ok := errors.AsType[*Error](err); ok {
		return a.resolve(ctx, err) // as is; a nil *Error becomes internal
	}
	return &Error{Kind: KindInvalidArgument, Message: err.Error(), cause: err}
}
