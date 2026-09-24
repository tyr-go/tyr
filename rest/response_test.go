package rest_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

// cached is a result that sets headers from its fields.
type cached struct {
	Code     string     `json:"code" header:"ETag"` // in the body too
	Modified time.Time  `json:"-" header:"Last-Modified"`
	Count    int        `json:"-" header:"X-Count"`
	Expires  *time.Time `json:"-" header:"Expires"`
}

type sinceReq struct {
	Since time.Time `json:"since" header:"If-Modified-Since"`
}

func TestResponseHeaders(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.cached", func(ctx context.Context, req sinceReq) (cached, error) {
			return cached{Code: "go", Modified: req.Since.Add(time.Hour)}, nil
		}, rest.Route("GET /cached"))
	})
	// Dates in headers are HTTP dates, both ways.
	rec := do(mux, "GET", "/cached", "", "", "If-Modified-Since", "Sun, 06 Nov 1994 08:49:37 GMT")

	want := http.Header{
		"Content-Type":  {"application/json"},
		"Etag":          {"go"},
		"Last-Modified": {"Sun, 06 Nov 1994 09:49:37 GMT"},
		"X-Count":       {"0"},
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `{"code":"go"}` || !equalHeader(rec.Header(), want) {
		t.Errorf("response = %d %v %s, want 200 %v {\"code\":\"go\"}", rec.Code, rec.Header(), rec.Body, want)
	}
}

// follow is the result of a redirect, with a member of its own.
type follow struct {
	URL  string `json:"url" header:"Location"`
	Hits int    `json:"hits"`
}

func TestRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		mux := mount(t, func(api *tyr.API) {
			api.Handle("links.follow", func(ctx context.Context, req getLinkReq) (follow, error) {
				return follow{URL: "https://go.dev/" + req.Code, Hits: 1}, nil
			}, rest.Route("GET /{code}"), rest.Status(status))
		})
		rec := do(mux, "GET", "/doc", "", "")

		// No body, though the result has JSON members.
		want := http.Header{"Location": {"https://go.dev/doc"}}
		if rec.Code != status || rec.Body.Len() != 0 || !equalHeader(rec.Header(), want) {
			t.Errorf("Status(%d): response = %d %v %q, want %d %v and no body", status, rec.Code, rec.Header(), rec.Body, status, want)
		}
	}
}

func TestResultErrorsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	api.Handle("links.follow", func(ctx context.Context, req getLinkReq) (*follow, error) {
		if req.Code == "nil" {
			return nil, nil
		}
		return &follow{}, nil // no URL
	}, rest.Route("GET /{code}"), rest.Status(http.StatusFound))
	api.Handle("links.bad", func(ctx context.Context, req struct{}) (withBadHeader, error) {
		return withBadHeader{OK: "yes"}, nil
	}, rest.Route("GET /bad/header"))
	api.Handle("links.unencodable", func(ctx context.Context, req struct{}) (withBadBody, error) {
		return withBadBody{OK: "yes"}, nil
	}, rest.Route("GET /bad/body"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	tests := []struct {
		target string
		msg    string
	}{
		{"/empty", "rest: a redirect without a Location"},
		{"/nil", "rest: a redirect without a Location"},
		{"/bad/header", "rest: encoding a header of the result"},
		{"/bad/body", "rest: encoding the result"},
	}
	for _, tt := range tests {
		buf.Reset()
		rec := do(mux, "GET", tt.target, "", "")

		// An internal error, without the headers the result set.
		if rec.Code != http.StatusInternalServerError || rec.Header().Get("X-Ok") != "" || rec.Header().Get("Location") != "" {
			t.Errorf("GET %s = %d %v, want 500 without the headers of the result", tt.target, rec.Code, rec.Header())
		}
		golden(t, "error_unencodable_result", rec.Body.Bytes()) // the generic internal error
		var record map[string]any
		if err := json.Unmarshal(buf.Bytes(), &record); err != nil || record["msg"] != tt.msg || record["level"] != "ERROR" {
			t.Errorf("GET %s logged %s, want one error %q", tt.target, buf.Bytes(), tt.msg)
		}
	}
}

// badText can't format itself.
type badText struct{}

func (badText) MarshalText() ([]byte, error) { return nil, errors.New("no text") }

type withBadHeader struct {
	OK  string  `json:"-" header:"X-Ok"`
	Bad badText `json:"-" header:"X-Bad"`
}

// withBadBody sets a header, but its body can't be encoded.
type withBadBody struct {
	OK  string   `json:"-" header:"X-Ok"`
	Bad chan int `json:"bad"`
}

// moved is a result without JSON members.
type moved struct {
	Location string `json:"-" header:"Location"`
}

func TestResultsWithoutMembers(t *testing.T) {
	handler := func(ctx context.Context, req struct{}) (moved, error) {
		return moved{Location: "/links/go"}, nil
	}
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.moved", handler, rest.Route("POST /moved"))
		api.Handle("links.created", handler, rest.Route("POST /created"), rest.Status(http.StatusCreated))
	})
	for target, status := range map[string]int{"/moved": http.StatusNoContent, "/created": http.StatusCreated} {
		rec := do(mux, "POST", target, "", "")
		want := http.Header{"Location": {"/links/go"}}
		if rec.Code != status || rec.Body.Len() != 0 || !equalHeader(rec.Header(), want) {
			t.Errorf("POST %s = %d %v %q, want %d %v and no body", target, rec.Code, rec.Header(), rec.Body, status, want)
		}
	}
}

func TestChallenge(t *testing.T) {
	api := newAPI()
	api.Handle("links.purge", func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, tyr.Unauthenticated("log in first")
	}, rest.Route("POST /purge"))
	api.Handle("links.lock", func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, tyr.PermissionDenied("admins only")
	}, rest.Route("POST /lock"))
	mux := http.NewServeMux()
	rest.Mount(mux, api, rest.Challenge(`Bearer realm="shortlink"`), rest.Challenge(`Basic realm="shortlink"`))

	rec := do(mux, "POST", "/purge", "", "")
	want := []string{`Bearer realm="shortlink"`, `Basic realm="shortlink"`}
	if got := rec.Header().Values("WWW-Authenticate"); rec.Code != http.StatusUnauthorized || !slices.Equal(got, want) {
		t.Errorf("POST /purge = %d, WWW-Authenticate %q; want 401 %q", rec.Code, got, want)
	}
	golden(t, "error_unauthenticated", rec.Body.Bytes())
	if rec := do(mux, "POST", "/lock", "", ""); rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("POST /lock = %d, WWW-Authenticate %q; want 403 without it", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestProblemTypes(t *testing.T) {
	api := newAPI()
	api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, tyr.NotFound("link %q not found", req.Code)
	}, rest.Route("GET /links/{code}"))
	types := rest.ProblemTypes("https://shortlink.example/problems/")
	mux := http.NewServeMux()
	routes := rest.Mount(mux, api, types)

	const notFound = `{"type":"https://shortlink.example/problems/not_found","title":"Not Found","status":404,"detail":"link \"go\" not found","kind":"not_found"}`
	if rec := do(mux, "GET", "/links/go", "", ""); rec.Code != http.StatusNotFound || rec.Body.String() != notFound {
		t.Errorf("GET /links/go = %d %s, want 404 %s", rec.Code, rec.Body, notFound)
	}

	// The routes write the same types outside operations.
	rec := httptest.NewRecorder()
	routes.WriteError(rec, httptest.NewRequest("GET", "/", nil), tyr.NotFound("link %q not found", "go"))
	if rec.Body.String() != notFound {
		t.Errorf("Routes.WriteError() = %s, want %s", rec.Body, notFound)
	}

	// The last ProblemTypes wins.
	routes = rest.Mount(http.NewServeMux(), newAPI(), types, rest.ProblemTypes("https://shortlink.example/problems#"))
	rec = httptest.NewRecorder()
	routes.WriteError(rec, httptest.NewRequest("GET", "/", nil), tyr.AlreadyExists("code is taken"))
	const taken = `{"type":"https://shortlink.example/problems#already_exists","title":"Already Exists","status":409,"detail":"code is taken","kind":"already_exists"}`
	if rec.Body.String() != taken {
		t.Errorf("Routes.WriteError() with two ProblemTypes = %s, want %s", rec.Body, taken)
	}

	// Without the / or the #, the name of a kind would run into the base.
	for _, base := range []string{"", "https://shortlink.example/problems", "https://shortlink.example/problems/not_found", "/problems/", "problems#", ":/"} {
		want := fmt.Sprintf("rest: ProblemTypes(%q): want an absolute URI that ends in / or #", base)
		if got := panicValue(func() { rest.ProblemTypes(base) }); got != want {
			t.Errorf("ProblemTypes(%q) panicked with %v, want %q", base, got, want)
		}
	}
}

func TestRoutesWriteError(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil)))))
	routes := rest.Mount(http.NewServeMux(), api, rest.Challenge(`Bearer realm="shortlink"`))

	// A 401 gets the challenges of the options of Mount, and only a 401.
	rec := httptest.NewRecorder()
	routes.WriteError(rec, httptest.NewRequest("GET", "/", nil), tyr.Unauthenticated("log in first"))
	if got := rec.Header().Values("WWW-Authenticate"); rec.Code != http.StatusUnauthorized || !slices.Equal(got, []string{`Bearer realm="shortlink"`}) {
		t.Errorf("Routes.WriteError() = %d, WWW-Authenticate %q; want 401 with the challenge", rec.Code, got)
	}
	rec = httptest.NewRecorder()
	routes.WriteError(rec, httptest.NewRequest("GET", "/", nil), tyr.PermissionDenied("admins only"))
	if rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("Routes.WriteError() = %d, WWW-Authenticate %q; want 403 without it", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	// An internal error goes to the logger of the API, with the context of
	// the request.
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(tyr.WithRequestID(req.Context(), "req-1"))
	rec = httptest.NewRecorder()
	routes.WriteError(rec, req, errors.New("disk full"))
	var record map[string]any
	if rec.Code != http.StatusInternalServerError || json.Unmarshal(buf.Bytes(), &record) != nil ||
		record["msg"] != "rest: internal error" || record["err"] != "internal: internal error: disk full" || record["request_id"] != "req-1" {
		t.Errorf("Routes.WriteError() = %d, logged %s; want 500 and the error in the log of the API", rec.Code, buf.Bytes())
	}

	write := func() { routes.WriteError(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), nil) }
	if got, want := panicValue(write), "rest: Routes.WriteError: nil error"; got != want {
		t.Errorf("Routes.WriteError(nil) panicked with %v, want %q", got, want)
	}
}

// Not parallel: it replaces the default logger, which WriteError logs to.
func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil))))

	var nilErr *tyr.Error
	tests := []struct {
		name   string
		err    error
		status int
		logged string // the error logged, "" if none is
	}{
		{"not found", tyr.NotFound("link %q not found", "go"), http.StatusNotFound, ""},
		{"wrapped", fmt.Errorf("finding: %w", tyr.NotFound("link %q not found", "go")), http.StatusNotFound, ""},
		{"violations", tyr.Violations{{Pointer: "/code", Detail: "is required"}}.Err(), http.StatusBadRequest, ""},
		{"unauthenticated", tyr.Unauthenticated("log in first"), http.StatusUnauthorized, ""},
		{"deadline", fmt.Errorf("db: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, ""},
		{"plain", errors.New("disk full"), http.StatusInternalServerError, "internal: internal error: disk full"},
		{"typed nil", nilErr, http.StatusInternalServerError, "internal: internal error: rest: a nil *tyr.Error was written as an error"},
		// The client gets only "internal error"; the log gets the rest.
		{"internal", tyr.Internal("disk is full"), http.StatusInternalServerError, "internal: disk is full"},
		{"unknown kind", &tyr.Error{Kind: tyr.Kind(42), Message: "odd"}, http.StatusInternalServerError, "Kind(42): odd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			req := httptest.NewRequest("GET", "/", nil)
			req = req.WithContext(tyr.WithRequestID(req.Context(), "req-1"))
			rec := httptest.NewRecorder()
			rest.WriteError(rec, req, tt.err)

			if rec.Code != tt.status || rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("WriteError() = %d %v, want %d problem+json", rec.Code, rec.Header(), tt.status)
			}
			golden(t, "write_error_"+strings.ReplaceAll(tt.name, " ", "_"), rec.Body.Bytes())

			var record map[string]any
			if tt.logged == "" {
				if buf.Len() != 0 {
					t.Errorf("WriteError() logged %s, want nothing", buf.Bytes())
				}
				return
			}
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil ||
				record["msg"] != "rest: internal error" || record["err"] != tt.logged || record["request_id"] != "req-1" {
				t.Errorf("WriteError() logged %s, want the error %q with the request ID", buf.Bytes(), tt.logged)
			}
		})
	}

	if got, want := panicValue(func() { rest.WriteError(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), nil) }), "rest: WriteError: nil error"; got != want {
		t.Errorf("WriteError(nil) panicked with %v, want %q", got, want)
	}
}

// Not parallel: it replaces the default logger, which WriteError logs to.
func TestWriteErrorCanceled(t *testing.T) {
	var buf bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	err := fmt.Errorf("db: %w", context.Canceled)

	// The client went away: the context of the request is canceled.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	rest.WriteError(rec, httptest.NewRequestWithContext(ctx, "GET", "/", nil), err)
	if rec.Code != 499 || buf.Len() != 0 {
		t.Errorf("WriteError() after the client went away = %d, logged %q; want 499 and nothing", rec.Code, buf.Bytes())
	}
	golden(t, "write_error_canceled", rec.Body.Bytes())

	// The request is alive, so the cancellation came from elsewhere.
	rec = httptest.NewRecorder()
	rest.WriteError(rec, httptest.NewRequest("GET", "/", nil), err)
	var record map[string]any
	if rec.Code != http.StatusInternalServerError || json.Unmarshal(buf.Bytes(), &record) != nil ||
		record["err"] != "internal: internal error: db: context canceled" {
		t.Errorf("WriteError() in a live request = %d, logged %s; want 500 and the error", rec.Code, buf.Bytes())
	}
}

// Not parallel: it replaces the default logger, which WriteError logs to.
func TestWriteErrorInternalDetails(t *testing.T) {
	var buf bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	rec := httptest.NewRecorder()
	err := tyr.Internal("disk is full").WithDetails(expiry{ExpiredAt: "2026-01-01"})
	rest.WriteError(rec, httptest.NewRequest("GET", "/", nil), err)

	// The client gets neither the message nor the details; the log gets
	// both.
	golden(t, "write_error_internal", rec.Body.Bytes())
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil ||
		record["err"] != "internal: disk is full" || fmt.Sprint(record["details"]) != "map[expired_at:2026-01-01]" {
		t.Errorf("WriteError() logged %s, want the message and the details", buf.Bytes())
	}
}

func TestWriteProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	rest.WriteProblem(rec, http.StatusForbidden)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("WriteProblem() = %d %v, want 403 problem+json", rec.Code, rec.Header())
	}
	golden(t, "problem_forbidden", rec.Body.Bytes())

	for _, status := range []int{0, 200, 399, 600} {
		want := fmt.Sprintf("rest: WriteProblem(%d): want a 4xx or 5xx status", status)
		if got := panicValue(func() { rest.WriteProblem(httptest.NewRecorder(), status) }); got != want {
			t.Errorf("WriteProblem(%d) panicked with %v, want %q", status, got, want)
		}
	}
}

func TestProblemHandler(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
			if req.Code == "missing" {
				return nil, tyr.NotFound("link %q not found", req.Code)
			}
			return getLink(ctx, req)
		}, rest.Route("GET /links/{code}"))
	})
	h := rest.ProblemHandler(mux)

	t.Run("replies of the mux", func(t *testing.T) {
		rec := do(h, "GET", "/nowhere", "", "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("GET /nowhere = %d %v, want 404 problem+json", rec.Code, rec.Header())
		}
		golden(t, "problem_not_found", rec.Body.Bytes())

		rec = do(h, "DELETE", "/links/go", "", "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("DELETE /links/go = %d %v, want 405 with Allow: GET, HEAD", rec.Code, rec.Header())
		}
		golden(t, "problem_method_not_allowed", rec.Body.Bytes())
	})

	t.Run("routes", func(t *testing.T) {
		// The route, its wildcards and its pattern, which Logger reads.
		req := httptest.NewRequest("GET", "/links/go", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"code":"go","url":"https://go.dev/go"}` || req.Pattern != "GET /links/{code}" {
			t.Errorf("GET /links/go = %d %s, pattern %q; want 200 from the route", rec.Code, rec.Body, req.Pattern)
		}

		// An operation's own 404 stays as it is.
		rec = do(h, "GET", "/links/missing", "", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /links/missing = %d, want 404", rec.Code)
		}
		golden(t, "error_not_found_link", rec.Body.Bytes())

		// So does the redirect of the mux to a clean path.
		if rec := do(h, "GET", "/links/../links/go", "", ""); rec.Code/100 != 3 || rec.Header().Get("Location") != "/links/go" {
			t.Errorf("GET /links/../links/go = %d %v, want a redirect to /links/go", rec.Code, rec.Header())
		}
	})

	if got, want := panicValue(func() { rest.ProblemHandler(nil) }), "rest: ProblemHandler: nil mux"; got != want {
		t.Errorf("ProblemHandler(nil) panicked with %v, want %q", got, want)
	}
}

func BenchmarkProblemHandler(b *testing.B) {
	// The route allocates nothing: the difference between the mux and
	// ProblemHandler is what ProblemHandler costs a request of a route, the
	// second lookup.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /links/{code}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := rest.ProblemHandler(mux)
	b.Run("mux, route", func(b *testing.B) { benchmarkServe(b, mux, "GET", "/links/go", "") })
	b.Run("ProblemHandler, route", func(b *testing.B) { benchmarkServe(b, h, "GET", "/links/go", "") })
	b.Run("ProblemHandler, no route", func(b *testing.B) { benchmarkServe(b, h, "GET", "/nowhere", "") })
}

// equalHeader reports whether the headers a and b are equal.
func equalHeader(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
