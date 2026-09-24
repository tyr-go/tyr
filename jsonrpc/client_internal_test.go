package jsonrpc

import (
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestKindOfCode(t *testing.T) {
	// Every kind comes back from its code, but already_exists, which has
	// 409, as failed_precondition has. Known kinds have names.
	for k := tyr.Kind(0); !strings.HasPrefix(k.String(), "Kind("); k++ {
		want := k
		if k == tyr.KindAlreadyExists {
			want = tyr.KindFailedPrecondition
		}
		if got := kindOfCode(codeOf(k)); got != want {
			t.Errorf("kindOfCode(codeOf(%v)) = %v, want %v", k, got, want)
		}
	}
}
