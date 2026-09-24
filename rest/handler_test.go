package rest_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

var update = flag.Bool("update", false, "update the golden files in testdata")

// golden compares a response body with the golden file testdata/name.json,
// or writes the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
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

// mount returns a mux that serves the operations register adds to a new API.
func mount(t *testing.T, register func(api *tyr.API)) *http.ServeMux {
	t.Helper()
	api := newAPI()
	register(api)
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	return mux
}

// do sends a request to h and returns the response. header holds pairs of
// header names and values.
func do(h http.Handler, method, target, contentType, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// updateReq has fields from every source.
type updateReq struct {
	Owner string `json:"owner" path:"owner"`
	Name  string `json:"name"`
	Limit int64  `json:"limit" query:"limit"` // int64: the range in errors is the same on every platform
	Tags  []int  `json:"tags" query:"tag"`
	Prio  uint8  `json:"prio" header:"X-Priority"`
	Page  *int   `json:"page" query:"page"`
}

// echo is an operation that returns its request.
func echo(api *tyr.API) {
	api.Handle("things.update", func(ctx context.Context, req updateReq) (updateReq, error) {
		return req, nil
	}, rest.Route("POST /owners/{owner}/things"))
}

func TestBinding(t *testing.T) {
	mux := mount(t, echo)
	tests := []struct {
		name        string
		target      string
		contentType string
		body        string
		header      []string
		want        string
	}{
		{
			name:   "no body",
			target: "/owners/ann/things?limit=5&tag=1&tag=2&page=2",
			header: []string{"X-Priority", "3"},
			want:   `{"owner":"ann","name":"","limit":5,"tags":[1,2],"prio":3,"page":2}`,
		},
		{
			// A pointer gets a value only when there is one.
			name:        "body",
			target:      "/owners/ann/things",
			contentType: "application/json",
			body:        `{"name":"n","limit":1,"tags":[7]}`,
			want:        `{"owner":"ann","name":"n","limit":1,"tags":[7],"prio":0,"page":null}`,
		},
		{
			// A missing parameter leaves what the body set, as page shows.
			name:        "the path, the query and headers win over the body",
			target:      "/owners/ann/things?limit=5",
			contentType: "application/json",
			body:        `{"owner":"bob","name":"n","limit":1,"prio":1,"page":1}`,
			header:      []string{"X-Priority", "3"},
			want:        `{"owner":"ann","name":"n","limit":5,"tags":[],"prio":3,"page":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(mux, "POST", tt.target, tt.contentType, tt.body, tt.header...)
			if rec.Code != http.StatusOK || rec.Body.String() != tt.want {
				t.Errorf("response = %d %s, want 200 %s", rec.Code, rec.Body, tt.want)
			}
		})
	}
}

// code is a type that parses itself.
type code string

func (c *code) UnmarshalText(b []byte) error {
	if len(b) < 2 {
		return errors.New("must be at least 2 characters")
	}
	*c = code(b)
	return nil
}

// level is a type that decodes JSON itself.
type level string

func (l *level) UnmarshalJSON(b []byte) error {
	if s := string(b); s != `"low"` && s != `"high"` {
		return errors.New("must be low or high")
	}
	*l = level(b)
	return nil
}

func TestBadRequests(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		echo(api)
		api.Handle("things.create", func(ctx context.Context, req struct {
			Code  code      `json:"code"`
			Level level     `json:"level"`
			Since time.Time `json:"since"`
			Items []struct {
				Count int `json:"count"`
			} `json:"items"`
		}) (string, error) {
			return "ok", nil
		}, rest.Route("POST /things"))
		api.Handle("things.page", func(ctx context.Context, req struct {
			Page int `json:"page" path:"page"`
		}) (string, error) {
			return "ok", nil
		}, rest.Route("POST /pages/{page}"))
		api.Handle("things.price", func(ctx context.Context, req struct {
			Price float64 `json:"price" query:"price"`
			Count int     `json:"count" query:"count"`
			Rate  float32 `json:"rate" header:"X-Rate"`
		}) (string, error) {
			return "ok", nil
		}, rest.Route("POST /prices"))
	})
	tests := []struct {
		name   string
		target string
		body   string
		header []string
	}{
		{
			// The pointer is the JSON name, the detail names the parameter.
			name:   "params",
			target: "/owners/ann/things?limit=many&tag=1&tag=x",
			header: []string{"X-Priority", "-1"},
		},
		{name: "path param", target: "/pages/first"},
		{name: "param out of range", target: "/owners/ann/things?limit=99999999999999999999"},
		{
			// The body can't carry these, so the path, the query and
			// headers can't either.
			name:   "invalid UTF-8",
			target: "/owners/%FF/things?tag=%FE",
			header: []string{"X-Priority", "\xff"},
		},
		{
			name:   "numbers outside JSON",
			target: "/prices?price=NaN&count=%2B5",
			header: []string{"X-Rate", "0x1p-2"},
		},
		{name: "broken JSON", target: "/things", body: `{"code": "go",}`},
		{name: "value of a wrong type", target: "/things", body: `{"items": [{"count": "many"}]}`},
		{name: "array for an object", target: "/things", body: `[1, 2]`},
		{name: "time", target: "/things", body: `{"since": "yesterday"}`},
		{name: "type that parses itself", target: "/things", body: `{"code": "x"}`},
		{name: "number for a type that parses strings", target: "/things", body: `{"code": 5}`},
		{name: "type that decodes JSON itself", target: "/things", body: `{"level": "extreme"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(mux, "POST", tt.target, "application/json", tt.body, tt.header...)
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("response = %d %s", rec.Code, rec.Header().Get("Content-Type"))
			}
			golden(t, "bad_request_"+strings.ReplaceAll(tt.name, " ", "_"), rec.Body.Bytes())
		})
	}
}

func TestContentType(t *testing.T) {
	mux := mount(t, echo)
	tests := []struct {
		name        string
		contentType string
		body        string
		wantCode    int
	}{
		{"POST without a body", "", "", http.StatusOK},
		{"no body with another type", "text/plain", "", http.StatusOK},
		{"application/json", "application/json", "{}", http.StatusOK},
		{"with charset", "application/json; charset=utf-8", "{}", http.StatusOK},
		{"in upper case", "APPLICATION/JSON", "{}", http.StatusOK},
		{"+json suffix", "application/merge-patch+json", "{}", http.StatusOK},
		{"bad parameter", "application/json; charset", "{}", http.StatusOK},
		{"text/plain", "text/plain", "{}", http.StatusUnsupportedMediaType},
		{"no type", "", "{}", http.StatusUnsupportedMediaType},
		{"broken type", "application/", "{}", http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(mux, "POST", "/owners/ann/things", tt.contentType, tt.body)
			if rec.Code != tt.wantCode {
				t.Errorf("response = %d %s, want %d", rec.Code, rec.Body, tt.wantCode)
			}
			if rec.Code == http.StatusUnsupportedMediaType {
				golden(t, "unsupported_media_type", rec.Body.Bytes())
			}
		})
	}
}

func TestMaxBodyBytes(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		echo(api)
		api.Handle("things.small", func(ctx context.Context, req struct {
			Name string `json:"name"`
		}) (string, error) {
			return req.Name, nil
		}, rest.Route("POST /small"), rest.MaxBodyBytes(16))
	})

	body := `{"name":"12345"}` // 16 bytes
	if rec := do(mux, "POST", "/small", "application/json", body); rec.Code != http.StatusOK {
		t.Errorf("a body at the limit: %d %s, want 200", rec.Code, rec.Body)
	}
	rec := do(mux, "POST", "/small", "application/json", body+" ")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit: %d, want 413", rec.Code)
	}
	golden(t, "too_large", rec.Body.Bytes())

	// The default limit is 1 MiB.
	large := `{"name":"` + strings.Repeat("x", 1<<20) + `"}`
	if rec := do(mux, "POST", "/owners/ann/things", "application/json", large); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over 1 MiB: %d, want 413", rec.Code)
	}
}

func TestResponses(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.get", getLink, rest.Route("GET /links/{code}"))
		api.Handle("links.create", getLink, rest.Route("POST /links/{code}"), rest.Status(http.StatusCreated))
		api.Handle("links.missing", func(ctx context.Context, req struct{}) (*link, error) {
			return nil, nil
		}, rest.Route("GET /missing"))
		api.Handle("links.delete", func(ctx context.Context, req getLinkReq) (struct{}, error) {
			return struct{}{}, nil
		}, rest.Route("DELETE /links/{code}"))
		api.Handle("links.purge", func(ctx context.Context, req struct{}) (struct{}, error) {
			return struct{}{}, nil
		}, rest.Route("POST /purge"), rest.Status(http.StatusAccepted))
	})
	tests := []struct {
		method, target string
		wantCode       int
		wantType       string
		wantBody       string
	}{
		{"GET", "/links/go", http.StatusOK, "application/json", `{"code":"go","url":"https://go.dev/go"}`},
		{"POST", "/links/go", http.StatusCreated, "application/json", `{"code":"go","url":"https://go.dev/go"}`},
		{"GET", "/missing", http.StatusOK, "application/json", `null`},
		{"DELETE", "/links/go", http.StatusNoContent, "", ""},
		{"POST", "/purge", http.StatusAccepted, "", ""},
	}
	for _, tt := range tests {
		rec := do(mux, tt.method, tt.target, "", "")
		if rec.Code != tt.wantCode || rec.Header().Get("Content-Type") != tt.wantType || rec.Body.String() != tt.wantBody {
			t.Errorf("%s %s = %d %q %s; want %d %q %s", tt.method, tt.target,
				rec.Code, rec.Header().Get("Content-Type"), rec.Body, tt.wantCode, tt.wantType, tt.wantBody)
		}
	}
}

// expiry is a detail of an error that isn't Violations.
type expiry struct {
	ExpiredAt string `json:"expired_at"`
}

func TestErrors(t *testing.T) {
	var v tyr.Violations
	v.Add("code", "only a-z, 0-9 and '-'")
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"not found", tyr.NotFound("link %q not found", "go"), http.StatusNotFound},
		{"violations", v.Err(), http.StatusBadRequest},
		{"details", tyr.FailedPrecondition("link expired").WithDetails(expiry{ExpiredAt: "2026-01-01"}), http.StatusConflict},
		{"internal", tyr.Internal("storage is read-only").WithDetails(expiry{}), http.StatusInternalServerError},
		{"unmapped", errors.New("db: connection refused"), http.StatusInternalServerError},
		{"unknown kind", &tyr.Error{Kind: tyr.Kind(42), Message: "odd"}, http.StatusInternalServerError},
		{"invalid UTF-8 in the message", tyr.NotFound("link %s not found", "\xff"), http.StatusNotFound},
		{"unencodable details", tyr.Unavailable("try again later").WithDetails(make(chan int)), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := mount(t, func(api *tyr.API) {
				api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
					return nil, tt.err
				}, rest.Route("GET /links/{code}"))
			})
			rec := do(mux, "GET", "/links/go", "", "")
			if rec.Code != tt.wantCode || rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("response = %d %s, want %d", rec.Code, rec.Header().Get("Content-Type"), tt.wantCode)
			}
			golden(t, "error_"+strings.ReplaceAll(tt.name, " ", "_"), rec.Body.Bytes())
		})
	}
}

func TestStatuses(t *testing.T) {
	tests := []struct {
		kind tyr.Kind
		want int
	}{
		{tyr.KindInvalidArgument, http.StatusBadRequest},
		{tyr.KindUnauthenticated, http.StatusUnauthorized},
		{tyr.KindPermissionDenied, http.StatusForbidden},
		{tyr.KindNotFound, http.StatusNotFound},
		{tyr.KindAlreadyExists, http.StatusConflict},
		{tyr.KindFailedPrecondition, http.StatusConflict},
		{tyr.KindResourceExhausted, http.StatusTooManyRequests},
		{tyr.KindCanceled, 499},
		{tyr.KindDeadlineExceeded, http.StatusGatewayTimeout},
		{tyr.KindUnavailable, http.StatusServiceUnavailable},
		{tyr.KindInternal, http.StatusInternalServerError},
		{tyr.Kind(42), http.StatusInternalServerError},
	}
	var kind tyr.Kind
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
			return nil, &tyr.Error{Kind: kind, Message: "failed"}
		}, rest.Route("GET /links/{code}"))
	})
	for _, tt := range tests {
		kind = tt.kind
		if rec := do(mux, "GET", "/links/go", "", ""); rec.Code != tt.want {
			t.Errorf("%v: status %d, want %d", tt.kind, rec.Code, tt.want)
		}
	}
}

func TestCanceled(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, ctx.Err()
	}, rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	// The client went away, and the handler stopped with the error of its
	// context: 499, as nginx has it, rather than 500, and no record.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/links/go", nil))

	if rec.Code != 499 || rec.Header().Get("Content-Type") != "application/problem+json" || buf.Len() != 0 {
		t.Errorf("response = %d %s, logged %q; want 499 problem+json and nothing", rec.Code, rec.Header().Get("Content-Type"), buf.Bytes())
	}
	golden(t, "error_canceled", rec.Body.Bytes())
}

func TestRequestInfo(t *testing.T) {
	api := newAPI()
	op := api.Handle("things.update", func(ctx context.Context, req updateReq) (updateReq, error) {
		return req, nil
	}, rest.Route("POST /owners/{owner}/things"), rest.MaxBodyBytes(16))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	tests := []struct {
		name        string
		contentType string
		body        string
		wantCode    int
	}{
		{"served", "application/json", `{"name":"a"}`, http.StatusOK},
		// Recorded before anything can fail.
		{"too large", "application/json", `{"name":"a long name"}`, http.StatusRequestEntityTooLarge},
		{"not JSON", "text/plain", "a", http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, info := tyr.WithRequestInfo(t.Context())
			req := httptest.NewRequestWithContext(ctx, "POST", "/owners/ann/things", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if got, ok := info.Operation(); rec.Code != tt.wantCode || info.Route() != "POST /owners/{owner}/things" || got != op || !ok {
				t.Errorf("%d, recorded %q and %v; want %d, the route and the operation", rec.Code, info.Route(), got, tt.wantCode)
			}
		})
	}
}

func TestReadError(t *testing.T) {
	mux := mount(t, echo)
	req := httptest.NewRequest("POST", "/owners/ann/things", iotest.ErrReader(errors.New("connection reset")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// The error of the transport isn't shown to the client.
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"detail":"invalid request"`) {
		t.Errorf("response = %d %s, want 400 with the detail invalid request", rec.Code, rec.Body)
	}
}

func TestUnencodableResult(t *testing.T) {
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.get", func(ctx context.Context, req getLinkReq) (chan int, error) {
			return make(chan int), nil
		}, rest.Route("GET /links/{code}"))
	})
	rec := do(mux, "GET", "/links/go", "", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("response = %d, want 500", rec.Code)
	}
	golden(t, "error_unencodable_result", rec.Body.Bytes())
}

func TestEncodingErrorsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil)))
	api := tyr.New(tyr.WithLogger(logger))
	api.Handle("links.result", func(ctx context.Context, req struct{}) (chan int, error) {
		return make(chan int), nil
	}, rest.Route("GET /result"))
	api.Handle("links.details", func(ctx context.Context, req struct{}) (string, error) {
		return "", tyr.Unavailable("try again later").WithDetails(make(chan int))
	}, rest.Route("GET /details"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	tests := []struct {
		target  string
		wantMsg string
		wantOp  string
	}{
		{"/result", "rest: encoding the result", "links.result"},
		{"/details", "rest: encoding error details", "links.details"},
	}
	for _, tt := range tests {
		buf.Reset()
		req := httptest.NewRequest("GET", tt.target, nil)
		req = req.WithContext(tyr.WithRequestID(req.Context(), "req-1"))
		mux.ServeHTTP(httptest.NewRecorder(), req)

		// The API's logger gets the record, with the request's context and
		// the operation.
		var got map[string]any
		for line := range bytes.Lines(buf.Bytes()) {
			var record map[string]any
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("unmarshal %q: %v", line, err)
			}
			if record["msg"] == tt.wantMsg {
				got = record
			}
		}
		if got["level"] != "ERROR" || got["request_id"] != "req-1" || got["operation"] != tt.wantOp ||
			!strings.Contains(fmt.Sprint(got["err"]), "chan int") {
			t.Errorf("GET %s logged %v, want an error %q with request_id req-1 and operation %s", tt.target, got, tt.wantMsg, tt.wantOp)
		}
	}
}

// Paging is embedded in requests, with its own binding and validation.
type Paging struct {
	Limit int `json:"limit" query:"limit" validate:"max=100"`
}

func TestEmbeddedAndValidated(t *testing.T) {
	type listReq struct {
		Paging
		Tag string `json:"tag" query:"tag" validate:"required"`
	}
	mux := mount(t, func(api *tyr.API) {
		api.Handle("links.list", func(ctx context.Context, req listReq) (listReq, error) {
			return req, nil
		}, rest.Route("GET /links"))
	})

	if rec := do(mux, "GET", "/links?limit=5&tag=go", "", ""); rec.Code != http.StatusOK || rec.Body.String() != `{"limit":5,"tag":"go"}` {
		t.Errorf("response = %d %s, want 200 {\"limit\":5,\"tag\":\"go\"}", rec.Code, rec.Body)
	}
	// Violations point to the members of the JSON object, as in JSON-RPC.
	rec := do(mux, "GET", "/links?limit=500", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("response = %d, want 400", rec.Code)
	}
	golden(t, "bad_request_validate_tags", rec.Body.Bytes())
}

func TestConcurrentRequests(t *testing.T) {
	mux := mount(t, echo)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			rec := do(mux, "POST", "/owners/ann/things?tag=1", "application/json", `{"name":"n"}`)
			if rec.Code != http.StatusOK {
				t.Errorf("response = %d %s", rec.Code, rec.Body)
			}
		})
	}
	wg.Wait()
}

func BenchmarkServeHTTP(b *testing.B) {
	// The handlers allocate nothing: what's measured is rest's own.
	goLink := &link{Code: "go", URL: "https://go.dev/go"}
	links := []*link{goLink}
	get := func(ctx context.Context, req getLinkReq) (*link, error) { return goLink, nil }
	api := newAPI()
	api.Handle("links.get", get, rest.Route("GET /links/{code}"))
	api.Handle("links.list", func(ctx context.Context, req struct {
		Owner string `json:"owner" path:"owner"`
		Limit int    `json:"limit" query:"limit" validate:"omitempty,max=100"`
		Tag   string `json:"tag" query:"tag"`
		Prio  int    `json:"prio" header:"X-Priority"`
	}) ([]*link, error) {
		return links, nil
	}, rest.Route("GET /owners/{owner}/links"))
	api.Handle("links.create", func(ctx context.Context, req struct {
		URL  string `json:"url" validate:"required,http_url"`
		Code string `json:"code" validate:"omitempty,min=4,max=16"`
	}) (*link, error) {
		return goLink, nil
	}, rest.Route("POST /links"), rest.Status(http.StatusCreated))
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	// links.get by hand, with net/http alone: what rest adds is the
	// difference.
	mux.HandleFunc("GET /by-hand/{code}", func(w http.ResponseWriter, r *http.Request) {
		res, _ := get(r.Context(), getLinkReq{Code: r.PathValue("code")})
		data, _ := json.Marshal(res)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	tests := []struct {
		name, method, target, body string
		header                     []string
	}{
		{name: "by hand", method: "GET", target: "/by-hand/go"},
		{name: "path", method: "GET", target: "/links/go"},
		{name: "path, query and header", method: "GET", target: "/owners/ann/links?limit=5&tag=go", header: []string{"X-Priority", "3"}},
		{name: "JSON body", method: "POST", target: "/links", body: `{"url":"https://go.dev","code":"gopher"}`},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			benchmarkServe(b, mux, tt.method, tt.target, tt.body, tt.header...)
		})
	}
}

// benchmarkServe serves a request to h over and over. It gives every
// response a cleared header, as net/http gives a new one, and a writer that
// drops the body, so that the handler is what's measured. header holds
// pairs of header names and values.
func benchmarkServe(b *testing.B, h http.Handler, method, target, body string, header ...string) {
	req := httptest.NewRequest(method, target, nil)
	r := bodyReader{strings.NewReader(body)}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Body, req.ContentLength = r, int64(len(body))
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := &discard{header: make(http.Header)}
	b.ReportAllocs()
	for b.Loop() {
		clear(w.header)
		r.Reset(body)
		h.ServeHTTP(w, req)
	}
}

// discard is a ResponseWriter that drops the body.
type discard struct {
	header http.Header
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}

// bodyReader is a request body that can be read again after Reset.
type bodyReader struct {
	*strings.Reader
}

func (bodyReader) Close() error { return nil }
