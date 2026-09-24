package contract_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/examples/shortlink/contract"
)

func TestCreateReqValidate(t *testing.T) {
	invalid := tyr.Violations{{Pointer: "/code", Detail: "only a-z, 0-9 and '-'"}}
	tests := []struct {
		code string
		want tyr.Violations // nil if the code is valid
	}{
		{"", nil}, // a random code
		{"go-docs", nil},
		{"a1-b2", nil},
		{"Go_Dev", invalid},
		{"go docs", invalid},
		{"ссылка", invalid},
	}
	for _, tt := range tests {
		err := contract.CreateReq{URL: "https://go.dev", Code: tt.code}.Validate()
		var got tyr.Violations
		if e, ok := errors.AsType[*tyr.Error](err); ok {
			got, _ = e.Details.(tyr.Violations)
		}
		if (err == nil) != (tt.want == nil) || !slices.Equal(got, tt.want) {
			t.Errorf("Validate() of code %q = %v, want %v", tt.code, err, tt.want)
		}
	}
}
