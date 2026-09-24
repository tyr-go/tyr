package jsonschema

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
)

func TestMarshal(t *testing.T) {
	link := &Def{Name: "Link", Ref: "#/components/schemas/Link"}
	tests := []struct {
		name   string
		schema *Schema
		want   string
	}{
		{"empty", &Schema{}, `{}`},
		{"one type", &Schema{Type: Types{"string"}, MinLength: new(1)}, `{"type":"string","minLength":1}`},
		{"types", &Schema{Type: Types{"integer", "null"}}, `{"type":["integer","null"]}`},
		{"reference", &Schema{Ref: link}, `{"$ref":"#/components/schemas/Link"}`},
		{"zero counts", &Schema{Type: Types{"array"}, MaxItems: new(0)}, `{"type":"array","maxItems":0}`},
		{
			"properties in order",
			&Schema{Type: Types{"object"}, Properties: Properties{{"z", &Schema{}}, {"a", &Schema{Ref: link}}}, Required: []string{"z"}},
			`{"type":"object","properties":{"z":{},"a":{"$ref":"#/components/schemas/Link"}},"required":["z"]}`,
		},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.schema)
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: Marshal() = %s, %v; want %s", tt.name, got, err, tt.want)
		}
	}
}

func TestNullable(t *testing.T) {
	link := &Def{Name: "Link", Ref: "#/$defs/Link"}
	tests := []struct {
		name   string
		schema *Schema
		want   string
	}{
		{"type", &Schema{Type: Types{"string"}, MinLength: new(2)}, `{"type":["string","null"],"minLength":2}`},
		{"null already", &Schema{Type: Types{"string", "null"}}, `{"type":["string","null"]}`},
		{"enum", &Schema{Type: Types{"integer"}, Enum: []jsontext.Value{jsontext.Value("1")}}, `{"type":["integer","null"],"enum":[1,null]}`},
		{"reference", &Schema{Ref: link}, `{"anyOf":[{"$ref":"#/$defs/Link"},{"type":"null"}]}`},
		{"any value", &Schema{}, `{}`},
	}
	for _, tt := range tests {
		before, _ := json.Marshal(tt.schema)
		got, err := json.Marshal(Nullable(tt.schema))
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: Nullable() = %s, %v; want %s", tt.name, got, err, tt.want)
		}
		if after, _ := json.Marshal(tt.schema); string(after) != string(before) {
			t.Errorf("%s: Nullable() changed its argument to %s", tt.name, after)
		}
	}
}

func TestDescribe(t *testing.T) {
	link := &Def{Name: "Link", Ref: "#/$defs/Link"}
	tests := []struct {
		name   string
		schema *Schema
		desc   string
		want   string
	}{
		{"type", &Schema{Type: Types{"string"}}, "The code.", `{"type":"string","description":"The code."}`},
		{"reference", &Schema{Ref: link}, "The link.", `{"description":"The link.","allOf":[{"$ref":"#/$defs/Link"}]}`},
		{"no description", &Schema{Ref: link}, "", `{"$ref":"#/$defs/Link"}`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(Describe(tt.schema, tt.desc))
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: Describe() = %s, %v; want %s", tt.name, got, err, tt.want)
		}
	}
}
