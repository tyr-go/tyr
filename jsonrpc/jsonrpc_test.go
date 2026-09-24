package jsonrpc_test

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
)

var update = flag.Bool("update", false, "update the golden files in testdata")

// golden compares a response body with the golden file testdata/name.json,
// or writes the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", strings.ReplaceAll(name, " ", "_")+".json")
	got = append(bytes.Clone(got), '\n') // a file ends with a newline, a body doesn't
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s:\ngot  %s\nwant %s", path, got, want)
	}
}

// newAPI returns an API that logs nothing.
func newAPI() *tyr.API {
	return tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
}

// post sends body to h as JSON and returns the response.
func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/rpc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// panicValue returns what f panics with, nil if it doesn't.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

func TestHandlerPanics(t *testing.T) {
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"nil API", func() { jsonrpc.Handler(nil) }, "jsonrpc: Handler: nil API"},
		{"nil option", func() { jsonrpc.Handler(newAPI(), nil) }, "jsonrpc: Handler: nil option"},
		{"MaxBatch(0)", func() { jsonrpc.MaxBatch(0) }, "jsonrpc: MaxBatch(0): want a positive number of calls"},
		{"MaxBatch(-1)", func() { jsonrpc.MaxBatch(-1) }, "jsonrpc: MaxBatch(-1): want a positive number of calls"},
		{"MaxBodyBytes(0)", func() { jsonrpc.MaxBodyBytes(0) }, "jsonrpc: MaxBodyBytes(0): want a positive size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(tt.f); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

func TestHandlerSeals(t *testing.T) {
	api := newAPI()
	jsonrpc.Handler(api)
	got := panicValue(func() {
		api.Handle("links.get", func(ctx context.Context, req struct{}) (struct{}, error) { return struct{}{}, nil })
	})
	if got == nil {
		t.Error("Handle() after Handler() didn't panic")
	}
}

// The operations of the examples of the JSON-RPC 2.0 specification, which
// take params by name.
type subtractReq struct {
	Minuend    int `json:"minuend"`
	Subtrahend int `json:"subtrahend"`
}

type sumReq struct {
	Numbers []int `json:"numbers"`
}

// specAPI returns the API of the examples of the specification.
func specAPI() *tyr.API {
	nothing := func(ctx context.Context, req struct{}) (struct{}, error) { return struct{}{}, nil }
	api := newAPI()
	api.Handle("subtract", func(ctx context.Context, req subtractReq) (int, error) {
		return req.Minuend - req.Subtrahend, nil
	})
	api.Handle("sum", func(ctx context.Context, req sumReq) (int, error) {
		sum := 0
		for _, n := range req.Numbers {
			sum += n
		}
		return sum, nil
	})
	api.Handle("update", nothing)
	api.Handle("notify_hello", nothing)
	api.Handle("notify_sum", nothing)
	api.Handle("get_data", func(ctx context.Context, req struct{}) ([]any, error) {
		return []any{"hello", 5}, nil
	})
	return api
}

// TestSpec sends the examples of section 7 of the JSON-RPC 2.0
// specification. The responses are those of the specification, except for
// params by position, which tyr doesn't take: the variants "by name" send
// the same params by name and get the responses of the specification.
func TestSpec(t *testing.T) {
	h := jsonrpc.Handler(specAPI())
	tests := []struct {
		name string
		body string
	}{
		{"call by position", `{"jsonrpc": "2.0", "method": "subtract", "params": [42, 23], "id": 1}`},
		{"call by position again", `{"jsonrpc": "2.0", "method": "subtract", "params": [23, 42], "id": 2}`},
		{"call by name", `{"jsonrpc": "2.0", "method": "subtract", "params": {"subtrahend": 23, "minuend": 42}, "id": 3}`},
		{"call by name again", `{"jsonrpc": "2.0", "method": "subtract", "params": {"minuend": 42, "subtrahend": 23}, "id": 4}`},
		{"notification", `{"jsonrpc": "2.0", "method": "update", "params": [1,2,3,4,5]}`},
		{"notification of a missing method", `{"jsonrpc": "2.0", "method": "foobar"}`},
		{"missing method", `{"jsonrpc": "2.0", "method": "foobar", "id": "1"}`},
		{"invalid JSON", `{"jsonrpc": "2.0", "method": "foobar, "params": "bar", "baz]`},
		{"invalid request object", `{"jsonrpc": "2.0", "method": 1, "params": "bar"}`},
		{"batch of invalid JSON", `[
			{"jsonrpc": "2.0", "method": "sum", "params": [1,2,4], "id": "1"},
			{"jsonrpc": "2.0", "method"
		]`},
		{"empty batch", `[]`},
		{"invalid batch", `[1]`},
		{"invalid batch of three", `[1,2,3]`},
		{"batch", `[
			{"jsonrpc": "2.0", "method": "sum", "params": [1,2,4], "id": "1"},
			{"jsonrpc": "2.0", "method": "notify_hello", "params": [7]},
			{"jsonrpc": "2.0", "method": "subtract", "params": [42,23], "id": "2"},
			{"foo": "boo"},
			{"jsonrpc": "2.0", "method": "foo.get", "params": {"name": "myself"}, "id": "5"},
			{"jsonrpc": "2.0", "method": "get_data", "id": "9"}
		]`},
		{"batch by name", `[
			{"jsonrpc": "2.0", "method": "sum", "params": {"numbers": [1,2,4]}, "id": "1"},
			{"jsonrpc": "2.0", "method": "notify_hello", "params": {"name": "world"}},
			{"jsonrpc": "2.0", "method": "subtract", "params": {"minuend": 42, "subtrahend": 23}, "id": "2"},
			{"foo": "boo"},
			{"jsonrpc": "2.0", "method": "foo.get", "params": {"name": "myself"}, "id": "5"},
			{"jsonrpc": "2.0", "method": "get_data", "id": "9"}
		]`},
		{"batch of notifications", `[
			{"jsonrpc": "2.0", "method": "notify_sum", "params": [1,2,4]},
			{"jsonrpc": "2.0", "method": "notify_hello", "params": [7]}
		]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(h, tt.body)
			if strings.Contains(tt.name, "notification") {
				// Nothing is returned.
				if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
					t.Errorf("response = %d %v %q, want 204 without a body", rec.Code, rec.Header(), rec.Body)
				}
				return
			}
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("response = %d %s, want 200 application/json", rec.Code, rec.Header().Get("Content-Type"))
			}
			golden(t, "spec_"+tt.name, rec.Body.Bytes())
		})
	}
}
