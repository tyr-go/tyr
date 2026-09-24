package health_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr/health"
)

// logs is a slog.Handler that keeps what it logs; checks log from their
// own goroutines.
type logs struct {
	mu      sync.Mutex
	records []record
}

// record is a record that logs got, without its time.
type record struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (l *logs) Enabled(context.Context, slog.Level) bool { return true }

func (l *logs) Handle(_ context.Context, r slog.Record) error {
	rec := record{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
	return nil
}

func (l *logs) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logs) WithGroup(string) slog.Handler      { return l }

func (l *logs) get() []record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.records
}

// probe sends a probe to h and returns the response.
func probe(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	return rec
}

// checkResponse reports what's wrong with a response of a probe, if
// anything.
func checkResponse(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	h := rec.Header()
	if rec.Code != code || rec.Body.String() != body || h.Get("Content-Type") != "application/json" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("response = %d %s %v, want %d %s as JSON and not to be stored", rec.Code, rec.Body, h, code, body)
	}
}

func TestLive(t *testing.T) {
	checkResponse(t, probe(health.Live()), 200, `{"status":"ok"}`)
}

func TestReadiness(t *testing.T) {
	errDown := errors.New("connection refused")
	pass := func(ctx context.Context) error { return nil }
	tests := []struct {
		name     string
		opts     []health.Option
		wantCode int
		wantBody string
		wantTook time.Duration
		wantLogs []record // stacks left out
	}{
		{name: "no checks", wantCode: 200, wantBody: `{"status":"ok"}`},
		{
			name:     "all pass",
			opts:     []health.Option{health.Check("db", pass), health.Check("cache", pass)},
			wantCode: 200, wantBody: `{"status":"ok","checks":{"db":"ok","cache":"ok"}}`,
		},
		{
			name: "one fails",
			opts: []health.Option{health.Check("db", pass), health.Check("cache", func(ctx context.Context) error {
				time.Sleep(time.Second / 2)
				return errDown
			})},
			wantCode: 503, wantBody: `{"status":"failed","checks":{"db":"ok","cache":"failed"}}`,
			wantTook: time.Second / 2,
			wantLogs: []record{{slog.LevelWarn, "health: check failed", map[string]string{"check": "cache", "err": "connection refused", "took": "500ms"}}},
		},
		{
			name: "in parallel",
			opts: []health.Option{
				health.Check("a", func(ctx context.Context) error { time.Sleep(time.Second); return nil }),
				health.Check("b", func(ctx context.Context) error { time.Sleep(time.Second); return nil }),
				health.Check("c", func(ctx context.Context) error { time.Sleep(time.Second); return nil }),
				health.Timeout(2 * time.Second),
			},
			wantCode: 200, wantBody: `{"status":"ok","checks":{"a":"ok","b":"ok","c":"ok"}}`,
			wantTook: time.Second,
		},
		{
			name: "timeout",
			opts: []health.Option{health.Check("db", func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})},
			wantCode: 503, wantBody: `{"status":"failed","checks":{"db":"failed"}}`,
			wantTook: time.Second, // by default
			wantLogs: []record{{slog.LevelWarn, "health: check failed", map[string]string{"check": "db", "err": "context deadline exceeded", "took": "1s"}}},
		},
		{
			name: "timeout of its own",
			opts: []health.Option{health.Timeout(time.Second / 4), health.Check("db", func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})},
			wantCode: 503, wantBody: `{"status":"failed","checks":{"db":"failed"}}`,
			wantTook: time.Second / 4,
			wantLogs: []record{{slog.LevelWarn, "health: check failed", map[string]string{"check": "db", "err": "context deadline exceeded", "took": "250ms"}}},
		},
		{
			// It ignores its context, and readiness waits for it.
			name: "nil too late",
			opts: []health.Option{health.Check("db", func(ctx context.Context) error {
				time.Sleep(2 * time.Second)
				return nil
			})},
			wantCode: 503, wantBody: `{"status":"failed","checks":{"db":"failed"}}`,
			wantTook: 2 * time.Second,
			wantLogs: []record{{slog.LevelWarn, "health: check returned after its timeout", map[string]string{"check": "db", "timeout": "1s", "took": "2s"}}},
		},
		{
			name:     "panic",
			opts:     []health.Option{health.Check("db", func(ctx context.Context) error { panic("nil map") })},
			wantCode: 503, wantBody: `{"status":"failed","checks":{"db":"failed"}}`,
			wantLogs: []record{{slog.LevelError, "health: check panicked", map[string]string{"check": "db", "panic": "nil map"}}},
		},
		{
			name:     "a name to quote",
			opts:     []health.Option{health.Check(`db "main"`, pass)},
			wantCode: 200, wantBody: `{"status":"ok","checks":{"db \"main\"":"ok"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := &logs{}
				ready := health.NewReadiness(append(tt.opts, health.WithLogger(slog.New(logs)))...)

				start := time.Now()
				rec := probe(ready)
				if took := time.Since(start); took != tt.wantTook {
					t.Errorf("the probe took %v, want %v", took, tt.wantTook)
				}
				checkResponse(t, rec, tt.wantCode, tt.wantBody)
				got := logs.get()
				for _, r := range got {
					if r.level == slog.LevelError && r.attrs["stack"] == "" {
						t.Errorf("%s: no stack", r.msg)
					}
					delete(r.attrs, "stack")
				}
				if !reflect.DeepEqual(got, tt.wantLogs) {
					t.Errorf("logged %+v, want %+v", got, tt.wantLogs)
				}
			})
		})
	}
}

func TestReadinessJoinsRuns(t *testing.T) {
	// Requests that come while a check runs wait for that run.
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		release := make(chan struct{})
		ready := health.NewReadiness(health.Check("db", func(ctx context.Context) error {
			runs.Add(1)
			<-release // it ignores its context
			return nil
		}), health.Timeout(time.Hour))

		responses := make(chan *httptest.ResponseRecorder, 3)
		for range 3 {
			go func() { responses <- probe(ready) }()
		}
		synctest.Wait()
		if n := runs.Load(); n != 1 {
			t.Errorf("%d runs for three requests, want 1", n)
		}
		close(release)
		for range 3 {
			checkResponse(t, <-responses, 200, `{"status":"ok","checks":{"db":"ok"}}`)
		}

		// A request after the run starts another.
		checkResponse(t, probe(ready), 200, `{"status":"ok","checks":{"db":"ok"}}`)
		if n := runs.Load(); n != 2 {
			t.Errorf("%d runs after the next request, want 2", n)
		}
	})
}

func TestReadinessClientGone(t *testing.T) {
	// A request stops waiting for a check that hangs when its client goes
	// away, and the check keeps its run until it returns.
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		ready := health.NewReadiness(health.WithLogger(slog.New(slog.DiscardHandler)), health.Check("db", func(ctx context.Context) error {
			<-release
			return nil
		}))
		ctx, cancel := context.WithTimeout(t.Context(), time.Second/2)
		defer cancel()
		rec := httptest.NewRecorder()
		start := time.Now()
		ready.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/readyz", nil))
		if took := time.Since(start); took != time.Second/2 {
			t.Errorf("the probe took %v, want 500ms", took)
		}
		checkResponse(t, rec, 503, `{"status":"failed","checks":{"db":"failed"}}`)
		close(release)
	})
}

func TestDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		ready := health.NewReadiness(health.Check("db", func(ctx context.Context) error {
			runs.Add(1)
			return nil
		}))
		checkResponse(t, probe(ready), 200, `{"status":"ok","checks":{"db":"ok"}}`)

		// Readiness fails at once, without running the checks, while Drain
		// waits.
		start := time.Now()
		drained := make(chan time.Duration)
		go func() {
			ready.Drain(t.Context(), 5*time.Second)
			drained <- time.Since(start)
		}()
		synctest.Wait()
		checkResponse(t, probe(ready), 503, `{"status":"draining"}`)
		if n := runs.Load(); n != 1 {
			t.Errorf("%d runs, want 1: none while draining", n)
		}
		checkResponse(t, probe(health.Live()), 200, `{"status":"ok"}`)
		if took := <-drained; took != 5*time.Second {
			t.Errorf("Drain() took %v, want 5s", took)
		}

		// Drain returns early once its context is done.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start = time.Now()
		ready.Drain(ctx, time.Minute)
		if took := time.Since(start); took != time.Second {
			t.Errorf("Drain() with a context done after 1s took %v", took)
		}
		checkResponse(t, probe(ready), 503, `{"status":"draining"}`)
	})
}

func TestReadinessLeaksNothing(t *testing.T) {
	// Checks that listen to their context leave nothing behind, those that
	// time out too.
	ready := health.NewReadiness(
		health.WithLogger(slog.New(slog.DiscardHandler)),
		health.Timeout(time.Millisecond),
		health.Check("db", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
		health.Check("cache", func(ctx context.Context) error { return nil }),
	)
	for range 3 {
		checkResponse(t, probe(ready), 503, `{"status":"failed","checks":{"db":"failed","cache":"ok"}}`)
	}

	var profile bytes.Buffer
	if err := pprof.Lookup("goroutineleak").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile.String(), "health") {
		t.Errorf("leaked goroutines:\n%s", profile.String())
	}
}

func TestPanics(t *testing.T) {
	pass := func(ctx context.Context) error { return nil }
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"empty name", func() { health.Check("", pass) }, `health: Check(""): want a name of valid UTF-8`},
		{"invalid UTF-8", func() { health.Check("db\xff", pass) }, `health: Check("db\xff"): want a name of valid UTF-8`},
		{"nil check", func() { health.Check("db", nil) }, `health: Check("db"): nil check`},
		{"zero timeout", func() { health.Timeout(0) }, `health: Timeout(0s): want a positive duration`},
		{"negative timeout", func() { health.Timeout(-time.Second) }, `health: Timeout(-1s): want a positive duration`},
		{"nil option", func() { health.NewReadiness(nil) }, `health: NewReadiness: nil option`},
		{"two checks of a name", func() { health.NewReadiness(health.Check("db", pass), health.Check("db", pass)) }, `health: NewReadiness: two checks named "db"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(tt.f); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

// panicValue returns what f panics with, nil if it doesn't.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}
