package reqid_test

import (
	"strings"
	"testing"

	"github.com/tyr-go/tyr/internal/reqid"
)

func TestValid(t *testing.T) {
	allowed := "ABCXYZabcxyz0189._:-"
	long := strings.Repeat(allowed, 7)[:128]
	tests := []struct {
		id   string
		want bool
	}{
		{allowed, true},
		{"a", true},
		{long, true},
		{"0192f5e2-7c3a-7b1e-9c4d-2f1a3b5c7d9e", true}, // a UUIDv7, as RequestID makes
		{"", false},
		{long + "a", false},
		{"a b", false},
		{"a, b", false}, // merged by a proxy
		{"a/b", false},
		{`a"b`, false},
		{"a\nb", false},
		{"é", false},
	}
	for _, tt := range tests {
		if got := reqid.Valid(tt.id); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
