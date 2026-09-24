package plan

import (
	"encoding/json/jsontext"
	"math"
	"math/big"

	"github.com/tyr-go/tyr/internal/jsonschema"
)

// form is how the values of a field look in JSON, for the keywords of its
// rules.
type form struct {
	pointer bool // the field is a pointer, and its rules apply to the value it points to
	quoted  bool // the string option: a number is written in a JSON string
}

// counted says what a comparison of a length counts in JSON.
type counted int

const (
	characters counted = iota // of a string: minLength, maxLength
	items                     // of an array: minItems, maxItems
	properties                // of an object: minProperties, maxProperties
)

// bound limits what s counts, as c says, to what op allows with n: a count
// of at least, at most, exactly, more than or fewer than n. If no count is
// allowed, nothing fits s.
func bound(s *jsonschema.Schema, c counted, op operator, n int64) {
	lo, hi := int64(0), int64(math.MaxInt64)
	switch op {
	case atLeast:
		lo = n
	case atMost:
		hi = n
	case exactly:
		lo, hi = n, n
	case above:
		if n == math.MaxInt64 {
			lo, hi = 1, 0
		} else {
			lo = n + 1
		}
	case below:
		hi = n - 1
	}
	if lo > hi || hi < 0 {
		nothing(s)
		return
	}
	var lower, upper **int
	switch c {
	case characters:
		lower, upper = &s.MinLength, &s.MaxLength
	case items:
		lower, upper = &s.MinItems, &s.MaxItems
	default:
		lower, upper = &s.MinProperties, &s.MaxProperties
	}
	if lo > 0 && (*lower == nil || int64(**lower) < lo) {
		*lower = new(int(lo))
	}
	if hi < math.MaxInt64 && (*upper == nil || int64(**upper) > hi) {
		*upper = new(int(hi))
	}
}

// limit limits the numbers of s to what op allows with p, a JSON number: at
// least, at most, exactly, more than or less than p. Of two limits of one
// kind, the stricter stays.
func limit(s *jsonschema.Schema, op operator, p jsontext.Value) {
	switch op {
	case atLeast:
		s.Minimum = pick(s.Minimum, p, 1)
	case atMost:
		s.Maximum = pick(s.Maximum, p, -1)
	case exactly:
		s.Const = p
	case above:
		s.ExclusiveMinimum = pick(s.ExclusiveMinimum, p, 1)
	case below:
		s.ExclusiveMaximum = pick(s.ExclusiveMaximum, p, -1)
	}
}

// pick returns the one of the JSON numbers cur and p that is greater, for
// sign 1, or less, for sign -1; cur may be nil.
func pick(cur, p jsontext.Value, sign int) jsontext.Value {
	if cur == nil || number(p).Cmp(number(cur)) == sign {
		return p
	}
	return cur
}

// number returns the value of the JSON number v, exactly.
func number(v jsontext.Value) *big.Rat {
	r, _ := new(big.Rat).SetString(string(v))
	return r
}

// limitFloat limits the numbers of s by op and n, a float parameter, which
// may be NaN or an infinity that JSON can't write: no number compares with
// NaN, so nothing fits, and a comparison with an infinity either holds for
// every number or for none.
func limitFloat(s *jsonschema.Schema, op operator, n float64, text string) {
	switch {
	case math.IsNaN(n):
		nothing(s)
	case math.IsInf(n, 0):
		up := n > 0
		// Whether every finite number holds against n; otherwise none does.
		every := op == atMost && up || op == below && up || op == atLeast && !up || op == above && !up
		if !every {
			nothing(s)
		}
	default:
		limit(s, op, jsontext.Value(text))
	}
}

// nothing makes s a schema that no value fits.
func nothing(s *jsonschema.Schema) {
	s.Not = &jsonschema.Schema{}
}

// quote returns the JSON string of s.
func quote(s string) jsontext.Value {
	b, _ := jsontext.AppendQuote(nil, s) // invalid UTF-8 becomes U+FFFD
	return b
}
