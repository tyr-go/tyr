package plan

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/tyr-go/tyr/internal/jsonschema"
)

// enum is an enum type: a named type whose values are a fixed list, which
// its method EnumValues() []T gives, as tyr.Enum documents it.
type enum struct {
	typ    reflect.Type
	json   []jsontext.Value // the values as json/v2 writes them, in their order
	detail string           // what a violation says: must be one of: todo, doing, done
	schema jsonschema.Types // string, integer or number, by the JSON of the values

	// The values, by the kind of the type, so that a check allocates
	// nothing: strings, ints, uints, floats, or other comparable values.
	// Few strings or ints are scanned, which beats hashing them.
	fewStrings []string
	fewInts    []int64
	strings    map[string]bool
	ints       map[int64]bool
	uints      map[uint64]bool
	floats     map[float64]bool
	others     map[any]bool
}

// few is the most values that a check scans rather than hashes.
const few = 8

// has reports whether v, a value of the enum type, is one of its values.
func (e *enum) has(v reflect.Value) bool {
	switch k := v.Kind(); {
	case k == reflect.String:
		if e.fewStrings != nil {
			return slices.Contains(e.fewStrings, v.String())
		}
		return e.strings[v.String()]
	case isInt(k):
		if e.fewInts != nil {
			return slices.Contains(e.fewInts, v.Int())
		}
		return e.ints[v.Int()]
	case isUint(k):
		return e.uints[v.Uint()]
	case k == reflect.Float32 || k == reflect.Float64:
		return e.floats[v.Float()]
	}
	return e.others[v.Interface()]
}

// add records v as one of the values, and reports false if it is already.
func (e *enum) add(v reflect.Value) bool {
	if e.has(v) {
		return false
	}
	switch k := v.Kind(); {
	case k == reflect.String:
		e.strings[v.String()] = true
	case isInt(k):
		e.ints[v.Int()] = true
	case isUint(k):
		e.uints[v.Uint()] = true
	case k == reflect.Float32 || k == reflect.Float64:
		e.floats[v.Float()] = true
	default:
		e.others[v.Interface()] = true
	}
	return true
}

// enumOf returns the enum of type t, or nil if t isn't one: a type that
// has no method EnumValues, on T or *T. It fails on a method of another
// signature, and on values that make no enum: none, of a type that isn't
// comparable, whose JSON isn't a string or a number, or is both, or that
// repeat, as values or as JSON.
func enumOf(t reflect.Type) (*enum, error) {
	if t.Name() == "" || t.Kind() == reflect.Interface || t.Kind() == reflect.Pointer {
		return nil, nil
	}
	m, ok := reflect.PointerTo(t).MethodByName("EnumValues")
	if !ok {
		return nil, nil
	}
	if m.Type.NumIn() != 1 || m.Type.NumOut() != 1 || m.Type.Out(0) != reflect.SliceOf(t) {
		return nil, fmt.Errorf("%v has a method EnumValues, but not EnumValues() []%s, as tyr.Enum has it", t, t.Name())
	}
	if !t.Comparable() {
		return nil, fmt.Errorf("%v has a method EnumValues, but isn't comparable, as the type of an enum must be", t)
	}
	values, err := callEnumValues(m, t)
	if err != nil {
		return nil, err
	}
	if values.Len() == 0 {
		return nil, fmt.Errorf("%v.EnumValues returns no values", t)
	}

	e := &enum{
		typ:     t,
		strings: map[string]bool{}, ints: map[int64]bool{}, uints: map[uint64]bool{},
		floats: map[float64]bool{}, others: map[any]bool{},
	}
	seen := make(map[string]bool, values.Len()) // JSON
	texts := make([]string, 0, values.Len())
	integers := true
	for i := range values.Len() {
		v := values.Index(i)
		data, err := json.Marshal(v.Interface())
		if err != nil {
			return nil, fmt.Errorf("%v.EnumValues: json/v2 can't write %v: %w", t, v, err)
		}
		value := jsontext.Value(data)
		kind := value.Kind()
		switch {
		case kind != '"' && kind != '0':
			return nil, fmt.Errorf("%v.EnumValues: %v writes %s, and the values of an enum write JSON strings or numbers", t, v, data)
		case len(e.json) > 0 && kind != e.json[0].Kind():
			return nil, fmt.Errorf("%v.EnumValues: the values write strings and numbers both, and those of an enum are all strings or all numbers", t)
		case !e.add(v):
			return nil, fmt.Errorf("%v.EnumValues has %s twice", t, data)
		case seen[string(data)]:
			return nil, fmt.Errorf("two values of %v.EnumValues write %s", t, data)
		}
		seen[string(data)] = true
		e.json = append(e.json, value)
		if kind == '"' {
			var s string
			_ = json.Unmarshal(data, &s) // a JSON string that json/v2 just wrote
			texts = append(texts, s)
		} else {
			texts = append(texts, string(data))
			integers = integers && !strings.ContainsAny(string(data), ".eE")
		}
	}
	e.detail = "must be one of: " + strings.Join(texts, ", ")
	if len(e.strings) > 0 && len(e.strings) <= few {
		e.fewStrings = slices.Collect(maps.Keys(e.strings))
	}
	if len(e.ints) > 0 && len(e.ints) <= few {
		e.fewInts = slices.Collect(maps.Keys(e.ints))
	}
	switch {
	case e.json[0].Kind() == '"':
		e.schema = stringType
	case integers:
		e.schema = integerType
	default:
		e.schema = jsonschema.Types{"number"}
	}
	return e, nil
}

// callEnumValues returns what the method EnumValues m of *t returns for a
// zero value, and fails if it panics.
func callEnumValues(m reflect.Method, t reflect.Type) (values reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v.EnumValues panicked: %v", t, r)
		}
	}()
	return m.Func.Call([]reflect.Value{reflect.New(t)})[0], nil
}
