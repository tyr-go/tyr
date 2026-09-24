package middleware_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/tyr-go/tyr/middleware"
)

func TestChain(t *testing.T) {
	var calls []string
	named := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, name+" in")
				next.ServeHTTP(w, r)
				calls = append(calls, name+" out")
			})
		}
	}
	h := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "handler")
	}), named("a"), named("b"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	want := []string{"a in", "b in", "handler", "b out", "a out"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}

	mux := http.NewServeMux()
	if got := middleware.Chain(mux); got != mux {
		t.Errorf("Chain(h) = %v, want h", got)
	}
}

func TestChainPanics(t *testing.T) {
	applied := 0
	counted := func(next http.Handler) http.Handler {
		applied++
		return next
	}
	null := func(http.Handler) http.Handler { return nil }
	mux := http.NewServeMux()

	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"nil handler", func() { middleware.Chain(nil, counted) }, "middleware: Chain: nil handler"},
		{"nil middleware", func() { middleware.Chain(mux, counted, nil) }, "middleware: Chain: nil middleware at index 1"},
		{"nil result", func() { middleware.Chain(mux, null, counted) }, "middleware: Chain: middleware at index 0 returned nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applied = 0
			if got := panicValue(tt.f); got != tt.want {
				t.Errorf("Chain() panicked with %v, want %q", got, tt.want)
			}
		})
	}
	// The middleware is checked before any is applied.
	applied = 0
	_ = panicValue(func() { middleware.Chain(mux, counted, nil) })
	if applied != 0 {
		t.Errorf("Chain() with a nil middleware applied %d others", applied)
	}
}

// panicValue returns what f panics with, nil if it doesn't.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

// logs is a slog.Handler that keeps the records it gets.
type logs struct {
	mu      sync.Mutex
	records []record
}

// record is a record that logs got, with its attributes as text.
type record struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (l *logs) Enabled(context.Context, slog.Level) bool {
	return true
}

func (l *logs) Handle(_ context.Context, r slog.Record) error {
	rec := record{level: r.Level, msg: r.Message, attrs: make(map[string]string)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
	return nil
}

func (l *logs) WithAttrs([]slog.Attr) slog.Handler {
	return l
}

func (l *logs) WithGroup(string) slog.Handler {
	return l
}

// get returns the records l got so far.
func (l *logs) get() []record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.records)
}

// serve serves h on an in-memory test server and returns its client and
// the log the server itself writes to, as net/http does of a superfluous
// WriteHeader call.
func serve(t *testing.T, h http.Handler) (*http.Client, *logs) {
	srv := httptest.NewTestServer(t, h)
	errs := &logs{}
	srv.Config.ErrorLog = slog.NewLogLogger(errs, slog.LevelError)
	return srv.Client(), errs
}

func BenchmarkChain(b *testing.B) {
	// The handler and the logger do nothing: what's measured is the
	// middleware's own.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	l := slog.New(slog.DiscardHandler)
	chain := middleware.Chain(h, middleware.RequestID(), middleware.Logger(l), middleware.Recover(l))
	tests := []struct {
		name string
		h    http.Handler
		id   string // the X-Request-ID of the request
	}{
		{"handler", h, ""},
		{"RequestID, Logger and Recover, incoming ID", chain, "req-1"},
		{"RequestID, Logger and Recover, new ID", chain, ""},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.id != "" {
				req.Header.Set("X-Request-ID", tt.id)
			}
			w := &discard{header: make(http.Header)}
			b.ReportAllocs()
			for b.Loop() {
				clear(w.header) // net/http gives every response a new one
				tt.h.ServeHTTP(w, req)
			}
		})
	}
}

// discard is a ResponseWriter that drops the body.
type discard struct {
	header http.Header
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}
