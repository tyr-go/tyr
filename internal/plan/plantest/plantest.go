// Package plantest holds the request types and values that tests check the
// plans of package plan against: the field layouts against what
// encoding/json/v2 writes and reads, and the validate tags against
// go-playground/validator, in the tests of the core here and of the
// validate/playground module.
package plantest

import (
	"encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"time"
)

// Layouts returns zero values of struct types whose fields json/v2 lays out
// in each way the plans must follow: flat, embedded, embedded through a
// pointer or with a JSON name, nested, hidden by a shallower field, tied at
// one depth, promoted with the embed option, and embedded from an
// unexported type.
func Layouts() []any {
	return []any{
		Flat{hidden: ""},
		Embedded{},
		EmbeddedPointer{},
		EmbeddedNamed{},
		Nested{},
		Shadowed{},
		TaggedWins{},
		Conflicting{},
		EmbedOption{},
		UnexportedEmbedded{},
		Deep{},
		Fallback{},
	}
}

// Fallback keeps unknown members in a field with the embed option, which
// isn't a member itself.
type Fallback struct {
	Own  string         `json:"own"`
	Rest map[string]any `json:",embed"` //nolint:staticcheck // SA5008 doesn't know the embed option of json/v2
}

// Base is embedded in layouts.
type Base struct {
	ID   string `json:"id"`
	Name string // named after the field
}

// Inner is nested in layouts.
type Inner struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// Flat has no embedded or nested structs.
type Flat struct {
	A       string `json:"a"`
	B       int    `json:"b"`
	C       string // named after the field
	Ignored string `json:"-"`
	hidden  string
}

// Embedded embeds a struct, whose fields JSON promotes.
type Embedded struct {
	Base
	Own string `json:"own"`
}

// EmbeddedPointer embeds a struct through a pointer.
type EmbeddedPointer struct {
	*Base
	Own string `json:"own"`
}

// EmbeddedNamed embeds a struct with a JSON name, which makes it a member.
type EmbeddedNamed struct {
	Base `json:"base"`
	Own  string `json:"own"`
}

// Nested has struct fields, which are members of their own.
type Nested struct {
	Inner Inner  `json:"inner"`
	Ptr   *Inner `json:"ptr"`
}

// Shadowed has a field that hides a field of its embedded struct.
type Shadowed struct {
	Base
	ID string `json:"id"`
}

// TaggedA has a field with a JSON name in its tag.
type TaggedA struct {
	Name string `json:"Name"`
}

// TaggedB has a field of the same JSON name without a tag.
type TaggedB struct {
	Name string
}

// TaggedWins embeds two fields of one name at one depth: the tagged wins.
type TaggedWins struct {
	TaggedA
	TaggedB
}

// ConflictA has a field without a tag.
type ConflictA struct {
	Value string
}

// ConflictB has a field of the same name without a tag.
type ConflictB struct {
	Value string
}

// Conflicting embeds two untagged fields of one name at one depth: neither
// is a member.
type Conflicting struct {
	ConflictA
	ConflictB
	Own string `json:"own"`
}

// EmbedOption promotes the members of a field with the embed option.
type EmbedOption struct {
	Opts Inner  `json:",embed"` //nolint:staticcheck // SA5008 doesn't know the embed option of json/v2
	Own  string `json:"own"`
}

// unexportedBase is an unexported type with exported fields.
type unexportedBase struct {
	Key string `json:"key"`
}

// UnexportedEmbedded embeds a struct of an unexported type.
type UnexportedEmbedded struct {
	unexportedBase
	Own string `json:"own"`
}

// Middle embeds Base and is embedded in Deep.
type Middle struct {
	Base
	Mid string `json:"mid"`
}

// Deep embeds structs two levels down.
type Deep struct {
	Middle
	Own string `json:"own"`
}

// Violation is a violation of a validate tag as the core reports it.
type Violation struct {
	Pointer string
	Detail  string
}

// Check is a value and the violations of its validate tags.
type Check struct {
	Name  string
	Value any
	Want  []Violation
}

// Strings has rules for strings.
type Strings struct {
	Required string `json:"required" validate:"required"`
	Min      string `json:"min" validate:"min=2"`
	Max      string `json:"max" validate:"max=3"`
	Len      string `json:"len" validate:"len=2"`
	Runes    string `json:"runes" validate:"max=2"`
	OneOf    string `json:"one_of" validate:"oneof=red green 'light blue'"`
	Email    string `json:"email" validate:"omitempty,email"`
	URL      string `json:"url" validate:"omitempty,url"`
	UUID     string `json:"uuid" validate:"omitempty,uuid"`
}

// Numbers has rules for numbers.
type Numbers struct {
	Required int           `json:"required" validate:"required"`
	Gt       int           `json:"gt" validate:"gt=0"`
	Gte      int8          `json:"gte" validate:"gte=-1"`
	Lt       uint          `json:"lt" validate:"lt=10"`
	Lte      float64       `json:"lte" validate:"lte=1.5"`
	Hex      int           `json:"hex" validate:"max=0x10"`
	OneOf    int           `json:"one_of" validate:"oneof=1 2 3"`
	Timeout  time.Duration `json:"timeout" validate:"min=1s"`
}

// Floats has rules for floats. As go-playground compares with the
// operators of Go, NaN fails every comparison, and an infinity fails on the
// side it goes past.
type Floats struct {
	Min   float64  `json:"min" validate:"min=0"`
	Max   float64  `json:"max" validate:"max=10"`
	Len   float64  `json:"len" validate:"len=1"`
	Gt    float64  `json:"gt" validate:"gt=0"`
	Lt    float64  `json:"lt" validate:"lt=10"`
	Range float32  `json:"range" validate:"min=0,max=10"`
	Ptr   *float64 `json:"ptr" validate:"omitempty,gte=0,lte=10"`
}

// Collections has rules for slices, maps and arrays.
type Collections struct {
	Tags  []string          `json:"tags" validate:"min=1,max=2"`
	Attrs map[string]string `json:"attrs" validate:"required"`
	Pair  [2]int            `json:"pair" validate:"len=2"`
	Empty []string          `json:"empty" validate:"omitempty,min=2"`
}

// Pointers has rules for pointers.
type Pointers struct {
	Required *int    `json:"required" validate:"required"`
	Omit     *int    `json:"omit" validate:"omitempty,min=5"`
	First    *string `json:"first" validate:"min=2"`
	Zero     *int    `json:"zero" validate:"required"`
}

// Profile is nested in Nesting.
type Profile struct {
	Color string `json:"color" validate:"required,oneof=red green"`
}

// Paging is embedded in Nesting.
type Paging struct {
	Limit int `json:"limit" validate:"max=100"`
}

// Nesting has nested and embedded structs.
type Nesting struct {
	Profile  Profile   `json:"profile"`
	Ptr      *Profile  `json:"ptr"`
	Required Profile   `json:"required" validate:"required"`
	Omitted  *Profile  `json:"omitted" validate:"omitempty"`
	Items    []Profile `json:"items"`
	Paging
}

// Times has rules for times.
type Times struct {
	At  time.Time  `json:"at" validate:"required"`
	Opt *time.Time `json:"opt" validate:"omitempty"`
}

// Order has fields out of alphabetical order.
type Order struct {
	B string `json:"b" validate:"required"`
	A string `json:"a" validate:"required"`
}

// Skipped has fields that JSON or validation leaves out.
type Skipped struct {
	Ignored string `json:"-" validate:"required"`
	hidden  string `validate:"required"`
	Dash    string `json:"dash" validate:"-"`
}

// Formats has more values for the rules of formats and oneof.
type Formats struct {
	Email    string `json:"email" validate:"email"`
	File     string `json:"file" validate:"url"`
	Opaque   string `json:"opaque" validate:"url"`
	Fragment string `json:"fragment" validate:"url"`
	Upper    string `json:"upper" validate:"url"`
	UUID     string `json:"uuid" validate:"uuid"`
	UUIDHex  string `json:"uuid_hex" validate:"uuid"`
	Level    uint8  `json:"level" validate:"oneof=1 2"`
	Web      string `json:"web" validate:"http_url"`
	WebUpper string `json:"web_upper" validate:"http_url"`
	WebPort  string `json:"web_port" validate:"http_url"`
	WebBare  string `json:"web_bare" validate:"http_url"`
}

// Level is an int that JSON carries as its name, by its methods of text,
// as a type of an enum often is.
type Level int

// levelNames are the names of the levels, by their values.
var levelNames = []string{"debug", "info", "warn"}

// MarshalText returns the name of l.
func (l Level) MarshalText() ([]byte, error) {
	if l < 0 || int(l) >= len(levelNames) {
		return nil, fmt.Errorf("level %d has no name", int(l))
	}
	return []byte(levelNames[l]), nil
}

// UnmarshalText sets l to the level that text names.
func (l *Level) UnmarshalText(text []byte) error {
	i := slices.Index(levelNames, string(text))
	if i < 0 {
		return fmt.Errorf("unknown level %q", text)
	}
	*l = Level(i)
	return nil
}

// Cents is an amount of money that JSON carries as a string of its units
// and cents, such as "1.50", by its methods of JSON.
type Cents int64

// MarshalJSON writes c as a string of its units and cents.
func (c Cents) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, `"%d.%02d"`, c/100, c%100), nil
}

// UnmarshalJSON reads c from a string of its units and cents.
func (c *Cents) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	var units, cents int64
	if _, err := fmt.Sscanf(s, "%d.%02d", &units, &cents); err != nil {
		return fmt.Errorf("want an amount such as 1.50: %w", err)
	}
	*c = Cents(units*100 + cents)
	return nil
}

// Methods has rules on types that JSON carries by methods of their own:
// the rules check the Go values, of which JSON Schema sees only the JSON.
type Methods struct {
	Level Level `json:"level" validate:"oneof=1 2"`
	Cents Cents `json:"cents" validate:"min=100"`
}

// Checks returns values with valid and invalid fields and the violations
// the core reports for them: for each field, the first rule it fails, in
// the order of the fields.
func Checks() []Check {
	nan, inf := math.NaN(), math.Inf(1)
	return []Check{
		{
			Name: "valid strings",
			Value: Strings{
				Required: "x", Min: "ab", Max: "abc", Len: "ab", Runes: "ёж",
				OneOf: "light blue", Email: "ann@go.dev", URL: "https://go.dev",
				UUID: "F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6",
			},
		},
		{
			Name: "invalid strings",
			Value: Strings{
				Min: "a", Max: "abcd", Len: "a", Runes: "ёжик", OneOf: "blue",
				Email: "Ann <ann@go.dev>", URL: "go.dev", UUID: "f81d4fae7dec11d0a76500a0c91e6bf6",
			},
			Want: []Violation{
				{"/required", "is required"},
				{"/min", "must be at least 2 characters"},
				{"/max", "must be at most 3 characters"},
				{"/len", "must be exactly 2 characters"},
				{"/runes", "must be at most 2 characters"},
				{"/one_of", "must be one of: red, green, light blue"},
				{"/email", "must be an email address"},
				{"/url", "must be a URL"},
				{"/uuid", "must be a UUID"},
			},
		},
		{
			Name: "valid formats",
			Value: Formats{
				Email: "ann@go.dev", File: "file:///tmp/x", Opaque: "mailto:ann@go.dev",
				Fragment: "x:/#top", Upper: "HTTPS://GO.DEV",
				UUID: "f81d4fae-7dec-11d0-a765-00a0c91e6bf6", UUIDHex: "F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6",
				Level: 2, Web: "https://go.dev", WebUpper: "HTTP://GO.DEV/doc", WebPort: "http://127.0.0.1:8080",
				WebBare: "https://go.dev/doc/",
			},
		},
		{
			Name: "invalid formats",
			Value: Formats{
				Email: "no-at-sign", File: "file:///", Opaque: "mailto:", Fragment: "x:/",
				UUID: "f81d4fae_7dec-11d0-a765-00a0c91e6bf6", UUIDHex: "f81d4fae-7dec-11d0-a765-00a0c91e6bfg",
				Level: 3, Web: "javascript:alert(1)", WebUpper: "HTTP:go.dev", WebPort: "ftp://go.dev",
				WebBare: "go.dev",
			},
			Want: []Violation{
				{"/email", "must be an email address"},
				{"/file", "must be a URL"},
				{"/opaque", "must be a URL"},
				{"/fragment", "must be a URL"},
				{"/upper", "must be a URL"},
				{"/uuid", "must be a UUID"},
				{"/uuid_hex", "must be a UUID"},
				{"/level", "must be one of: 1, 2"},
				{"/web", "must be an http or https URL"},
				{"/web_upper", "must be an http or https URL"},
				{"/web_port", "must be an http or https URL"},
				{"/web_bare", "must be an http or https URL"},
			},
		},
		{
			Name:  "valid numbers",
			Value: Numbers{Required: 1, Gt: 1, Gte: -1, Lt: 9, Lte: 1.5, Hex: 16, OneOf: 3, Timeout: time.Second},
		},
		{
			Name:  "invalid numbers",
			Value: Numbers{Gte: -2, Lt: 10, Lte: 2, Hex: 17, OneOf: 4, Timeout: time.Second / 2},
			Want: []Violation{
				{"/required", "is required"},
				{"/gt", "must be greater than 0"},
				{"/gte", "must be at least -1"},
				{"/lt", "must be less than 10"},
				{"/lte", "must be at most 1.5"},
				{"/hex", "must be at most 16"},
				{"/one_of", "must be one of: 1, 2, 3"},
				{"/timeout", "must be at least 1s"},
			},
		},
		{
			Name:  "valid floats",
			Value: Floats{Min: 0, Max: 10, Len: 1, Gt: 0.5, Lt: 9.5, Range: 5, Ptr: new(10.0)},
		},
		{
			Name:  "NaN",
			Value: Floats{Min: nan, Max: nan, Len: nan, Gt: nan, Lt: nan, Range: float32(nan), Ptr: new(nan)},
			Want: []Violation{
				{"/min", "must be at least 0"},
				{"/max", "must be at most 10"},
				{"/len", "must be 1"},
				{"/gt", "must be greater than 0"},
				{"/lt", "must be less than 10"},
				{"/range", "must be at least 0"},
				{"/ptr", "must be at least 0"},
			},
		},
		{
			Name:  "infinity",
			Value: Floats{Min: inf, Max: inf, Len: inf, Gt: inf, Lt: inf, Range: float32(inf), Ptr: new(inf)},
			Want: []Violation{
				{"/max", "must be at most 10"},
				{"/len", "must be 1"},
				{"/lt", "must be less than 10"},
				{"/range", "must be at most 10"},
				{"/ptr", "must be at most 10"},
			},
		},
		{
			Name:  "negative infinity",
			Value: Floats{Min: -inf, Max: -inf, Len: -inf, Gt: -inf, Lt: -inf, Range: float32(-inf), Ptr: new(-inf)},
			Want: []Violation{
				{"/min", "must be at least 0"},
				{"/len", "must be 1"},
				{"/gt", "must be greater than 0"},
				{"/range", "must be at least 0"},
				{"/ptr", "must be at least 0"},
			},
		},
		{
			Name:  "valid collections",
			Value: Collections{Tags: []string{"a"}, Attrs: map[string]string{}},
		},
		{
			// An empty slice that isn't nil has a value: omitempty doesn't
			// skip it.
			Name:  "invalid collections",
			Value: Collections{Empty: []string{}},
			Want: []Violation{
				{"/tags", "must have at least 1 item"},
				{"/attrs", "is required"},
				{"/empty", "must have at least 2 items"},
			},
		},
		{
			// A nil pointer answers to its first rule; a pointer to zero
			// has a value.
			Name:  "pointers",
			Value: Pointers{Omit: new(3), Zero: new(0)},
			Want: []Violation{
				{"/required", "is required"},
				{"/omit", "must be at least 5"},
				{"/first", "must be at least 2 characters"},
			},
		},
		{
			// A zero struct fails required and isn't checked further;
			// slices of structs aren't checked without dive.
			Name: "nesting",
			Value: Nesting{
				Profile: Profile{Color: "blue"},
				Items:   []Profile{{}},
				Paging:  Paging{Limit: 101},
			},
			Want: []Violation{
				{"/profile/color", "must be one of: red, green"},
				{"/required", "is required"},
				{"/limit", "must be at most 100"},
			},
		},
		{
			Name:  "nested pointer",
			Value: Nesting{Ptr: &Profile{}, Required: Profile{Color: "red"}, Omitted: &Profile{Color: "red"}},
			Want: []Violation{
				{"/profile/color", "is required"},
				{"/ptr/color", "is required"},
			},
		},
		{
			Name:  "times",
			Value: Times{},
			Want:  []Violation{{"/at", "is required"}},
		},
		{
			Name:  "order of the fields",
			Value: Order{},
			Want:  []Violation{{"/b", "is required"}, {"/a", "is required"}},
		},
		{
			// A field JSON leaves out is still checked, under its Go name.
			Name:  "skipped fields",
			Value: Skipped{hidden: ""},
			Want:  []Violation{{"/Ignored", "is required"}},
		},
		{
			// The rules check the values, whatever their JSON: "info" is
			// the level 1, and "1.50" 150 cents.
			Name:  "valid methods",
			Value: Methods{Level: 1, Cents: 150},
		},
		{
			Name:  "invalid methods",
			Value: Methods{Level: 0, Cents: 50},
			Want: []Violation{
				{"/level", "must be one of: 1, 2"},
				{"/cents", "must be at least 100"},
			},
		},
	}
}
