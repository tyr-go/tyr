package plan

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tyr-go/tyr/internal/jsonschema"
)

// Violation is a field that failed validation.
type Violation struct {
	Pointer string // JSON Pointer of the field
	Detail  string // what's wrong with it, for clients
}

// Validation checks values of a struct type against the validate tags of
// its fields, with the semantics of the same tags in go-playground/validator
// created WithRequiredStructEnabled: a field fails on its first failing
// rule, with one violation, and all fields are checked, in their order,
// through nested and embedded structs.
type Validation struct {
	root *validatedStruct
}

// validatedStruct is the plan of one struct type.
type validatedStruct struct {
	fields []validatedField
}

// validatedField is the plan of one field of a struct.
type validatedField struct {
	index   int
	name    string // in Go
	segment string // of the JSON Pointer; "" for a struct embedded in JSON
	rules   []rule
	nested  *validatedStruct // for a field of a struct type or a pointer to one
}

// rule is one rule of a validate tag.
type rule struct {
	name   string
	param  string
	detail string                                       // of a violation
	check  func(v reflect.Value, fromPointer bool) bool // v has no pointers left
	// keywords add to s, the JSON Schema of the values of the field, what
	// the rule demands of them, as far as JSON Schema can say it; f is how
	// they look in JSON. Every rule has them, next to its check.
	keywords func(s *jsonschema.Schema, f form)
}

// hint ends the message about a rule the core doesn't know.
const hint = "check the tags with tyr.WithValidator(playground.New()) of the module validate/playground for more rules, or move the check to Validate()"

// NewValidation returns the validation of the struct type t, or nil if t
// has nothing to validate. It fails on an unknown rule, a rule that
// doesn't apply to its field's type, and a bad parameter.
func NewValidation(t reflect.Type) (*Validation, error) {
	b := &validationBuilder{plans: make(map[reflect.Type]*validatedStruct)}
	root, err := b.build(t, "")
	if err != nil || root == nil {
		return nil, err
	}
	return &Validation{root: root}, nil
}

// Validate returns the violations of v, a value of the validation's type,
// in the order of the fields.
func (p *Validation) Validate(v reflect.Value) []Violation {
	var out []Violation
	p.root.validate(v, "", &out)
	return out
}

// Failure is a rule that a value fails, as Failures reports it.
type Failure struct {
	Path  []string // the Go names of the fields from the validated struct down to the value
	Rule  string
	Param string
}

// Failures returns the rules that v, a value of the validation's type,
// fails, as Validate does, but with the Go names of the fields rather than
// JSON Pointers, as validators of their own report failures; see Pointer.
func (p *Validation) Failures(v reflect.Value) []Failure {
	var out []Failure
	p.root.failures(v, nil, &out)
	return out
}

func (s *validatedStruct) validate(v reflect.Value, prefix string, out *[]Violation) {
	for i := range s.fields {
		f := &s.fields[i]
		f.validate(v.Field(f.index), prefix+f.segment, out)
	}
}

func (s *validatedStruct) failures(v reflect.Value, path []string, out *[]Failure) {
	for i := range s.fields {
		f := &s.fields[i]
		f.failures(v.Field(f.index), append(path[:len(path):len(path)], f.name), out)
	}
}

func (f *validatedField) validate(v reflect.Value, pointer string, out *[]Violation) {
	failed, v := f.check(v)
	switch {
	case failed != nil:
		*out = append(*out, Violation{Pointer: pointer, Detail: failed.detail})
	case v.IsValid() && f.nested != nil:
		f.nested.validate(v, pointer, out)
	}
}

func (f *validatedField) failures(v reflect.Value, path []string, out *[]Failure) {
	failed, v := f.check(v)
	switch {
	case failed != nil:
		*out = append(*out, Failure{Path: path, Rule: failed.name, Param: failed.param})
	case v.IsValid() && f.nested != nil:
		f.nested.failures(v, path, out)
	}
}

// check returns the rule of the field that v, its value, fails, if any;
// otherwise v past its pointers, whose fields are checked next, or the zero
// Value if they aren't.
func (f *validatedField) check(v reflect.Value) (*rule, reflect.Value) {
	// Like go-playground, go through pointers to the value. A nil one only
	// answers to the first rule: omitempty skips it, any other rule fails.
	fromPointer := false
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			if len(f.rules) > 0 && f.rules[0].name != "omitempty" {
				return &f.rules[0], reflect.Value{}
			}
			return nil, reflect.Value{}
		}
		v = v.Elem()
		fromPointer = true
	}
	for i := range f.rules {
		r := &f.rules[i]
		if r.name == "omitempty" {
			if !hasValue(v, fromPointer) {
				return nil, reflect.Value{}
			}
			continue
		}
		if !r.check(v, fromPointer) {
			return r, reflect.Value{}
		}
	}
	return nil, v
}

// hasValue is hasValue of go-playground, which required and omitempty use:
// nil and zero values have none, while a value behind a non-nil pointer
// has one even if it is zero.
func hasValue(v reflect.Value, fromPointer bool) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
		return !v.IsNil()
	}
	return fromPointer || v.IsValid() && !v.IsZero()
}

// validationBuilder builds the plans of struct types, once per type, so
// that recursive types end.
type validationBuilder struct {
	plans map[reflect.Type]*validatedStruct
}

// build returns the plan of the struct type t, or nil if there is nothing
// to validate. path names t's field in messages.
func (b *validationBuilder) build(t reflect.Type, path string) (*validatedStruct, error) {
	if s, ok := b.plans[t]; ok {
		return s, nil
	}
	s := &validatedStruct{}
	b.plans[t] = s // a recursive type finds its plan while it's built
	for i := range t.NumField() {
		sf := t.Field(i)
		tag := sf.Tag.Get("validate")
		if !sf.IsExported() && !sf.Anonymous || tag == "-" {
			continue // as go-playground skips them
		}
		name := sf.Name
		if path != "" {
			name = path + "." + sf.Name
		}
		f := validatedField{index: i, name: sf.Name, segment: segment(sf)}

		value := sf.Type
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		var err error
		if tag != "" {
			if f.rules, err = parseRules(name, tag, value); err != nil {
				return nil, err
			}
		}
		if value.Kind() == reflect.Struct && !value.ConvertibleTo(reflect.TypeFor[time.Time]()) {
			if f.nested, err = b.build(value, name); err != nil {
				return nil, err
			}
		}
		if len(f.rules) > 0 || f.nested != nil {
			s.fields = append(s.fields, f)
		}
	}
	if len(s.fields) == 0 {
		b.plans[t] = nil
		return nil, nil
	}
	return s, nil
}

// segment returns the segment of the JSON Pointer of sf's field: none for
// a struct that JSON embeds, the JSON name otherwise, and the Go name for
// a field that JSON leaves out.
func segment(sf reflect.StructField) string {
	tag := parseJSONTag(sf)
	if tag.ignored {
		return pointer("", sf.Name)
	}
	t := sf.Type
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	if (tag.embed || sf.Anonymous && !tag.tagged) && t.Kind() == reflect.Struct {
		return ""
	}
	return pointer("", tag.name)
}

// parseRules returns the rules of the validate tag of the field name, whose
// value, past any pointers, is of type t.
func parseRules(name, tag string, t reflect.Type) ([]rule, error) {
	if strings.Contains(tag, "|") {
		return nil, fmt.Errorf("field %s: validate:%q: alternatives with | aren't supported; %s", name, tag, hint)
	}
	var rules []rule
	for part := range strings.SplitSeq(tag, ",") {
		key, param, _ := strings.Cut(part, "=")
		r, err := newRule(key, param, t)
		if err != nil {
			return nil, fmt.Errorf("field %s: validate:%q: %w", name, tag, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// keywordRules returns the rules of the validate tag of a field, whose
// value, past any pointers, is of type t, that give the schema of the field
// keywords: those that the core knows and that apply to t. A validator of
// its own may take other rules, and then the keywords of a rule the core
// doesn't know would promise what the schema can't vouch for. The rules
// after dive check the elements, and the alternatives with | either, so
// neither adds keywords. Without such a validator, Mount checked the tag,
// and these are all of its rules.
func keywordRules(tag string, t reflect.Type) []rule {
	var rules []rule
	for part := range strings.SplitSeq(tag, ",") {
		if part == "dive" {
			break
		}
		if strings.Contains(part, "|") {
			continue
		}
		key, param, _ := strings.Cut(part, "=")
		if r, err := newRule(key, param, t); err == nil {
			rules = append(rules, r)
		}
	}
	return rules
}

// Detail returns what a violation of the rule key=param says of a value of
// type t, past pointers, and reports whether the core knows the rule for
// the type: the detail of a failure that a validator of its own reports.
func Detail(key, param string, t reflect.Type) (string, bool) {
	r, err := newRule(key, param, t)
	if err != nil || r.detail == "" {
		return "", false
	}
	return r.detail, true
}

// Pointer returns the JSON Pointer of the value at path in a value of the
// struct type t, and the type of that value, past pointers. path is as
// Failures and validators of their own report it: the Go names of fields,
// the embedded ones among them, and the indexes of the elements of slices
// and arrays and the keys of maps. The segment of a field is the one that
// Validate gives it, so the pointers are those of Validate.
func Pointer(t reflect.Type, path []string) (string, reflect.Type, error) {
	ptr := ""
	for i, step := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			sf, ok := directField(t, step)
			if !ok {
				return "", nil, fmt.Errorf("%v has no field %s", t, step)
			}
			ptr += segment(sf)
			t = sf.Type
		case reflect.Slice, reflect.Array:
			if _, err := strconv.ParseUint(step, 10, 0); err != nil {
				return "", nil, fmt.Errorf("%v has no element %q", t, step)
			}
			ptr += "/" + step
			t = t.Elem()
		case reflect.Map:
			ptr = pointer(ptr, step)
			t = t.Elem()
		default:
			return "", nil, fmt.Errorf("path %s goes past %v", strings.Join(path[:i+1], "."), t)
		}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return ptr, t, nil
}

// directField returns the field of the struct type t with the Go name
// name, an embedded one too, but not a field promoted from one.
func directField(t reflect.Type, name string) (reflect.StructField, bool) {
	for sf := range t.Fields() {
		if sf.Name == name {
			return sf, true
		}
	}
	return reflect.StructField{}, false
}

// ruleNames are the names of the rules that the core knows.
var ruleNames = []string{
	"required", "omitempty", "min", "max", "len", "gt", "gte", "lt", "lte",
	"oneof", "email", "url", "http_url", "uuid",
}

// newRule returns the rule key=param for values of type t.
func newRule(key, param string, t reflect.Type) (rule, error) {
	r := rule{name: key, param: param}
	if (key == "required" || key == "omitempty") && param != "" {
		return r, fmt.Errorf("rule %q takes no parameter", key)
	}
	switch key {
	case "required":
		r.detail = "is required"
		r.check = hasValue
		r.keywords = func(s *jsonschema.Schema, f form) {
			// The member is required anyway. The value must be non-zero,
			// unless it is behind a pointer, which may point to zero.
			if f.pointer || f.quoted {
				return
			}
			switch k := t.Kind(); {
			case t == reflect.TypeFor[time.Time]():
				// The zero time, as json/v2 writes it. Other ways to write
				// that instant in UTC, such as with fractions of a second,
				// escape the keyword.
				if s.Not == nil {
					s.Not = &jsonschema.Schema{Const: jsontext.Value(`"0001-01-01T00:00:00Z"`)}
				}
			case k == reflect.String:
				bound(s, characters, atLeast, 1)
			case k == reflect.Bool:
				s.Const = jsontext.Value("true")
			case isInt(k) || isUint(k) || k == reflect.Float32 || k == reflect.Float64:
				if s.Not == nil {
					s.Not = &jsonschema.Schema{Const: jsontext.Value("0")}
				}
			}
		}
		return r, nil
	case "omitempty":
		// Handled by validatedField.validate. It adds no keywords: a schema
		// describes the canonical form, where a zero value that omitempty
		// skips isn't sent at all.
		r.keywords = func(*jsonschema.Schema, form) {}
		return r, nil
	}

	c, compares := comparisonOf(key)
	switch {
	case !slices.Contains(ruleNames, key):
		return r, fmt.Errorf("unknown rule %q; %s", key, hint)
	case t.ConvertibleTo(reflect.TypeFor[time.Time]()):
		return r, fmt.Errorf("rule %q doesn't apply to %v: the core supports only required and omitempty for times", key, t)
	case compares:
		return compareRule(r, c, param, t)
	case key == "oneof":
		return oneOfRule(r, param, t)
	}
	return stringRule(r, param, t)
}

// comparison says how a rule compares a length or a number with its
// parameter, and how its violations read.
type comparison struct {
	op           operator
	chars, items string // for strings and for collections, with %s the count
	number       string // for numbers, with %s the parameter
}

// operator is how a rule compares a value with its parameter.
type operator int

const (
	atLeast operator = iota // >=
	atMost                  // <=
	exactly                 // ==
	above                   // >
	below                   // <
)

// comparisonOf returns the comparison of the rule key and reports whether
// key compares lengths or numbers.
func comparisonOf(key string) (comparison, bool) {
	switch key {
	case "min", "gte":
		return comparison{atLeast, "must be at least %s", "must have at least %s", "must be at least %s"}, true
	case "max", "lte":
		return comparison{atMost, "must be at most %s", "must have at most %s", "must be at most %s"}, true
	case "len":
		return comparison{exactly, "must be exactly %s", "must have exactly %s", "must be %s"}, true
	case "gt":
		return comparison{above, "must be more than %s", "must have more than %s", "must be greater than %s"}, true
	case "lt":
		return comparison{below, "must be fewer than %s", "must have fewer than %s", "must be less than %s"}, true
	}
	return comparison{}, false
}

// holds reports whether x compares with n as op says, with the operators of
// Go, as go-playground compares: NaN fails every comparison.
func holds[T int64 | uint64 | float64](op operator, x, n T) bool {
	switch op {
	case atLeast:
		return x >= n
	case atMost:
		return x <= n
	case exactly:
		return x == n
	case above:
		return x > n
	}
	return x < n
}

// compareRule returns a rule of cmp, which compares the length of a string
// in runes or of a collection, or the value of a number, with param.
func compareRule(r rule, c comparison, param string, t reflect.Type) (rule, error) {
	bad := func(err error) (rule, error) {
		return r, fmt.Errorf("rule %s=%s: bad parameter for %v: %w", r.name, param, t, err)
	}
	switch k := t.Kind(); {
	case k == reflect.String || k == reflect.Slice || k == reflect.Map || k == reflect.Array:
		n, err := strconv.ParseInt(param, 0, 64)
		if err != nil {
			return bad(err)
		}
		if k == reflect.String {
			r.detail = fmt.Sprintf(c.chars, count(n, "character"))
			r.check = func(v reflect.Value, _ bool) bool {
				return holds(c.op, int64(utf8.RuneCountInString(v.String())), n)
			}
			// Runes are code points, which JSON Schema counts too.
			r.keywords = func(s *jsonschema.Schema, _ form) { bound(s, characters, c.op, n) }
		} else {
			r.detail = fmt.Sprintf(c.items, count(n, "item"))
			r.check = func(v reflect.Value, _ bool) bool { return holds(c.op, int64(v.Len()), n) }
			switch {
			case k != reflect.Map && t.Elem() == reflect.TypeFor[byte]():
				// Bytes are a base64 string, whose length JSON Schema can't
				// relate to the number of bytes.
				r.keywords = func(*jsonschema.Schema, form) {}
			case k == reflect.Map:
				r.keywords = func(s *jsonschema.Schema, _ form) { bound(s, properties, c.op, n) }
			default:
				r.keywords = func(s *jsonschema.Schema, _ form) { bound(s, items, c.op, n) }
			}
		}
	case isInt(k):
		n, err := parseInt(param, t)
		if err != nil {
			return bad(err)
		}
		text := strconv.FormatInt(n, 10)
		if t == reflect.TypeFor[time.Duration]() {
			text = time.Duration(n).String()
		}
		r.detail = fmt.Sprintf(c.number, text)
		r.check = func(v reflect.Value, _ bool) bool { return holds(c.op, v.Int(), n) }
		r.keywords = numberKeywords(c.op, jsontext.Value(strconv.FormatInt(n, 10)))
	case isUint(k):
		n, err := strconv.ParseUint(param, 0, 64)
		if err != nil {
			return bad(err)
		}
		r.detail = fmt.Sprintf(c.number, strconv.FormatUint(n, 10))
		r.check = func(v reflect.Value, _ bool) bool { return holds(c.op, v.Uint(), n) }
		r.keywords = numberKeywords(c.op, jsontext.Value(strconv.FormatUint(n, 10)))
	case k == reflect.Float32 || k == reflect.Float64:
		n, err := strconv.ParseFloat(param, t.Bits())
		if err != nil {
			return bad(err)
		}
		text := strconv.FormatFloat(n, 'g', -1, t.Bits())
		r.detail = fmt.Sprintf(c.number, text)
		r.check = func(v reflect.Value, _ bool) bool { return holds(c.op, v.Float(), n) }
		r.keywords = func(s *jsonschema.Schema, f form) {
			if !f.quoted {
				limitFloat(s, c.op, n, text)
			}
		}
	default:
		return r, fmt.Errorf("rule %q doesn't apply to %v", r.name, t)
	}
	return r, nil
}

// numberKeywords returns the keywords of a comparison of an integer with p,
// the parameter as a JSON number. A number in a JSON string, by the string
// option, gets none: JSON Schema can't compare it.
func numberKeywords(op operator, p jsontext.Value) func(s *jsonschema.Schema, f form) {
	return func(s *jsonschema.Schema, f form) {
		if !f.quoted {
			limit(s, op, p)
		}
	}
}

// splitParams splits the parameter of oneof as go-playground does: into
// words or 'quoted phrases'.
var splitParams = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`'[^']*'|\S+`)
})

// oneOfRule returns a rule that checks that a string or an integer is one
// of the values in param, compared as text, as go-playground does.
func oneOfRule(r rule, param string, t reflect.Type) (rule, error) {
	values := splitParams().FindAllString(param, -1)
	for i, v := range values {
		values[i] = strings.ReplaceAll(v, "'", "")
	}
	if len(values) == 0 {
		return r, errors.New("rule oneof needs values")
	}
	var text func(v reflect.Value) string
	switch k := t.Kind(); {
	case k == reflect.String:
		text = reflect.Value.String
	case isInt(k):
		text = func(v reflect.Value) string { return strconv.FormatInt(v.Int(), 10) }
	case isUint(k):
		text = func(v reflect.Value) string { return strconv.FormatUint(v.Uint(), 10) }
	default:
		return r, fmt.Errorf("rule %q doesn't apply to %v", r.name, t)
	}
	r.detail = "must be one of: " + strings.Join(values, ", ")
	r.check = func(v reflect.Value, _ bool) bool {
		return slices.Contains(values, text(v))
	}
	r.keywords = func(s *jsonschema.Schema, f form) {
		s.Enum = nil
		for _, v := range values {
			switch {
			case t.Kind() == reflect.String:
				s.Enum = append(s.Enum, quote(v))
			case !isNumeral(v):
				// No integer has this text, so no value matches it.
			case f.quoted:
				s.Enum = append(s.Enum, quote(v))
			default:
				s.Enum = append(s.Enum, jsontext.Value(v))
			}
		}
		if len(s.Enum) == 0 {
			nothing(s)
		}
	}
	return r, nil
}

// isNumeral reports whether s is an integer as strconv formats one, the
// only text of an integer that oneof can match.
func isNumeral(s string) bool {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(n, 10) == s
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == s
}

// stringRule returns the rule email, url, http_url or uuid, which check
// strings; uuid also checks a fmt.Stringer, as go-playground does.
func stringRule(r rule, param string, t reflect.Type) (rule, error) {
	if param != "" {
		return r, fmt.Errorf("rule %q takes no parameter", r.name)
	}
	isString := t.Kind() == reflect.String
	stringer := r.name == "uuid" && t.Implements(reflect.TypeFor[fmt.Stringer]())
	if !isString && !stringer {
		return r, fmt.Errorf("rule %q doesn't apply to %v", r.name, t)
	}
	var matches func(s string) bool
	switch r.name {
	case "email":
		r.detail, matches = "must be an email address", isEmail
	case "url":
		r.detail, matches = "must be a URL", isURL
	case "http_url":
		r.detail, matches = "must be an http or https URL", isHTTPURL
	default:
		r.detail, matches = "must be a UUID", isUUID
	}
	r.check = func(v reflect.Value, _ bool) bool {
		if isString {
			return matches(v.String())
		}
		return matches(v.Interface().(fmt.Stringer).String())
	}
	r.keywords = func(s *jsonschema.Schema, _ form) {
		if !isString {
			return // a Stringer, whose JSON may not be the string it checks
		}
		switch r.name {
		case "email":
			s.Format = "email"
		case "url":
			s.Format = "uri"
		case "http_url":
			s.Format, s.Pattern = "uri", httpURLPattern
		default:
			s.Format = "uuid"
		}
	}
	return r, nil
}

// httpURLPattern is the pattern of http_url, next to the format uri: the
// scheme http or https, in any case, and a host.
const httpURLPattern = "^[Hh][Tt][Tt][Pp][Ss]?://[^/?#]"

// isURL is isURL of go-playground: a URL with a scheme and, but for file
// URLs, which need a path, a host, a fragment or an opaque part.
func isURL(s string) bool {
	s = strings.ToLower(s)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return false
	}
	if u.Scheme == "file" {
		return u.Path != "" && u.Path != "/"
	}
	return u.Host != "" || u.Fragment != "" || u.Opaque != ""
}

// isHTTPURL is isHttpURL of go-playground: a URL, see isURL, with a host
// and the scheme http or https.
func isHTTPURL(s string) bool {
	if !isURL(s) {
		return false
	}
	u, err := url.Parse(strings.ToLower(s))
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// isUUID matches uUIDRegexString of go-playground: 8-4-4-4-12 hexadecimal
// digits of either case.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// parseInt parses the parameter of a rule for a signed integer type t, as
// go-playground does: in any base with a prefix, or as a duration for
// time.Duration.
func parseInt(param string, t reflect.Type) (int64, error) {
	if t == reflect.TypeFor[time.Duration]() {
		if d, err := time.ParseDuration(param); err == nil {
			return int64(d), nil
		}
	}
	return strconv.ParseInt(param, 0, 64)
}

// count returns n of unit, in the plural unless n is 1.
func count(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.FormatInt(n, 10) + " " + unit + "s"
}

func isInt(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Int64
}

func isUint(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uint64
}
