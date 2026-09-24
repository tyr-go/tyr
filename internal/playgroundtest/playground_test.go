package playgroundtest

import (
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/tyr-go/tyr/internal/plan"
	"github.com/tyr-go/tyr/internal/plan/plantest"
)

// violation is a field that failed validation and the rule it failed.
type violation struct {
	Pointer string
	Rule    string
}

func TestChecks(t *testing.T) {
	// The core's semantics are those of go-playground with this option.
	v := validator.New(validator.WithRequiredStructEnabled())
	for _, c := range plantest.Checks() {
		t.Run(c.Name, func(t *testing.T) {
			typ := reflect.TypeOf(c.Value)
			var want []violation
			var errs validator.ValidationErrors
			if err := v.Struct(c.Value); errors.As(err, &errs) {
				for _, fe := range errs {
					want = append(want, violation{Pointer: pointerOf(typ, fe.StructNamespace()), Rule: fe.Tag()})
				}
			} else if err != nil {
				t.Fatalf("go-playground: Struct() error = %v", err)
			}

			core, err := plan.NewValidation(typ)
			if err != nil {
				t.Fatalf("NewValidation() error = %v", err)
			}
			var got []violation
			for _, x := range core.Validate(reflect.ValueOf(c.Value)) {
				got = append(got, violation{Pointer: x.Pointer, Rule: x.Detail})
			}
			if !slices.EqualFunc(got, want, func(g, w violation) bool {
				return g.Pointer == w.Pointer && slices.Contains(rulesOf(g.Rule), w.Rule)
			}) {
				t.Errorf("the core reports\n%q\nwhere go-playground reports\n%q", got, want)
			}
		})
	}
}

// pointerOf returns the JSON Pointer that the core reports for the field
// at ns, a struct namespace of go-playground such as Nesting.Profile.Color
// in the struct type t: JSON names, without the structs JSON embeds, and
// the Go name of a field JSON leaves out.
func pointerOf(t reflect.Type, ns string) string {
	var p jsontext.Pointer
	for _, name := range strings.Split(ns, ".")[1:] {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		f, _ := t.FieldByName(name)
		t = f.Type
		tag, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case tag == "-":
			p = p.AppendToken(f.Name)
		case (tag == "" && f.Anonymous || slices.Contains(strings.Split(opts, ","), "embed")) && ft.Kind() == reflect.Struct:
			// JSON puts the members of the struct into its parent.
		case tag != "":
			p = p.AppendToken(tag)
		default:
			p = p.AppendToken(f.Name)
		}
	}
	return string(p)
}

// rulesOf returns the go-playground tags that can fail with the detail
// that the core reports.
func rulesOf(detail string) []string {
	for _, r := range []struct {
		prefix string
		tags   []string
	}{
		{"is required", []string{"required"}},
		{"must be at least ", []string{"min", "gte"}},
		{"must have at least ", []string{"min", "gte"}},
		{"must be at most ", []string{"max", "lte"}},
		{"must have at most ", []string{"max", "lte"}},
		{"must be exactly ", []string{"len"}},
		{"must have exactly ", []string{"len"}},
		{"must be more than ", []string{"gt"}},
		{"must have more than ", []string{"gt"}},
		{"must be greater than ", []string{"gt"}},
		{"must be fewer than ", []string{"lt"}},
		{"must have fewer than ", []string{"lt"}},
		{"must be less than ", []string{"lt"}},
		{"must be one of: ", []string{"oneof"}},
		{"must be an email address", []string{"email"}},
		{"must be an http or https URL", []string{"http_url"}},
		{"must be a URL", []string{"url"}},
		{"must be a UUID", []string{"uuid"}},
	} {
		if strings.HasPrefix(detail, r.prefix) {
			return r.tags
		}
	}
	return []string{"len"} // "must be 5", the len of a number
}
