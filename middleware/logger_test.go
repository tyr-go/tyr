package middleware_test

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/middleware"
)

// transport returns a handler that records route and op in the
// tyr.RequestInfo of the request, as a transport does.
func transport(route string, op *tyr.Operation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, ok := tyr.RequestInfoFrom(r.Context()); ok {
			info.Record(route, op)
		}
	})
}

// newOperation returns an operation named name.
func newOperation(name string) *tyr.Operation {
	return tyr.New().Handle(name, func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
}

// withValue is a middleware that passes on another request, with a value
// in its context, which hides the route that a ServeMux sets in it.
func withValue(next http.Handler) http.Handler {
	type key struct{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, 1)))
	})
}

func TestLogger(t *testing.T) {
	tests := []struct {
		name    string
		path    string            // "/links/go" if empty
		handler http.HandlerFunc  // at GET /links/{code}
		want    map[string]string // the attributes other than the method, route and duration
	}{
		{
			name:    "status",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) },
			want:    map[string]string{"status": "201"},
		},
		{
			name:    "no status",
			handler: func(w http.ResponseWriter, r *http.Request) {},
			want:    map[string]string{"status": "200"},
		},
		{
			name:    "write",
			handler: func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "https://go.dev") },
			want:    map[string]string{"status": "200"},
		},
		{
			name: "1xx first",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusNoContent)
			},
			want: map[string]string{"status": "204"},
		},
		{
			name: "second status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusInternalServerError) // net/http drops it
			},
			want: map[string]string{"status": "201"},
		},
		{
			name:    "flush",
			handler: func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush() },
			want:    map[string]string{"status": "200"},
		},
		{
			name: "no route",
			path: "/nowhere",
			want: map[string]string{"route": "", "status": "404"},
		},
		{
			name: "aborted",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "partial")
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			},
			want: map[string]string{"status": "200", "aborted": "true"},
		},
		{
			name:    "aborted before the status",
			handler: func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) },
			want:    map[string]string{"status": "0", "aborted": "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mux := http.NewServeMux()
				if tt.handler != nil {
					mux.Handle("GET /links/{code}", tt.handler)
				}
				slow := func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						time.Sleep(time.Second)
						next.ServeHTTP(w, r)
					})
				}
				logs := &logs{}
				client, _ := serve(t, middleware.Chain(mux, middleware.Logger(slog.New(logs)), slow))

				path := "/links/go"
				if tt.path != "" {
					path = tt.path
				}
				if resp, err := client.Get("http://example.com" + path); err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				synctest.Wait()

				want := map[string]string{"method": "GET", "route": "GET /links/{code}", "duration": "1s"}
				maps.Copy(want, tt.want)
				got := logs.get()
				if len(got) != 1 || got[0].level != slog.LevelInfo || got[0].msg != "middleware: request" || !maps.Equal(got[0].attrs, want) {
					t.Errorf("logged %+v, want one middleware: request at INFO with %v", got, want)
				}
			})
		})
	}
}

// Not parallel: it replaces the default logger.
func TestNilLogger(t *testing.T) {
	h := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}), middleware.Logger(nil), middleware.Recover(nil))

	// Both log to the default logger as it is at the time of writing.
	logs := &logs{}
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(logs))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	got := logs.get()
	if len(got) != 2 || got[0].msg != "middleware: panic" || got[1].msg != "middleware: request" {
		t.Errorf("the default logger got %+v, want middleware: panic and middleware: request", got)
	}
}

func TestLoggerRequestInfo(t *testing.T) {
	op := newOperation("links.get")
	mux := http.NewServeMux()
	mux.Handle("GET /links/{code}", transport("GET /links/{code}", op))

	// Middleware above Logger reads the same RequestInfo.
	var above *tyr.RequestInfo
	metrics := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, info := tyr.WithRequestInfo(r.Context())
			above = info
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	logs := &logs{}
	h := middleware.Chain(mux, metrics, middleware.Logger(slog.New(logs)), withValue)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/links/go", nil))

	// withValue hides the route that the mux sets, but not the one the
	// transport records, and there is no warning.
	got := logs.get()
	want := map[string]string{"method": "GET", "route": "GET /links/{code}", "operation": "links.get", "status": "200"}
	if len(got) == 1 {
		delete(got[0].attrs, "duration")
	}
	if len(got) != 1 || got[0].msg != "middleware: request" || !maps.Equal(got[0].attrs, want) {
		t.Errorf("logged %+v, want one middleware: request with %v", got, want)
	}
	if op, _ := above.Operation(); above.Route() != "GET /links/{code}" || op == nil || op.Name() != "links.get" {
		t.Errorf("the middleware above Logger read %q and %v, want the route and links.get", above.Route(), op)
	}
}

func TestLoggerWarnsOfHiddenRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /links/{code}", func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("GET /things/{id}", transport("GET /things/{id}", newOperation("things.get")))
	tests := []struct {
		name    string
		handler http.Handler                      // mux if nil
		below   []func(http.Handler) http.Handler // the middleware under Logger
		method  string
		target  string
		header  []string // pairs of names and values
		warning bool
	}{
		{name: "hidden route", below: []func(http.Handler) http.Handler{withValue}, method: "GET", target: "/links/go", warning: true},
		{name: "hidden route of an operation", below: []func(http.Handler) http.Handler{withValue}, method: "GET", target: "/things/1"},
		{name: "operation without a route", handler: transport("", newOperation("things.get")), method: "GET", target: "/things/1"},
		{name: "route", method: "GET", target: "/links/go"},
		{name: "no route for the path", method: "GET", target: "/nowhere"},
		{name: "method not allowed", method: "POST", target: "/links/go"},
		{
			name: "rejected under Logger", below: []func(http.Handler) http.Handler{http.NewCrossOriginProtection().Handler},
			method: "POST", target: "/links/go", header: []string{"Sec-Fetch-Site", "cross-site"},
		},
		{
			name: "preflight that CORS answers", below: []func(http.Handler) http.Handler{middleware.CORS{Origins: []string{app}}.Handler},
			method: "OPTIONS", target: "/links/go", header: []string{"Origin", app, "Access-Control-Request-Method", "POST"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logs{}
			var handler http.Handler = mux
			if tt.handler != nil {
				handler = tt.handler
			}
			h := middleware.Chain(handler, append([]func(http.Handler) http.Handler{middleware.Logger(slog.New(logs))}, tt.below...)...)
			for range 2 {
				req := httptest.NewRequest(tt.method, tt.target, nil)
				for i := 0; i+1 < len(tt.header); i += 2 {
					req.Header.Set(tt.header[i], tt.header[i+1])
				}
				h.ServeHTTP(httptest.NewRecorder(), req)
			}

			// Once per Logger, at most.
			var warnings []record
			for _, r := range logs.get() {
				if r.level == slog.LevelWarn {
					warnings = append(warnings, r)
				}
			}
			want := 0
			if tt.warning {
				want = 1
			}
			if len(warnings) != want || want == 1 && (warnings[0].msg != "middleware: request without a route" || !strings.Contains(warnings[0].attrs["hint"], "put it above Logger")) {
				t.Errorf("warnings = %+v, want %d about a request without a route", warnings, want)
			}
		})
	}
}

func TestLoggerOuterMux(t *testing.T) {
	// A mux above Logger, as one that serves probes past the middleware,
	// sets its pattern in the request, "/" for the rest; that isn't the
	// route of the request, which is the pattern of the mux below.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /links/{code}", func(w http.ResponseWriter, r *http.Request) {})
	tests := []struct {
		name      string
		below     []func(http.Handler) http.Handler // the middleware under Logger
		method    string
		target    string
		header    []string // pairs of names and values
		wantRoute string
		warning   bool
	}{
		{name: "route", method: "GET", target: "/links/go", wantRoute: "GET /links/{code}"},
		{name: "no route for the path", method: "GET", target: "/nowhere"},
		{
			name: "rejected under Logger", below: []func(http.Handler) http.Handler{http.NewCrossOriginProtection().Handler},
			method: "POST", target: "/links/go", header: []string{"Sec-Fetch-Site", "cross-site"},
		},
		{
			name: "preflight that CORS answers", below: []func(http.Handler) http.Handler{middleware.CORS{Origins: []string{app}}.Handler},
			method: "OPTIONS", target: "/links/go", header: []string{"Origin", app, "Access-Control-Request-Method", "POST"},
		},
		{name: "hidden route", below: []func(http.Handler) http.Handler{withValue}, method: "GET", target: "/links/go", warning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logs{}
			root := http.NewServeMux()
			root.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {})
			root.Handle("/", middleware.Chain(mux, append([]func(http.Handler) http.Handler{middleware.Logger(slog.New(logs))}, tt.below...)...))
			req := httptest.NewRequest(tt.method, tt.target, nil)
			for i := 0; i+1 < len(tt.header); i += 2 {
				req.Header.Set(tt.header[i], tt.header[i+1])
			}
			root.ServeHTTP(httptest.NewRecorder(), req)

			var routes []string
			warnings := 0
			for _, r := range logs.get() {
				switch r.msg {
				case "middleware: request":
					routes = append(routes, r.attrs["route"])
				case "middleware: request without a route":
					warnings++
				}
			}
			if len(routes) != 1 || routes[0] != tt.wantRoute {
				t.Errorf("routes = %q, want %q", routes, tt.wantRoute)
			}
			if want := map[bool]int{true: 1}[tt.warning]; warnings != want {
				t.Errorf("%d warnings of a request without a route, want %d", warnings, want)
			}
		})
	}
}
