// Package playground checks the validate tags of requests with all of
// go-playground/validator v10.30.5, in place of the subset of its rules
// that tyr implements, as a [tyr.TagValidator]:
//
//	api := tyr.New(tyr.WithValidator(playground.New()))
//
// The validator is created with WithRequiredStructEnabled, whose semantics
// the subset has, so the rules of the subset behave as they do without the
// module, and so do their violations: tyr gives them its JSON Pointers and
// its details. The other rules come along: dive, which checks the elements
// of slices, arrays and maps, with pointers such as /tags/1, alternatives
// with |, rules across fields such as eqfield and required_if, and formats
// such as e164. The violation of such a rule says "must satisfy" and the
// rule, as in "must satisfy e164", or what a rule of your own says; see
// [Rule]. Violations come in the order of the fields, of the elements and
// of the keys of maps, whose order go-playground doesn't keep.
//
// The documents of the transports get the keywords of the rules of the
// subset only, so that a schema promises nothing tyr can't vouch for: a
// member that e164 checks is a string, with nothing on its format. A member
// is required as a zero value fails the validator in it, so e164 without
// omitempty makes one required.
//
// Plan has go-playground parse the tags of every struct type in a request,
// so an unknown rule panics at registration rather than at a request.
// go-playground reads the parameter of a rule as it checks a value against
// it, though: a bad one behind omitempty panics only for a value that gets
// there, and [tyr.Operation.Call] reports that as an internal error.
package playground

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/tyr-go/tyr"
)

// Option configures the validator that New returns.
type Option func(*config)

// config is what the options of New set.
type config struct {
	rules []rule
}

// rule is a rule of your own.
type rule struct {
	tag    string
	fn     validator.Func
	detail string
}

// Rule adds a rule of your own: fn checks the values of the fields whose
// validate tag has tag, and a violation says detail, or "must satisfy" and
// the tag if detail is empty. [New] panics if fn is nil, if go-playground
// has a rule tag already, as it has every rule of the core, or if it
// refuses the tag, as one with a comma.
func Rule(tag string, fn validator.Func, detail string) Option {
	return func(c *config) { c.rules = append(c.rules, rule{tag: tag, fn: fn, detail: detail}) }
}

// New returns a [tyr.TagValidator] with all the rules of
// go-playground/validator v10.30.5, created with WithRequiredStructEnabled,
// and the rules of opts. It panics on a nil option or a rule that can't be
// added; see [Rule].
func New(opts ...Option) tyr.TagValidator {
	var c config
	for _, opt := range opts {
		if opt == nil {
			panic("playground: New: nil option")
		}
		opt(&c)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	details := make(map[string]string, len(c.rules))
	for _, r := range c.rules {
		switch {
		case r.fn == nil:
			panic(fmt.Sprintf("playground: Rule(%q): nil func", r.tag))
		case has(v, r.tag):
			panic(fmt.Sprintf("playground: Rule(%q): go-playground has the rule; give yours another name", r.tag))
		}
		if err := register(v, r); err != nil {
			panic(fmt.Sprintf("playground: Rule(%q): %v", r.tag, err))
		}
		details[r.tag] = r.detail
	}
	return &tagValidator{v: v, details: details}
}

// register adds r to v, with the panic of go-playground on a tag it
// refuses as an error.
func register(v *validator.Validate, r rule) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	return v.RegisterValidation(r.tag, r.fn)
}

// has reports whether v has the rule tag, by checking a value against it:
// go-playground panics on an undefined rule, and a known one without its
// parameter may panic otherwise.
func has(v *validator.Validate, tag string) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = !strings.Contains(fmt.Sprint(r), "Undefined validation function")
		}
	}()
	_ = v.Var("", tag)
	return true
}

// tagValidator is the TagValidator that New returns.
type tagValidator struct {
	v       *validator.Validate
	details map[string]string // of the rules of your own, by tag
}

// Plan returns the check of the values of t, a struct type, once
// go-playground has parsed the tags of every struct type in t, or a nil
// check if none of them has a validate tag. An unknown rule is an error.
func (tv *tagValidator) Plan(t reflect.Type) (func(req any) []tyr.FailedRule, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%v isn't a struct", t)
	}
	tagged, err := tv.parse(t, make(map[reflect.Type]bool))
	if err != nil || !tagged {
		return nil, err
	}
	return tv.check, nil
}

// parse has go-playground parse the tags of the struct type t and of the
// struct types in its fields, and reports whether any of them has a
// validate tag.
func (tv *tagValidator) parse(t reflect.Type, seen map[reflect.Type]bool) (tagged bool, err error) {
	if seen[t] || t == reflect.TypeFor[time.Time]() {
		return false, nil
	}
	seen[t] = true
	if err := tv.try(t); err != nil {
		return false, err
	}
	for sf := range t.Fields() {
		if tag := sf.Tag.Get("validate"); tag != "" && tag != "-" {
			tagged = true
		}
		for _, st := range structsIn(sf.Type) {
			inner, err := tv.parse(st, seen)
			if err != nil {
				return false, err
			}
			tagged = tagged || inner
		}
	}
	return tagged, nil
}

// try checks a zero value of the struct type t, for go-playground to parse
// its tags, and returns a panic, such as of an unknown rule, as an error.
func (tv *tagValidator) try(t reflect.Type) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v: %v", t, r)
		}
	}()
	_ = tv.v.Struct(reflect.New(t).Interface())
	return nil
}

// structsIn returns the struct types that values of type t hold: t itself
// past pointers, and those of the elements and keys of slices, arrays and
// maps.
func structsIn(t reflect.Type) []reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return []reflect.Type{t}
	case reflect.Slice, reflect.Array:
		return structsIn(t.Elem())
	case reflect.Map:
		return append(structsIn(t.Key()), structsIn(t.Elem())...)
	}
	return nil
}

// failure is a failed rule with its place among the values of a request.
type failure struct {
	rule  tyr.FailedRule
	order []step
}

// step is where a step of a path goes: to a field by its index, to an
// element by its index, or to an element of a map by its key.
type step struct {
	index int
	key   string
}

// check checks req, a pointer to a struct, and returns the rules that its
// values fail, in the order of the fields, of the elements and of the keys.
func (tv *tagValidator) check(req any) []tyr.FailedRule {
	var errs validator.ValidationErrors
	if err := tv.v.Struct(req); !errors.As(err, &errs) {
		return nil // no error, or one a pointer to a struct can't get
	}
	root := reflect.ValueOf(req)
	name := root.Elem().Type().Name() // go-playground starts a namespace with it
	failures := make([]failure, 0, len(errs))
	for _, fe := range errs {
		ns := strings.TrimPrefix(fe.StructNamespace(), name+".")
		path, order, ok := parse(root, ns)
		if !ok {
			path = []string{ns} // a path to nothing, which tyr reports as a bug
		}
		failures = append(failures, failure{
			rule:  tyr.FailedRule{Path: path, Rule: fe.Tag(), Param: fe.Param(), Detail: tv.details[fe.Tag()]},
			order: order,
		})
	}
	slices.SortStableFunc(failures, func(a, b failure) int { return compare(a.order, b.order) })
	out := make([]tyr.FailedRule, len(failures))
	for i, f := range failures {
		out[i] = f.rule
	}
	return out
}

// compare orders two places of values: by their steps, the first different
// one deciding, and a value before those within it.
func compare(a, b []step) int {
	for i := range min(len(a), len(b)) {
		if c := a[i].index - b[i].index; c != 0 {
			return c
		}
		if c := strings.Compare(a[i].key, b[i].key); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

// parse returns the path of tyr to the value at ns in v, a namespace of
// go-playground past the name of the struct, such as Items[1].Name, and its
// place among the values of v. A key of a map may have any characters, so
// it's found among the keys of the map, and ok is false if nothing fits.
func parse(v reflect.Value, ns string) (path []string, order []step, ok bool) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil, ns == ""
		}
		v = v.Elem()
	}
	if ns == "" {
		return nil, nil, true
	}
	switch v.Kind() {
	case reflect.Struct:
		end := strings.IndexAny(ns, ".[")
		if end < 0 {
			end = len(ns)
		}
		sf, found := v.Type().FieldByName(ns[:end])
		if !found || len(sf.Index) != 1 {
			return nil, nil, false
		}
		rest := strings.TrimPrefix(ns[end:], ".")
		p, o, ok := parse(v.Field(sf.Index[0]), rest)
		return append([]string{sf.Name}, p...), append([]step{{index: sf.Index[0]}}, o...), ok
	case reflect.Slice, reflect.Array:
		inside, rest, found := strings.Cut(strings.TrimPrefix(ns, "["), "]")
		i, err := strconv.Atoi(inside)
		if !found || !strings.HasPrefix(ns, "[") || err != nil || i < 0 || i >= v.Len() {
			return nil, nil, false
		}
		p, o, ok := parse(v.Index(i), strings.TrimPrefix(rest, "."))
		return append([]string{inside}, p...), append([]step{{index: i}}, o...), ok
	case reflect.Map:
		if !strings.HasPrefix(ns, "[") {
			return nil, nil, false
		}
		// The longest key that fits first: of keys a and a]b, only the
		// latter fits a]b].
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return len(fmt.Sprint(b)) - len(fmt.Sprint(a)) })
		for _, k := range keys {
			key := fmt.Sprintf("%v", k)
			rest, fits := strings.CutPrefix(ns[1:], key+"]")
			if !fits || rest != "" && rest[0] != '.' && rest[0] != '[' {
				continue
			}
			if p, o, ok := parse(v.MapIndex(k), strings.TrimPrefix(rest, ".")); ok {
				return append([]string{key}, p...), append([]step{{key: key}}, o...), true
			}
		}
	}
	return nil, nil, false
}
