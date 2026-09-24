package jsonreq_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonreq"
)

func TestIsJSON(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"APPLICATION/JSON", true},
		{"application/merge-patch+json", true},
		{"application/json; charset", true}, // a broken parameter only
		{"text/plain", false},
		{"", false},
		{"application/", false},
		{"application/jsonl", false},
	}
	for _, tt := range tests {
		if got := jsonreq.IsJSON(tt.contentType); got != tt.want {
			t.Errorf("IsJSON(%q) = %t, want %t", tt.contentType, got, tt.want)
		}
	}
}

// code is a type that parses itself from text.
type code string

func (c *code) UnmarshalText(b []byte) error {
	if len(b) < 2 {
		return errors.New("must be at least 2 characters")
	}
	*c = code(b)
	return nil
}

// level is a type that decodes JSON itself.
type level string

func (l *level) UnmarshalJSON(b []byte) error {
	if s := string(b); s != `"low"` && s != `"high"` {
		return errors.New("must be low or high")
	}
	*l = level(strings.Trim(string(b), `"`))
	return nil
}

type thing struct {
	Code  code      `json:"code"`
	Level level     `json:"level"`
	Since time.Time `json:"since"`
	Items []struct {
		Count int `json:"count"`
	} `json:"items"`
}

func TestUnmarshal(t *testing.T) {
	var got thing
	if err := jsonreq.Unmarshal([]byte(`{"code": "go", "level": "low"}`), &got); err != nil || got.Code != "go" || got.Level != "low" {
		t.Errorf("Unmarshal() = %+v, %v; want code go and level low", got, err)
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
		want tyr.Violation
	}{
		{"broken JSON", `{"code": "go",}`, tyr.Violation{Detail: "invalid JSON at byte offset 13: invalid character ',' at start of value"}},
		{"value of a wrong type", `{"items": [{"count": "many"}]}`, tyr.Violation{Pointer: "/items/0/count", Detail: "must be an integer"}},
		{"array for an object", `[1, 2]`, tyr.Violation{Detail: "must be an object"}},
		// time.Time parses itself, but its errors name Go layouts.
		{"time", `{"since": "yesterday"}`, tyr.Violation{Pointer: "/since", Detail: "must be an RFC 3339 time"}},
		{"type that parses itself", `{"code": "x"}`, tyr.Violation{Pointer: "/code", Detail: "must be at least 2 characters"}},
		{"number for a type that parses strings", `{"code": 5}`, tyr.Violation{Pointer: "/code", Detail: "must be a string"}},
		{"type that decodes JSON itself", `{"level": "extreme"}`, tyr.Violation{Pointer: "/level", Detail: "must be low or high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := jsonreq.Unmarshal([]byte(tt.data), new(thing))
			e, ok := err.(*tyr.Error)
			if !ok || e.Kind != tyr.KindInvalidArgument {
				t.Fatalf("Unmarshal() error = %v, want invalid_argument", err)
			}
			if v, _ := e.Details.(tyr.Violations); !slices.Equal(v, tyr.Violations{tt.want}) {
				t.Errorf("violations = %+v, want %+v", e.Details, tt.want)
			}
		})
	}
}
