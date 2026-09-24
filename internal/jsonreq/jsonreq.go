// Package jsonreq decodes the JSON of requests for the transports. REST
// decodes a body and JSON-RPC its params into the same Req, and a value
// that doesn't fit gets the same violation over both.
package jsonreq

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"reflect"
	"strings"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/plan"
)

// IsJSON reports whether contentType is a JSON media type: application/json
// or a type with the +json suffix, with any parameters.
func IsJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// Unmarshal decodes data into dst, a *Req. An error becomes a [tyr.Error]
// of [tyr.KindInvalidArgument] with a [tyr.Violation]: at the value that
// doesn't fit its field, or at the whole document, with the byte offset,
// if the JSON is broken.
func Unmarshal(data []byte, dst any) error {
	if err := json.Unmarshal(data, dst); err != nil {
		return violation(err)
	}
	return nil
}

// violation turns an error of json.Unmarshal into a violation, as described
// at Unmarshal.
func violation(err error) error {
	v := tyr.Violations{{Detail: "invalid JSON"}}
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		v[0] = tyr.Violation{Pointer: string(se.JSONPointer), Detail: describe(se)}
	} else if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		v[0].Detail = fmt.Sprintf("invalid JSON at byte offset %d: %v", se.ByteOffset, se.Err)
	}
	return v.Err()
}

// describe says what the value at a semantic error must be. A type that
// unmarshals itself speaks for itself, in the error its method returned.
func describe(se *json.SemanticError) string {
	t := se.GoType
	if se.Err != nil && t != nil && t != reflect.TypeFor[time.Time]() && ranOwnMethod(t, se.JSONKind) {
		return se.Err.Error()
	}
	return plan.Describe(t)
}

// ranOwnMethod reports whether decoding a JSON value of kind k into type t
// calls a method of t: UnmarshalJSON for any value, UnmarshalText only for
// a string.
func ranOwnMethod(t reflect.Type, k jsontext.Kind) bool {
	p := reflect.PointerTo(t)
	if p.Implements(reflect.TypeFor[json.Unmarshaler]()) || p.Implements(reflect.TypeFor[json.UnmarshalerFrom]()) {
		return true
	}
	return k == '"' && p.Implements(reflect.TypeFor[encoding.TextUnmarshaler]())
}
