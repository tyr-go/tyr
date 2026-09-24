package tyr_test

import (
	"testing"

	"github.com/tyr-go/tyr"
)

func TestDefine(t *testing.T) {
	if got := tyr.Define[getLinkReq, *link]("links.get").Name(); got != "links.get" {
		t.Errorf("Name() = %q, want %q", got, "links.get")
	}
	var zero tyr.Contract[getLinkReq, *link]
	if got := zero.Name(); got != "" {
		t.Errorf("Name() of the zero Contract = %q, want %q", got, "")
	}
}

func TestDefinePanics(t *testing.T) {
	tests := []struct {
		name   string
		define func()
		want   string
	}{
		{
			name:   "invalid name",
			define: func() { tyr.Define[getLinkReq, *link]("links..get") },
			want:   `tyr: Define("links..get"): name must be dot-separated segments of ASCII letters, digits, '_' and '-'`,
		},
		{
			name:   "reserved name",
			define: func() { tyr.Define[getLinkReq, *link]("rpc.discover") },
			want:   `tyr: Define("rpc.discover"): the first segment "rpc" is reserved by JSON-RPC`,
		},
		{
			name:   "pointer request",
			define: func() { tyr.Define[*getLinkReq, *link]("links.get") },
			want:   `tyr: Define("links.get"): request type *tyr_test.getLinkReq is not a struct; use tyr_test.getLinkReq`,
		},
		{
			name:   "non-struct request",
			define: func() { tyr.Define[string, *link]("links.get") },
			want:   `tyr: Define("links.get"): request type string is not a struct`,
		},
		{
			name:   "nil option",
			define: func() { tyr.Define[getLinkReq, *link]("links.get", nil) },
			want:   `tyr: Define("links.get"): nil option`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(tt.define); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}
