// Package jsonschema models the JSON Schemas that tyr writes into the
// documents of APIs: only keywords that JSON Schema Draft 7, which OpenRPC
// uses, and 2020-12, which OpenAPI 3.1 uses, share and read alike. Package
// plan builds the schemas of Go types in it.
//
// The one difference of the dialects that shows is where definitions live:
// a reference is its prefix and the name of a definition, such as
// "#/components/schemas/Link". Nothing else stands beside a reference,
// since Draft 7 ignores the keywords beside $ref: a described reference is
// wrapped in allOf, and a nullable one in anyOf.
package jsonschema

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
)

// Schema is a JSON Schema. Its fields are encoded in their order, and those
// left zero are left out, so the zero Schema is {}, which any value fits.
type Schema struct {
	Ref         *Def   `json:"$ref,omitzero"`
	Type        Types  `json:"type,omitzero"`
	Format      string `json:"format,omitzero"`
	Description string `json:"description,omitzero"`

	Enum  []jsontext.Value `json:"enum,omitzero"`
	Const jsontext.Value   `json:"const,omitzero"`

	Minimum          jsontext.Value `json:"minimum,omitzero"`
	Maximum          jsontext.Value `json:"maximum,omitzero"`
	ExclusiveMinimum jsontext.Value `json:"exclusiveMinimum,omitzero"`
	ExclusiveMaximum jsontext.Value `json:"exclusiveMaximum,omitzero"`

	MinLength       *int   `json:"minLength,omitzero"`
	MaxLength       *int   `json:"maxLength,omitzero"`
	Pattern         string `json:"pattern,omitzero"`
	ContentEncoding string `json:"contentEncoding,omitzero"`

	Items    *Schema `json:"items,omitzero"`
	MinItems *int    `json:"minItems,omitzero"`
	MaxItems *int    `json:"maxItems,omitzero"`

	Properties           Properties `json:"properties,omitzero"`
	Required             []string   `json:"required,omitzero"`
	AdditionalProperties *Schema    `json:"additionalProperties,omitzero"`
	MinProperties        *int       `json:"minProperties,omitzero"`
	MaxProperties        *int       `json:"maxProperties,omitzero"`

	Not   *Schema   `json:"not,omitzero"`
	AllOf []*Schema `json:"allOf,omitzero"`
	AnyOf []*Schema `json:"anyOf,omitzero"`
}

// Def is a schema defined once, by name, and referenced from others.
type Def struct {
	Name   string // in the definitions, such as "Link"
	Ref    string // that references it, such as "#/components/schemas/Link"
	Schema *Schema
}

// MarshalJSONTo writes the reference to d, for the $ref of a schema.
func (d *Def) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String(d.Ref))
}

// Types is the type keyword: one name of a JSON type, written as a string,
// or several, written as an array.
type Types []string

// MarshalJSONTo writes t as a string if it has one name, or as an array.
func (t Types) MarshalJSONTo(enc *jsontext.Encoder) error {
	if len(t) == 1 {
		return enc.WriteToken(jsontext.String(t[0]))
	}
	return json.MarshalEncode(enc, []string(t))
}

// Property is a property of an object.
type Property struct {
	Name   string
	Schema *Schema
}

// Properties are the properties of an object, written in their order.
type Properties []Property

// MarshalJSONTo writes p as an object, in the order of the properties.
func (p Properties) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, prop := range p {
		if err := enc.WriteToken(jsontext.String(prop.Name)); err != nil {
			return err
		}
		if err := json.MarshalEncode(enc, prop.Schema); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// Nullable returns the schema of the values of s and null.
func Nullable(s *Schema) *Schema {
	switch {
	case s.Ref != nil:
		return &Schema{AnyOf: []*Schema{s, {Type: Types{"null"}}}}
	case len(s.Type) > 0:
		if slices.Contains(s.Type, "null") {
			return s
		}
		c := *s
		c.Type = append(Types{}, s.Type...)
		c.Type = append(c.Type, "null")
		if len(c.Enum) > 0 {
			c.Enum = append(append([]jsontext.Value{}, c.Enum...), jsontext.Value("null"))
		}
		return &c
	}
	return s // any value, null too
}

// Describe returns s with the description d. A reference gets wrapped in
// allOf, as nothing may stand beside it.
func Describe(s *Schema, d string) *Schema {
	if d == "" {
		return s
	}
	if s.Ref != nil {
		return &Schema{AllOf: []*Schema{s}, Description: d}
	}
	c := *s
	c.Description = d
	return &c
}
