package schematest

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/tyr-go/tyr/internal/jsonreq"
	"github.com/tyr-go/tyr/internal/plan"
)

// decoded has a field of each type whose JSON a schema can't tell apart
// from what json/v2 rejects.
type decoded struct {
	I      int            `json:"i"`
	I64    int64          `json:"i64"`
	U64    uint64         `json:"u64"`
	Quoted int8           `json:"quoted,string"`
	F32    float32        `json:"f32"`
	At     time.Time      `json:"at"`
	Bytes  []byte         `json:"bytes"`
	IntMap map[int]string `json:"int_map"`
	Addr   netip.Addr     `json:"addr"`
	S      string         `json:"s"`
}

// TestDecodeGaps holds the JSON that fits the input schema of its type but
// that the server doesn't decode, as the Limitations of README list it: a
// JSON Schema tells what values are, not how they are written. A case that
// starts to fail has left the list, and README must follow.
func TestDecodeGaps(t *testing.T) {
	tests := []struct {
		name, data, why string
	}{
		{"integer as 1.0", `{"i":1.0}`, "JSON Schema takes 1.0 for an integer"},
		{"integer as 1e2", `{"i":1e2}`, "JSON Schema takes 1e2 for an integer"},
		{"int64 beyond its range", `{"i64":9223372036854775808}`, "the schemas bound integers of 8, 16 and 32 bits only"},
		{"uint64 beyond its range", `{"u64":18446744073709551616}`, "the schemas bound integers of 8, 16 and 32 bits only"},
		{"quoted int8 beyond its range", `{"quoted":"300"}`, "the pattern of the string option has no range"},
		{"float32 beyond its range", `{"f32":1e39}`, "the schema of a float has no range"},
		{"lowercase time", `{"at":"2026-09-24t12:00:00z"}`, "RFC 3339 allows lowercase t and z, time.Parse doesn't"},
		{"leap second", `{"at":"2016-12-31T23:59:60Z"}`, "RFC 3339 allows the second 60, time.Parse doesn't"},
		{"bytes not in base64", `{"bytes":"!!!"}`, "contentEncoding is an annotation"},
		{"key of a map of ints", `{"int_map":{"abc":"x"}}`, "the schema of a map doesn't constrain its keys"},
		{"string a type rejects", `{"addr":"not-an-address"}`, "the schema of a type that parses itself is any string"},
		{"name given twice", `{"s":"a","s":"b"}`, "a validator sees one member"},
		{"lone surrogate", `{"s":"\ud800"}`, "a validator takes an escape of a lone surrogate"},
	}
	typ := reflect.TypeFor[decoded]()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, d := range dialects {
				if got := failures(t, compile(t, typ, plan.Input, d), []byte(tt.data)); len(got) > 0 {
					t.Errorf("%s: %s fails the schema at %q, so it has left the list: %s", d.name, tt.data, got, tt.why)
				}
			}
			if err := jsonreq.Unmarshal([]byte(tt.data), new(decoded)); err == nil {
				t.Errorf("the server decodes %s, so it has left the list: %s", tt.data, tt.why)
			}
		})
	}
}
