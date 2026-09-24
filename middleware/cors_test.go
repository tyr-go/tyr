package middleware_test

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr/middleware"
)

const (
	app   = "https://app.example.com"
	other = "https://other.example.com"
)

// preflightVary is the Vary of the answer to a preflight request.
const preflightVary = "Origin, Access-Control-Request-Method, Access-Control-Request-Headers"

func TestCORS(t *testing.T) {
	// next marks the responses of the requests that get to it.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next", "called")
	})
	policy := middleware.CORS{
		Origins: []string{app, "http://localhost:5173"},
		Expose:  []string{"Location", "X-Request-ID"},
		MaxAge:  90 * time.Second,
	}
	forbidden := http.Header{"Vary": {preflightVary}, "Content-Type": {"application/problem+json"}}
	tests := []struct {
		name       string
		policy     *middleware.CORS // policy if nil
		method     string
		header     []string // pairs of names and values
		wantStatus int
		wantHeader http.Header
	}{
		{
			name: "no origin", method: "GET",
			wantStatus: 200, wantHeader: http.Header{"Vary": {"Origin"}, "X-Next": {"called"}},
		},
		{
			name: "allowed origin", method: "GET", header: []string{"Origin", app},
			wantStatus: 200,
			wantHeader: http.Header{
				"Vary":                          {"Origin"},
				"Access-Control-Allow-Origin":   {app},
				"Access-Control-Expose-Headers": {"Location, X-Request-ID"},
				"X-Next":                        {"called"},
			},
		},
		{
			// Not rejected: pages send an Origin to their own origin too.
			name: "other origin", method: "POST", header: []string{"Origin", other},
			wantStatus: 200, wantHeader: http.Header{"Vary": {"Origin"}, "X-Next": {"called"}},
		},
		{
			name: "preflight", method: "OPTIONS",
			header:     []string{"Origin", app, "Access-Control-Request-Method", "PUT", "Access-Control-Request-Headers", "authorization,content-type"},
			wantStatus: 204,
			wantHeader: http.Header{
				"Vary":                         {preflightVary},
				"Access-Control-Allow-Origin":  {app},
				"Access-Control-Allow-Methods": {"PUT"},
				"Access-Control-Allow-Headers": {"authorization,content-type"},
				"Access-Control-Max-Age":       {"90"},
			},
		},
		{
			name: "preflight without headers", method: "OPTIONS",
			header:     []string{"Origin", "http://localhost:5173", "Access-Control-Request-Method", "DELETE"},
			wantStatus: 204,
			wantHeader: http.Header{
				"Vary":                         {preflightVary},
				"Access-Control-Allow-Origin":  {"http://localhost:5173"},
				"Access-Control-Allow-Methods": {"DELETE"},
				"Access-Control-Max-Age":       {"90"},
			},
		},
		{
			name: "preflight of another origin", method: "OPTIONS",
			header:     []string{"Origin", other, "Access-Control-Request-Method", "POST"},
			wantStatus: 403, wantHeader: forbidden,
		},
		{
			name: "preflight of an invalid method", method: "OPTIONS",
			header:     []string{"Origin", app, "Access-Control-Request-Method", "PO ST"},
			wantStatus: 403, wantHeader: forbidden,
		},
		{
			name: "preflight of invalid headers", method: "OPTIONS",
			header:     []string{"Origin", app, "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type,,x:y"},
			wantStatus: 403, wantHeader: forbidden,
		},
		{
			// Without Access-Control-Request-Method, it's a request of its own.
			name: "OPTIONS", method: "OPTIONS", header: []string{"Origin", app},
			wantStatus: 200,
			wantHeader: http.Header{
				"Vary":                          {"Origin"},
				"Access-Control-Allow-Origin":   {app},
				"Access-Control-Expose-Headers": {"Location, X-Request-ID"},
				"X-Next":                        {"called"},
			},
		},
		{
			name: "any origin", policy: &middleware.CORS{Origins: []string{"*"}}, method: "GET", header: []string{"Origin", other},
			wantStatus: 200,
			wantHeader: http.Header{"Vary": {"Origin"}, "Access-Control-Allow-Origin": {"*"}, "X-Next": {"called"}},
		},
		{
			name: "preflight of any origin", policy: &middleware.CORS{Origins: []string{"*"}}, method: "OPTIONS",
			header:     []string{"Origin", "null", "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type"},
			wantStatus: 204,
			wantHeader: http.Header{
				"Vary":                         {preflightVary},
				"Access-Control-Allow-Origin":  {"*"},
				"Access-Control-Allow-Methods": {"POST"},
				"Access-Control-Allow-Headers": {"content-type"},
			},
		},
		{
			name: "credentials", policy: &middleware.CORS{Origins: []string{app}, Credentials: true}, method: "GET", header: []string{"Origin", app},
			wantStatus: 200,
			wantHeader: http.Header{
				"Vary":                             {"Origin"},
				"Access-Control-Allow-Origin":      {app},
				"Access-Control-Allow-Credentials": {"true"},
				"X-Next":                           {"called"},
			},
		},
		{
			name: "preflight with credentials", policy: &middleware.CORS{Origins: []string{app}, Credentials: true}, method: "OPTIONS",
			header:     []string{"Origin", app, "Access-Control-Request-Method", "POST"},
			wantStatus: 204,
			wantHeader: http.Header{
				"Vary":                             {preflightVary},
				"Access-Control-Allow-Origin":      {app},
				"Access-Control-Allow-Credentials": {"true"},
				"Access-Control-Allow-Methods":     {"POST"},
			},
		},
		{
			name: "no origins", policy: &middleware.CORS{}, method: "OPTIONS",
			header:     []string{"Origin", app, "Access-Control-Request-Method", "POST"},
			wantStatus: 403, wantHeader: forbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := policy
			if tt.policy != nil {
				p = *tt.policy
			}
			req := httptest.NewRequest(tt.method, "/links", nil)
			for i := 0; i+1 < len(tt.header); i += 2 {
				req.Header.Set(tt.header[i], tt.header[i+1])
			}
			rec := httptest.NewRecorder()
			p.Handler(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !maps.EqualFunc(rec.Header(), tt.wantHeader, equalValues) {
				t.Errorf("response = %d %v, want %d %v", rec.Code, rec.Header(), tt.wantStatus, tt.wantHeader)
			}
			wantBody := ""
			if tt.wantStatus == 403 {
				wantBody = `{"type":"about:blank","title":"Forbidden","status":403}`
			}
			if rec.Body.String() != wantBody {
				t.Errorf("body = %q, want %q", rec.Body, wantBody)
			}
		})
	}
}

// equalValues reports whether two values of a header are the same.
func equalValues(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

func TestCORSVary(t *testing.T) {
	// CORS adds to the Vary of the middleware above it rather than
	// replacing it.
	above := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Accept-Encoding")
			next.ServeHTTP(w, r)
		})
	}
	h := middleware.Chain(http.NotFoundHandler(), above, middleware.CORS{Origins: []string{app}}.Handler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Values("Vary"); !equalValues(got, []string{"Accept-Encoding", "Origin"}) {
		t.Errorf("Vary = %q, want Accept-Encoding and Origin", got)
	}
}

func TestCORSMaxAge(t *testing.T) {
	// MaxAge is in whole seconds, and none leaves it to the browser.
	for _, tt := range []struct {
		maxAge time.Duration
		want   []string
	}{
		{0, nil},
		{time.Second / 2, nil},
		{1500 * time.Millisecond, []string{"1"}},
		{2 * time.Hour, []string{"7200"}},
	} {
		h := middleware.CORS{Origins: []string{app}, MaxAge: tt.maxAge}.Handler(http.NotFoundHandler())
		req := httptest.NewRequest("OPTIONS", "/", nil)
		req.Header.Set("Origin", app)
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Values("Access-Control-Max-Age"); !equalValues(got, tt.want) {
			t.Errorf("MaxAge %v: Access-Control-Max-Age = %q, want %q", tt.maxAge, got, tt.want)
		}
	}
}

func TestCORSPanics(t *testing.T) {
	const scheme = `want a scheme and a host, with a port if it isn't the default one, as in "https://app.example.com"`
	tests := []struct {
		policy middleware.CORS
		want   string // the panic after "middleware: CORS.Handler: "
	}{
		{middleware.CORS{Origins: []string{"https://app.example.com/"}}, `origin "https://app.example.com/": ` + scheme},
		{middleware.CORS{Origins: []string{"https://app.example.com/links"}}, `origin "https://app.example.com/links": ` + scheme},
		{middleware.CORS{Origins: []string{"https://app.example.com?x=1"}}, `origin "https://app.example.com?x=1": ` + scheme},
		{middleware.CORS{Origins: []string{"https://user@app.example.com"}}, `origin "https://user@app.example.com": ` + scheme},
		{middleware.CORS{Origins: []string{"https://app.example.com:"}}, `origin "https://app.example.com:": ` + scheme},
		{middleware.CORS{Origins: []string{"app.example.com"}}, `origin "app.example.com": ` + scheme},
		{middleware.CORS{Origins: []string{"null"}}, `origin "null": ` + scheme},
		{middleware.CORS{Origins: []string{""}}, `origin "": ` + scheme},
		{middleware.CORS{Origins: []string{"https://App.example.com"}}, `origin "https://App.example.com": want lower case, as browsers send it`},
		{middleware.CORS{Origins: []string{"https://пример.рф"}}, `origin "https://пример.рф": want ASCII, as browsers send it: punycode for an internationalized host`},
		{middleware.CORS{Origins: []string{"https://app.example.com:443"}}, `origin "https://app.example.com:443": browsers leave out the default port`},
		{middleware.CORS{Origins: []string{"http://app.example.com:80"}}, `origin "http://app.example.com:80": browsers leave out the default port`},
		{middleware.CORS{Origins: []string{"*", app}}, `origins "*" and others: "*" allows any origin, so it goes alone`},
		{middleware.CORS{Origins: []string{"*"}, Credentials: true}, `origins "*" with Credentials: browsers send credentials only to origins that are named`},
		{middleware.CORS{Expose: []string{"*"}}, `Expose: "*" isn't the name of a header`},
		{middleware.CORS{Expose: []string{""}}, `Expose: "" isn't the name of a header`},
		{middleware.CORS{Expose: []string{"X Request-ID"}}, `Expose: "X Request-ID" isn't the name of a header`},
		{middleware.CORS{MaxAge: -time.Second}, `MaxAge -1s: want 0 or more`},
	}
	for _, tt := range tests {
		want := "middleware: CORS.Handler: " + tt.want
		if got := panicValue(func() { tt.policy.Handler(http.NotFoundHandler()) }); got != want {
			t.Errorf("Handler() of %+v panicked with %v, want %q", tt.policy, got, want)
		}
	}

	// Origins as browsers send them.
	valid := middleware.CORS{Origins: []string{"http://localhost:5173", "https://app.example.com:8443", "https://[::1]:8443", "chrome-extension://abcdefgh"}}
	if got := panicValue(func() { valid.Handler(http.NotFoundHandler()) }); got != nil {
		t.Errorf("Handler() of %v panicked with %v", valid.Origins, got)
	}
}

func TestCORSCrossOriginProtection(t *testing.T) {
	csrf := middleware.CORS{Origins: []string{app, "http://localhost:5173"}}.CrossOriginProtection()
	for _, tt := range []struct {
		origin string
		ok     bool
	}{
		{app, true},
		{"http://localhost:5173", true},
		{other, false},
	} {
		// A POST from a page of another site, as a browser sends it.
		req := httptest.NewRequest("POST", "/links", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", tt.origin)
		if err := csrf.Check(req); (err == nil) != tt.ok {
			t.Errorf("Check() of a POST from %s = %v, want it to pass: %v", tt.origin, err, tt.ok)
		}
	}

	tests := []struct {
		policy middleware.CORS
		want   string
	}{
		{middleware.CORS{Origins: []string{"*"}}, `middleware: CORS.CrossOriginProtection: origins "*": the protection can't trust every origin`},
		{middleware.CORS{Origins: []string{"https://app.example.com/"}}, "middleware: CORS.CrossOriginProtection: origin \"https://app.example.com/\": " +
			`want a scheme and a host, with a port if it isn't the default one, as in "https://app.example.com"`},
	}
	for _, tt := range tests {
		if got := panicValue(func() { tt.policy.CrossOriginProtection() }); got != tt.want {
			t.Errorf("CrossOriginProtection() of %v panicked with %v, want %q", tt.policy.Origins, got, tt.want)
		}
	}
	// No origins: nothing cross-site gets through.
	req := httptest.NewRequest("POST", "/links", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", app)
	if err := (middleware.CORS{}).CrossOriginProtection().Check(req); err == nil {
		t.Error("with no origins, Check() of a cross-site POST = <nil>, want an error")
	}
}

func BenchmarkCORS(b *testing.B) {
	h := middleware.CORS{Origins: []string{app}, Expose: []string{"Location"}}.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/links/go", nil)
	req.Header.Set("Origin", app)
	for _, tt := range []struct {
		name string
		req  *http.Request
	}{
		{"no origin", httptest.NewRequest("GET", "/links/go", nil)},
		{"allowed origin", req},
	} {
		b.Run(tt.name, func(b *testing.B) {
			w := &discard{header: make(http.Header)}
			b.ReportAllocs()
			for b.Loop() {
				clear(w.header)
				h.ServeHTTP(w, tt.req)
			}
		})
	}
}
