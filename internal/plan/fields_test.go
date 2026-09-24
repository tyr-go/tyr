package plan

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/tyr-go/tyr/internal/plan/plantest"
)

// leaf is a JSON value of a string or a number and the field it comes from.
type leaf struct {
	pointer string
	index   []int
	typ     reflect.Type
}

// leaves returns the leaves of the JSON of the struct type t by the plan:
// its members, and the members of nested structs, recursively.
func leaves(t *testing.T, typ reflect.Type, prefix string, index []int) []leaf {
	t.Helper()
	ms, err := members(typ)
	if err != nil {
		t.Fatalf("members(%v) error = %v", typ, err)
	}
	var out []leaf
	for _, m := range ms {
		p := pointer(prefix, m.name)
		i := slices.Concat(index, m.index)
		ft := m.field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, leaves(t, ft, p, i)...)
			continue
		}
		out = append(out, leaf{pointer: p, index: i, typ: m.field.Type})
	}
	return out
}

// fill sets every field of v it can set, through embedded and nested
// structs and pointers, to a value that no other field has.
func fill(v reflect.Value, n *int) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n)
	case reflect.Struct:
		for i := range v.NumField() {
			sf := v.Type().Field(i)
			if sf.IsExported() || sf.Anonymous {
				fill(v.Field(i), n)
			}
		}
	case reflect.String:
		*n++
		v.SetString("v" + strconv.Itoa(*n))
	case reflect.Int:
		*n++
		v.SetInt(int64(*n))
	}
}

// jsonLeaves returns the strings and numbers in a JSON value by their
// pointers, as text.
func jsonLeaves(v any, prefix string, out map[string]string) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			jsonLeaves(x, pointer(prefix, k), out)
		}
	case string:
		out[prefix] = v
	case float64:
		out[prefix] = strconv.FormatFloat(v, 'f', -1, 64)
	}
}

func TestMembersWrite(t *testing.T) {
	for _, layout := range plantest.Layouts() {
		typ := reflect.TypeOf(layout)
		t.Run(typ.Name(), func(t *testing.T) {
			v := reflect.New(typ).Elem()
			fill(v, new(int))
			data, err := json.Marshal(v.Interface())
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			var decoded any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			written := make(map[string]string)
			jsonLeaves(decoded, "", written)

			// The plan has the leaves json/v2 writes, with the values of
			// the fields it points them to.
			planned := make(map[string]string)
			for _, l := range leaves(t, typ, "", nil) {
				f, ok := fieldByIndex(v, l.index, false)
				if !ok {
					t.Fatalf("no field at %s", l.pointer)
				}
				planned[l.pointer] = fmt.Sprint(f.Interface())
			}
			if !maps.Equal(planned, written) {
				t.Errorf("plan:\n%v\njson/v2 writes %s:\n%v", planned, data, written)
			}
		})
	}
}

func TestMembersRead(t *testing.T) {
	for _, layout := range plantest.Layouts() {
		typ := reflect.TypeOf(layout)
		t.Run(typ.Name(), func(t *testing.T) {
			// Put a value that no other leaf has at every pointer of the plan.
			ls := leaves(t, typ, "", nil)
			object := make(map[string]any)
			want := make(map[string]string)
			for i, l := range ls {
				var value any = "r" + strconv.Itoa(i)
				if l.typ.Kind() == reflect.Int {
					value = i
				}
				want[l.pointer] = fmt.Sprint(value)
				put(object, jsontext.Pointer(l.pointer), value)
			}
			data, err := json.Marshal(object)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			v := reflect.New(typ)
			if err := json.Unmarshal(data, v.Interface()); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", data, err)
			}

			// json/v2 reads every value into the field the plan says.
			for _, l := range ls {
				f, ok := fieldByIndex(v.Elem(), l.index, false)
				if !ok {
					t.Errorf("json/v2 left the way to %s nil reading %s", l.pointer, data)
					continue
				}
				if got := fmt.Sprint(f.Interface()); got != want[l.pointer] {
					t.Errorf("field at %s = %s after reading %s, want %s", l.pointer, got, data, want[l.pointer])
				}
			}
		})
	}
}

func TestFieldByIndex(t *testing.T) {
	v := reflect.ValueOf(&plantest.EmbeddedPointer{}).Elem()
	index := []int{0, 0} // Base.ID, through the nil *Base
	if _, ok := fieldByIndex(v, index, false); ok {
		t.Error("fieldByIndex() without alloc went through a nil pointer")
	}
	f, ok := fieldByIndex(v, index, true)
	if !ok || v.Field(0).IsNil() {
		t.Fatal("fieldByIndex() with alloc didn't allocate the embedded struct")
	}
	f.SetString("id")
	if got := v.Interface().(plantest.EmbeddedPointer).ID; got != "id" {
		t.Errorf("the field got %q, want id", got)
	}
}

// put sets value in object at p, making the objects on the way.
func put(object map[string]any, p jsontext.Pointer, value any) {
	tokens := slices.Collect(p.Tokens())
	for _, tok := range tokens[:len(tokens)-1] {
		next, ok := object[tok].(map[string]any)
		if !ok {
			next = make(map[string]any)
			object[tok] = next
		}
		object = next
	}
	object[tokens[len(tokens)-1]] = value
}

// Layouts json/v2 refuses, and members refuses too.
type (
	duplicateNames struct {
		A string `json:"B"` // B's name
		B string
	}
	names         []string
	embeddedSlice struct{ names }
	textID        struct{ ID string }
	embedMethods  struct {
		T textID `json:",embed"` //nolint:staticcheck // SA5008 doesn't know the embed option of json/v2
	}
	quotedName struct {
		A string `json:"'a,b'"` // json/v2 in Go 1.27 takes no quoted names
	}
)

func (t textID) MarshalText() ([]byte, error) { return []byte(t.ID), nil }

func TestMembersErrors(t *testing.T) {
	for _, v := range []any{duplicateNames{}, embeddedSlice{names: names{"a"}}, embedMethods{}, quotedName{}} {
		typ := reflect.TypeOf(v)
		_, jsonErr := json.Marshal(v)
		_, err := members(typ)
		if jsonErr == nil || err == nil {
			t.Errorf("%v: json.Marshal() error = %v, members() error = %v; want both to fail", typ, jsonErr, err)
		}
	}
}

// promoted embeds a type with JSON methods, which Go promotes to it.
type promoted struct {
	textID
	Own string `json:"own"`
}

func TestMembersOfTypeWithMethods(t *testing.T) {
	// json/v2 uses the promoted method, not the fields: they aren't members.
	data, err := json.Marshal(promoted{textID{ID: "id"}, "own"})
	if err != nil || string(data) != `"id"` {
		t.Fatalf("json.Marshal() = %s, %v; want \"id\"", data, err)
	}
	if _, err := members(reflect.TypeFor[promoted]()); err == nil {
		t.Error("members() error = <nil>, want one for a type with JSON methods")
	}
}
