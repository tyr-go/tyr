package plan

import (
	"encoding"
	"fmt"
	"net/http"
	"net/textproto"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Headers sets the headers of responses from the fields of results.
type Headers struct {
	fields []headerField // in the order of the struct
}

// headerField is a field of a result that sets a header.
type headerField struct {
	name   string // canonical
	goName string // with the embedded structs on the way
	index  []int
	typ    reflect.Type
	format func(v reflect.Value) (string, error) // "" sets no header
}

// HeaderField is a field of a result that sets a header.
type HeaderField struct {
	Name  string       // of the header, canonical
	Index []int        // of the field, through embedded structs
	Type  reflect.Type // of the field
}

// Fields returns the fields that set headers, in the order of the struct.
func (h *Headers) Fields() []HeaderField {
	out := make([]HeaderField, len(h.fields))
	for i, f := range h.fields {
		out[i] = HeaderField{Name: f.name, Index: f.index, Type: f.typ}
	}
	return out
}

// NewHeaders returns the headers of results of type t. A field of a struct
// t, or of the struct t points to, may have the tag header, which names the
// header it sets. The field must be of type string, bool, an integer or a
// float type, time.Time, which becomes an HTTP date, or implement
// encoding.TextMarshaler, or be a pointer to one of those. A nil pointer,
// an empty string and a zero time set no header. The field may be left out
// of JSON with json:"-".
//
// The tags path and query mean nothing in a result, and a type may be both
// a request and a result, so they are left alone.
//
// NewHeaders fails on a field of another type, on a header tag that isn't a
// header name, repeats one or names a header the server sets itself
// (Connection, Content-Length, Content-Type or Transfer-Encoding), on an
// unexported field and on a field of a nested struct.
func NewHeaders(t reflect.Type) (*Headers, error) {
	h := &Headers{}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return h, nil
	}
	seen := make(map[string]string) // header → Go name
	for _, tf := range taggedFields(t) {
		f := tf.field
		name, ok := f.Tag.Lookup(Header.Tag())
		if !ok {
			continue // path or query
		}
		tag := fmt.Sprintf("header:%q", name)
		switch {
		case !f.IsExported():
			return nil, fmt.Errorf("field %s has %s, but it is unexported", tf.name, tag)
		case tf.nested:
			return nil, fmt.Errorf("field %s has %s, but only fields of %v and of structs embedded in it can set headers", tf.name, tag, t)
		case name == "":
			return nil, fmt.Errorf("field %s has an empty header tag", tf.name)
		case !isToken(name):
			return nil, fmt.Errorf("field %s has %s, which isn't a header name", tf.name, tag)
		}
		name = textproto.CanonicalMIMEHeaderKey(name)
		if ownHeader(name) {
			return nil, fmt.Errorf("field %s has %s, but the server sets %s itself", tf.name, tag, name)
		}
		if other, ok := seen[name]; ok {
			return nil, fmt.Errorf("fields %s and %s both set the header %s", other, tf.name, name)
		}
		seen[name] = tf.name
		format := formatter(f.Type)
		if format == nil {
			return nil, fmt.Errorf("field %s has %s, but its type %v can't set a header", tf.name, tag, f.Type)
		}
		h.fields = append(h.fields, headerField{name: name, goName: tf.name, index: tf.index, typ: f.Type, format: format})
	}
	return h, nil
}

// Has reports whether a field sets the header with the name.
func (h *Headers) Has(name string) bool {
	name = textproto.CanonicalMIMEHeaderKey(name)
	return slices.ContainsFunc(h.fields, func(f headerField) bool { return f.name == name })
}

// Of returns the headers that the fields of v, a result, set, or nil if
// none do; a field under a nil embedded pointer sets none. The error is one
// of a MarshalText method.
func (h *Headers) Of(v reflect.Value) (http.Header, error) {
	if len(h.fields) == 0 || !v.IsValid() {
		return nil, nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	var header http.Header
	for _, f := range h.fields {
		fv, ok := fieldByIndex(v, f.index, false)
		if !ok {
			continue
		}
		s, err := f.format(fv)
		if err != nil {
			return nil, fmt.Errorf("field %s, header %s: %w", f.goName, f.name, err)
		}
		if s == "" {
			continue
		}
		if header == nil {
			header = make(http.Header, len(h.fields))
		}
		header[f.name] = []string{s}
	}
	return header, nil
}

// NoMembers reports whether the values of t, a struct type or a pointer to
// one, are JSON objects without members, like struct{}: json/v2 leaves out
// all the fields, such as those with json:"-".
func NoMembers(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	ms, err := members(t)
	return err == nil && len(ms) == 0
}

// isToken reports whether s is a token of RFC 9110, as header names are.
func isToken(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return s != ""
}

// ownHeader reports whether the server sets the header with the canonical
// name itself.
func ownHeader(name string) bool {
	switch name {
	case "Connection", "Content-Length", "Content-Type", "Transfer-Encoding":
		return true
	}
	return false
}

// formatter returns the function that formats a value of type t for a
// header, "" for no header, or nil if t can't set a header.
func formatter(t reflect.Type) func(v reflect.Value) (string, error) {
	if t.Kind() == reflect.Pointer {
		format := formatter(t.Elem())
		if format == nil || t.Elem().Kind() == reflect.Pointer {
			return nil
		}
		return func(v reflect.Value) (string, error) {
			if v.IsNil() {
				return "", nil
			}
			return format(v.Elem())
		}
	}
	if t == reflect.TypeFor[time.Time]() {
		return func(v reflect.Value) (string, error) {
			tm := v.Interface().(time.Time)
			if tm.IsZero() {
				return "", nil
			}
			return tm.UTC().Format(http.TimeFormat), nil
		}
	}
	if marshaler := reflect.TypeFor[encoding.TextMarshaler](); reflect.PointerTo(t).Implements(marshaler) {
		return func(v reflect.Value) (string, error) {
			if !v.CanAddr() { // for MarshalText with a pointer receiver
				p := reflect.New(t).Elem()
				p.Set(v)
				v = p
			}
			text, err := v.Addr().Interface().(encoding.TextMarshaler).MarshalText()
			return string(text), err
		}
	}

	switch t.Kind() {
	case reflect.String:
		return func(v reflect.Value) (string, error) { return v.String(), nil }
	case reflect.Bool:
		return func(v reflect.Value) (string, error) { return strconv.FormatBool(v.Bool()), nil }
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(v reflect.Value) (string, error) { return strconv.FormatInt(v.Int(), 10), nil }
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return func(v reflect.Value) (string, error) { return strconv.FormatUint(v.Uint(), 10), nil }
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(v reflect.Value) (string, error) { return strconv.FormatFloat(v.Float(), 'g', -1, bits), nil }
	}
	return nil
}
