package plan

import (
	"encoding"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// EnumCheck checks the values of enum types wherever they are in values of
// a type: in the fields of structs, nested and embedded ones, behind
// pointers, as elements of slices and arrays, and as keys and values of
// maps. NewEnumCheck makes it, once per type.
type EnumCheck struct {
	root *enumNode
	dir  Direction
}

// EnumFailure is a value of an enum type that isn't one of its values.
type EnumFailure struct {
	Pointer string        // the JSON Pointer of the value
	Value   reflect.Value // the value
	Type    reflect.Type  // its enum type
	Detail  string        // what a violation says of it, such as must be one of: todo, doing, done
	Field   bool          // the value is that of a field, not an element, a key or the whole
}

// enumNode checks the values of one type.
type enumNode struct {
	kind   reflect.Kind
	enum   *enum       // for an enum type
	elem   *enumNode   // of a pointer, of the elements of a slice or an array, of the values of a map
	key    *enumNode   // of the keys of a map, of an enum type
	fields []enumField // of a struct
	live   bool        // it checks something, itself or under it
}

// enumField is a field of a struct, whose value is checked.
type enumField struct {
	index     []int  // of the field, through embedded structs
	segment   string // of the JSON Pointer; "" for a struct embedded in JSON
	omitzero  bool   // json/v2 leaves the member out while its value is zero
	omitempty bool   // json/v2 leaves the member out while its JSON is empty
	node      *enumNode
}

// NewEnumCheck returns the check of the values of enum types in values of
// type t that go the way dir says, or nil if they can have none.
//
// For Input, a failure within a field that holds the zero value of its type
// doesn't count: the member was left out, or means "not set". The elements
// of slices and arrays, the keys and the values of maps, and the values
// behind non-nil pointers count, as they were sent. For Output, what
// json/v2 writes counts, and a failure within a member that omitzero or
// omitempty leaves out doesn't; a field that REST sends as a header counts
// too.
//
// NewEnumCheck fails on an enum type that makes no enum, and on an enum of
// numbers as the keys of a map, whose members are named by strings.
func NewEnumCheck(t reflect.Type, dir Direction) (*EnumCheck, error) {
	b := &enumBuilder{dir: dir, nodes: map[reflect.Type]*enumNode{}}
	root, err := b.node(t)
	if err != nil {
		return nil, err
	}
	b.settle()
	if !root.live {
		return nil, nil
	}
	return &EnumCheck{root: root, dir: dir}, nil
}

// OK reports whether every value of an enum type in v, a value of the
// check's type, is one of its values, but for the failures that don't
// count. It allocates nothing, but a key and a value for each map it walks.
func (c *EnumCheck) OK(v reflect.Value) bool {
	return c.root.ok(v, c.dir)
}

// Failures returns the values of enum types in v, a value of the check's
// type, that aren't among their values, but for those that don't count:
// all of them, in the order of the fields, the elements and the keys, the
// keys of maps sorted by their names, or only the first if first is set.
func (c *EnumCheck) Failures(v reflect.Value, first bool) []EnumFailure {
	if c.OK(v) {
		return nil
	}
	var out []EnumFailure
	c.root.failures(v, "", false, first, c.dir, &out)
	return out
}

// ok reports whether the values of enum types in v count as valid.
func (n *enumNode) ok(v reflect.Value, dir Direction) bool {
	if n.enum != nil {
		return n.enum.has(v)
	}
	switch n.kind {
	case reflect.Pointer:
		return v.IsNil() || n.elem.ok(v.Elem(), dir)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if !n.elem.ok(v.Index(i), dir) {
				return false
			}
		}
	case reflect.Map:
		if v.Len() == 0 {
			return true
		}
		// One key and one value for the whole walk, rather than copies of
		// each.
		var it reflect.MapIter
		it.Reset(v)
		var key, value reflect.Value
		if n.key != nil {
			key = reflect.New(v.Type().Key()).Elem()
		}
		if n.elem != nil {
			value = reflect.New(v.Type().Elem()).Elem()
		}
		for it.Next() {
			if n.key != nil {
				key.SetIterKey(&it)
				if !n.key.ok(key, dir) {
					return false
				}
			}
			if n.elem != nil {
				value.SetIterValue(&it)
				if !n.elem.ok(value, dir) {
					return false
				}
			}
		}
	case reflect.Struct:
		for i := range n.fields {
			f := &n.fields[i]
			fv, ok := fieldByIndex(v, f.index, false)
			if ok && !f.node.ok(fv, dir) && !f.discounts(fv, dir) {
				return false
			}
		}
	}
	return true
}

// failures appends to out the failures within v, at the pointer p; field
// is whether v is the value of a field.
func (n *enumNode) failures(v reflect.Value, p string, field, first bool, dir Direction, out *[]EnumFailure) {
	if n.enum != nil {
		if !n.enum.has(v) {
			*out = append(*out, EnumFailure{Pointer: p, Value: v, Type: n.enum.typ, Detail: n.enum.detail, Field: field})
		}
		return
	}
	switch n.kind {
	case reflect.Pointer:
		if !v.IsNil() {
			n.elem.failures(v.Elem(), p, field, first, dir, out)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			n.elem.failures(v.Index(i), p+"/"+strconv.Itoa(i), false, first, dir, out)
			if first && len(*out) > 0 {
				return
			}
		}
	case reflect.Map:
		keys := v.MapKeys()
		names := make(map[reflect.Value]string, len(keys))
		for _, k := range keys {
			names[k] = keyName(k)
		}
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(names[a], names[b]) })
		for _, k := range keys {
			member := pointer(p, names[k])
			if n.key != nil {
				n.key.failures(k, member, false, first, dir, out)
			}
			if n.elem != nil {
				n.elem.failures(v.MapIndex(k), member, false, first, dir, out)
			}
			if first && len(*out) > 0 {
				return
			}
		}
	case reflect.Struct:
		for i := range n.fields {
			f := &n.fields[i]
			fv, ok := fieldByIndex(v, f.index, false)
			if !ok {
				continue
			}
			before := len(*out)
			f.node.failures(fv, p+f.segment, true, first, dir, out)
			if len(*out) > before && f.discounts(fv, dir) {
				*out = (*out)[:before]
			}
			if first && len(*out) > 0 {
				return
			}
		}
	}
}

// discounts reports whether the failures within v, the value of the field,
// don't count: for Input, v is zero, which means "not set"; for Output,
// json/v2 leaves the member out.
func (f *enumField) discounts(v reflect.Value, dir Direction) bool {
	if dir == Input {
		return v.IsZero()
	}
	return f.omitzero && isZeroForJSON(v) || f.omitempty && isEmptyJSON(v)
}

// isZeroForJSON reports whether omitzero of json/v2 leaves v out: its
// IsZero method says so, if it has one, or else v is the zero value.
func isZeroForJSON(v reflect.Value) bool {
	type zeroer interface{ IsZero() bool }
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return true
	}
	if v.CanInterface() {
		if z, ok := v.Interface().(zeroer); ok {
			return z.IsZero()
		}
	}
	if v.CanAddr() && v.Addr().CanInterface() {
		if z, ok := v.Addr().Interface().(zeroer); ok {
			return z.IsZero()
		}
	}
	return v.IsZero()
}

// isEmptyJSON reports whether omitempty of json/v2 leaves v out: json/v2
// writes it as null, "", {} or [].
func isEmptyJSON(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || isEmptyJSON(v.Elem())
	}
	if methodsOf(v.Type(), Output) == noMethods && v.Kind() != reflect.Struct {
		switch v.Kind() {
		case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
			return v.Len() == 0
		}
		return false // a number or a bool
	}
	if !v.CanInterface() {
		return false
	}
	data, err := json.Marshal(v.Interface())
	if err != nil {
		return false
	}
	switch string(data) {
	case "null", `""`, "{}", "[]":
		return true
	}
	return false
}

// keyName returns the name of the member that json/v2 writes for the key
// k of a map.
func keyName(k reflect.Value) string {
	if k.CanInterface() {
		if m, ok := k.Interface().(encoding.TextMarshaler); ok {
			if text, err := m.MarshalText(); err == nil {
				return string(text)
			}
		}
	}
	switch kind := k.Kind(); {
	case kind == reflect.String:
		return k.String()
	case isInt(kind):
		return strconv.FormatInt(k.Int(), 10)
	case isUint(kind):
		return strconv.FormatUint(k.Uint(), 10)
	case kind == reflect.Float32 || kind == reflect.Float64:
		return strconv.FormatFloat(k.Float(), 'g', -1, k.Type().Bits())
	}
	return fmt.Sprint(k)
}

// enumBuilder builds the nodes of the types in a type, once per type, so
// that recursive types end.
type enumBuilder struct {
	dir   Direction
	nodes map[reflect.Type]*enumNode
	all   []*enumNode
}

// node returns the node of type t.
func (b *enumBuilder) node(t reflect.Type) (*enumNode, error) {
	if n, ok := b.nodes[t]; ok {
		return n, nil
	}
	n := &enumNode{kind: t.Kind()}
	b.nodes[t] = n
	b.all = append(b.all, n)
	e, err := enumOf(t)
	switch {
	case err != nil:
		return nil, err
	case e != nil:
		n.enum, n.live = e, true
		return n, nil
	case t.Kind() != reflect.Pointer && methodsOf(t, b.dir) != noMethods:
		return n, nil // its methods write it as they like: nothing to walk
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		n.elem, err = b.node(t.Elem())
	case reflect.Map:
		if n.key, err = b.node(t.Key()); err != nil {
			return nil, err
		}
		if n.key.enum == nil {
			n.key = nil
		} else if n.key.enum.schema[0] != "string" {
			return nil, fmt.Errorf("the keys of %v are of %v, an enum of numbers, which can't name the members of a JSON object", t, t.Key())
		}
		n.elem, err = b.node(t.Elem())
	case reflect.Struct:
		err = b.fields(n, t)
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// fields adds to n, the node of the struct type t, its fields: for Input,
// those that validation walks; for Output, the members that json/v2 writes
// and the fields that REST sends as headers.
func (b *enumBuilder) fields(n *enumNode, t reflect.Type) error {
	add := func(sf reflect.StructField, f enumField) error {
		node, err := b.node(sf.Type)
		if err != nil {
			return err
		}
		f.node = node
		n.fields = append(n.fields, f)
		return nil
	}
	if b.dir == Input {
		for i := range t.NumField() {
			sf := t.Field(i)
			if !sf.IsExported() && !sf.Anonymous {
				continue
			}
			if err := add(sf, enumField{index: []int{i}, segment: segment(sf)}); err != nil {
				return err
			}
		}
		return nil
	}
	layout, _, err := objectOf(t)
	if err != nil {
		return nil // json/v2 can't write it, which the transport reports
	}
	for _, m := range layout {
		tag := parseJSONTag(m.field)
		f := enumField{index: m.index, segment: pointer("", m.name), omitzero: tag.omitzero, omitempty: tag.omitempty}
		if err := add(m.field, f); err != nil {
			return err
		}
	}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.IsExported() && parseJSONTag(sf).ignored && sf.Tag.Get("header") != "" {
			if err := add(sf, enumField{index: []int{i}, segment: pointer("", sf.Name)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// settle marks the nodes that check something, themselves or under them,
// and drops what checks nothing from the others. A recursive type needs
// rounds: a node may learn that it checks something only from a node it
// leads to, which is built after it.
func (b *enumBuilder) settle() {
	for changed := true; changed; {
		changed = false
		for _, n := range b.all {
			if n.live {
				continue
			}
			if n.elem != nil && n.elem.live || n.key != nil && n.key.live ||
				slices.ContainsFunc(n.fields, func(f enumField) bool { return f.node.live }) {
				n.live, changed = true, true
			}
		}
	}
	for _, n := range b.all {
		n.fields = slices.DeleteFunc(n.fields, func(f enumField) bool { return !f.node.live })
		if n.elem != nil && !n.elem.live {
			n.elem = nil
		}
		if n.key != nil && !n.key.live {
			n.key = nil
		}
	}
}
