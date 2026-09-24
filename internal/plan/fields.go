package plan

import (
	"cmp"
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// member is a member of the JSON object of a struct type: a field of the
// struct or of a struct embedded in it, as encoding/json/v2 sees it.
type member struct {
	name     string              // the member's name
	tagged   bool                // the json tag gives the name
	index    []int               // of the field, through embedded structs
	field    reflect.StructField // the Go field
	omit     bool                // omitzero or omitempty may leave it out
	quoted   bool                // the string option: a number in a JSON string
	indirect bool                // under an embedded pointer, which leaves it out while nil
}

// members returns the members of the JSON object of the struct type t, in
// the order of the fields, following the rules of encoding/json/v2:
//
//   - A field with json:"-" or an unexported field is left out.
//   - A Go embedded struct without a JSON name, or a field with the embed
//     option, has its members promoted into t's object; an embedded struct
//     with a JSON name is a member of its own.
//   - Of members with the same name, the one at the shallowest depth wins;
//     at the same depth, the only one with a JSON name in its tag wins, and
//     otherwise none does.
//
// members fails where json/v2 fails to use t, e.g. on a malformed json
// tag or two fields of one struct with the same name: it asks json/v2. It
// also fails if t has JSON methods, maybe promoted from an embedded
// struct: then json/v2 uses them instead of t's fields.
func members(t reflect.Type) ([]member, error) {
	ms, _, err := objectOf(t)
	return ms, err
}

// objectOf returns the members of the JSON object of the struct type t, as
// members does, and the type of its embedded fallback for unknown members,
// a map[~string]T or jsontext.Value, or nil if it has none.
func objectOf(t reflect.Type) (ms []member, fallback reflect.Type, err error) {
	if hasJSONMethods(t) {
		return nil, nil, fmt.Errorf("%v has JSON methods, so its fields aren't members of a JSON object", t)
	}
	if err := json.Unmarshal([]byte("{}"), reflect.New(t).Interface()); err != nil {
		return nil, nil, fmt.Errorf("json/v2 can't use %v: %w", t, err)
	}
	type queued struct {
		typ      reflect.Type
		index    []int
		visit    bool // whether to visit the structs embedded in typ
		indirect bool // an embedded pointer is on the way
	}
	queue := []queued{{typ: t, visit: true}}
	seen := map[reflect.Type]bool{t: true}

	// json/v2 accepts t, so the walk meets no malformed tags or conflicts
	// within one struct.
	var all []member
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		for i := range q.typ.NumField() {
			sf := q.typ.Field(i)
			tag := parseJSONTag(sf)
			if tag.ignored || !sf.IsExported() && !sf.Anonymous {
				continue
			}
			index := append(slices.Clip(q.index), i)
			if tag.embed || sf.Anonymous && !tag.tagged {
				et := sf.Type
				pointer := et.Kind() == reflect.Pointer && et.Name() == ""
				if pointer {
					et = et.Elem()
				}
				if et.Kind() != reflect.Struct {
					// A map or a jsontext.Value, as json/v2 accepted t: a
					// fallback for unknown members, not a member.
					if fallback == nil {
						fallback = et
					}
					continue
				}
				if q.visit {
					queue = append(queue, queued{typ: et, index: index, visit: !seen[et], indirect: q.indirect || pointer})
				}
				seen[et] = true
				continue
			}
			all = append(all, member{
				name: tag.name, tagged: tag.tagged, index: index, field: sf,
				omit: tag.omit, quoted: tag.quoted, indirect: q.indirect,
			})
		}
	}

	// Of the members with one name, keep the one that dominates, if any.
	byName := slices.Clone(all)
	slices.SortStableFunc(byName, func(x, y member) int {
		return cmp.Or(
			strings.Compare(x.name, y.name),
			cmp.Compare(len(x.index), len(y.index)),
			-cmp.Compare(rank(x.tagged), rank(y.tagged)), // tagged first
		)
	})
	var kept []member
	for len(byName) > 0 {
		n := 1
		for n < len(byName) && byName[n].name == byName[0].name {
			n++
		}
		if n == 1 || len(byName[0].index) != len(byName[1].index) || byName[0].tagged != byName[1].tagged {
			kept = append(kept, byName[0])
		}
		byName = byName[n:]
	}

	// Back to the order of the fields, depth first.
	slices.SortFunc(kept, func(x, y member) int {
		return slices.Compare(x.index, y.index)
	})
	return kept, fallback, nil
}

// jsonTag is what the json tag of a field says.
type jsonTag struct {
	name      string // the JSON name, which is the Go name if the tag gives none
	tagged    bool   // the tag gives the name
	embed     bool   // the embed option
	ignored   bool   // json:"-": the field is left out
	omit      bool   // the omitzero or the omitempty option
	omitzero  bool   // the omitzero option
	omitempty bool   // the omitempty option
	quoted    bool   // the string option
}

// parseJSONTag returns what the json tag of sf says.
func parseJSONTag(sf reflect.StructField) jsonTag {
	tag, ok := sf.Tag.Lookup("json")
	if tag == "-" {
		return jsonTag{ignored: true}
	}
	t := jsonTag{name: sf.Name}
	if !ok {
		return t
	}
	given, opts, _ := strings.Cut(tag, ",")
	if given != "" {
		t.name, t.tagged = given, true
	}
	for opt := range strings.SplitSeq(opts, ",") {
		switch opt {
		case "embed":
			t.embed = true
		case "omitzero":
			t.omit, t.omitzero = true, true
		case "omitempty":
			t.omit, t.omitempty = true, true
		case "string":
			t.quoted = true
		}
	}
	return t
}

// hasJSONMethods reports whether values of type t marshal or unmarshal
// themselves.
func hasJSONMethods(t reflect.Type) bool {
	for _, m := range []reflect.Type{
		reflect.TypeFor[json.Marshaler](),
		reflect.TypeFor[json.MarshalerTo](),
		reflect.TypeFor[json.Unmarshaler](),
		reflect.TypeFor[json.UnmarshalerFrom](),
		reflect.TypeFor[encoding.TextMarshaler](),
		reflect.TypeFor[encoding.TextAppender](),
		reflect.TypeFor[encoding.TextUnmarshaler](),
	} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// rank is 1 for true and 0 for false, to sort by.
func rank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pointer returns the JSON Pointer of the member name under prefix.
func pointer(prefix, name string) string {
	return prefix + string(jsontext.Pointer("").AppendToken(name))
}

// fieldByIndex returns the field of the struct v at index, allocating nil
// embedded pointers on the way if alloc is set; without alloc, it reports
// false for a field behind a nil pointer.
func fieldByIndex(v reflect.Value, index []int, alloc bool) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !alloc {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, true
}
