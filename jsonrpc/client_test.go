package jsonrpc_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/rest"
)

// echoOp is the contract of things.echo of echoAPI.
var echoOp = tyr.Define[thing, thing]("things.echo")

// newClient returns a client of h, which a test server serves over a
// network in memory.
func newClient(t *testing.T, h http.Handler) *jsonrpc.Client {
	t.Helper()
	srv := httptest.NewTestServer(t, h)
	return jsonrpc.NewClient("http://example.com/rpc", srv.Client())
}

// replyClient returns a client of a server that answers every request
// with status and body, of the given Content-Type.
func replyClient(t *testing.T, status int, contentType, body string) *jsonrpc.Client {
	t.Helper()
	return newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

// roundTripFunc is an http.RoundTripper made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewClientPanics(t *testing.T) {
	const notURL = `jsonrpc: NewClient(%q): want an absolute http or https URL`
	tests := []struct {
		endpoint string
		hc       *http.Client
		want     string
	}{
		{"", http.DefaultClient, fmt.Sprintf(notURL, "")},
		{"/rpc", http.DefaultClient, fmt.Sprintf(notURL, "/rpc")},
		{"localhost:8080/rpc", http.DefaultClient, fmt.Sprintf(notURL, "localhost:8080/rpc")}, // the scheme is localhost
		{"ftp://links/rpc", http.DefaultClient, fmt.Sprintf(notURL, "ftp://links/rpc")},
		{"http:///rpc", http.DefaultClient, fmt.Sprintf(notURL, "http:///rpc")},
		{"http://links/%zz", http.DefaultClient, fmt.Sprintf(notURL, "http://links/%zz")},
		{"http://links/rpc", nil, `jsonrpc: NewClient("http://links/rpc"): nil http.Client`},
		// A panic doesn't print a password.
		{"ftp://user:secret@links/rpc", http.DefaultClient, fmt.Sprintf(notURL, "ftp://user:***@links/rpc")},
		{"http://user:secret@links/rpc", nil, `jsonrpc: NewClient("http://user:***@links/rpc"): nil http.Client`},
	}
	for _, tt := range tests {
		if got := panicValue(func() { jsonrpc.NewClient(tt.endpoint, tt.hc) }); got != tt.want {
			t.Errorf("NewClient(%q) panicked with %v, want %q", tt.endpoint, got, tt.want)
		}
	}
	if got, want := panicValue(func() { jsonrpc.NewClient("http://links/rpc", http.DefaultClient, nil) }), `jsonrpc: NewClient("http://links/rpc"): nil option`; got != want {
		t.Errorf("NewClient() with a nil option panicked with %v, want %q", got, want)
	}
	for _, n := range []int64{0, -1} {
		want := fmt.Sprintf("jsonrpc: MaxResponseBytes(%d): want a positive size", n)
		if got := panicValue(func() { jsonrpc.MaxResponseBytes(n) }); got != want {
			t.Errorf("MaxResponseBytes(%d) panicked with %v, want %q", n, got, want)
		}
	}
}

func TestClientRedirects(t *testing.T) {
	// A JSON-RPC endpoint answers with 200, so a redirect is no answer: the
	// client doesn't follow it, and the call, with the headers that the
	// Transport adds, such as a token, goes nowhere else.
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		var elsewhere atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/elsewhere" {
				elsewhere.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{"code":"elsewhere","count":0},"id":1}`)
				return
			}
			http.Redirect(w, r, "http://other.example/elsewhere", status)
		}))
		hc := srv.Client()
		c := jsonrpc.NewClient("http://example.com/rpc", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer secret")
			return hc.Transport.RoundTrip(r)
		})})

		_, err := c.Call(t.Context(), echoOp, thing{Code: "go"})
		if he, ok := errors.AsType[*jsonrpc.HTTPError](err); !ok || he.StatusCode != status {
			t.Errorf("Call() redirected with %d = %v, want an HTTPError with %d", status, err, status)
		}
		if n := elsewhere.Load(); n != 0 {
			t.Errorf("a redirect with %d reached where it points %d times, want none", status, n)
		}
	}

	// The client works with a copy: the http.Client given keeps its own
	// policy.
	hc := &http.Client{}
	jsonrpc.NewClient("http://links/rpc", hc)
	if hc.CheckRedirect != nil {
		t.Error("NewClient() changed the CheckRedirect of the http.Client given")
	}
}

func TestClientResponseLimit(t *testing.T) {
	get := tyr.Define[thing, string]("things.get")
	// body is the response to the call, with a result of n bytes.
	body := func(n int) []byte {
		return []byte(`{"jsonrpc":"2.0","result":"` + strings.Repeat("a", n) + `","id":1}`)
	}
	serving := func(data []byte, header http.Header) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			maps.Copy(w.Header(), header)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(data)
		})
	}
	gzipped := func(data []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(data)
		_ = zw.Close()
		return buf.Bytes()
	}

	small := body(1000)
	tests := []struct {
		name   string
		h      http.Handler
		limit  int64 // 0 for the default
		larger bool  // than the limit
	}{
		{"at the limit", serving(small, nil), int64(len(small)), false},
		{"a byte over", serving(small, nil), int64(len(small)) - 1, true},
		{"its length over", serving(small, http.Header{"Content-Length": {strconv.Itoa(len(small))}}), int64(len(small)) - 1, true},
		// Counted as decompressed: a small body of gzip can be large.
		{"gzip under", serving(gzipped(body(10000)), http.Header{"Content-Encoding": {"gzip"}}), 20000, false},
		{"gzip over", serving(gzipped(body(10000)), http.Header{"Content-Encoding": {"gzip"}}), 5000, true},
		{"default", serving(body(4<<20), nil), 0, true},
		{"default under", serving(body(4<<20-100), nil), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []jsonrpc.ClientOption
			limit := int64(4 << 20)
			if tt.limit > 0 {
				opts, limit = append(opts, jsonrpc.MaxResponseBytes(tt.limit)), tt.limit
			}
			c := jsonrpc.NewClient("http://example.com/rpc", httptest.NewTestServer(t, tt.h).Client(), opts...)
			res, err := c.Call(t.Context(), get, thing{})
			want := fmt.Sprintf("jsonrpc: things.get: invalid response: larger than %d bytes", limit)
			switch {
			case tt.larger && (err == nil || err.Error() != want):
				t.Errorf("Call() = %d bytes, %v; want %s", len(res), err, want)
			case !tt.larger && err != nil:
				t.Errorf("Call() = %v, want a result", err)
			}
		})
	}
}

func TestClientCall(t *testing.T) {
	api := echoAPI()
	api.Handle("things.list", func(ctx context.Context, req struct{}) ([]thing, error) {
		return []thing{{Code: "a", Count: 1}, {Code: "b", Count: 2}}, nil
	})
	api.Handle("things.none", func(ctx context.Context, req struct{}) (*thing, error) {
		return nil, nil
	})
	c := newClient(t, jsonrpc.Handler(api))

	got, err := c.Call(t.Context(), echoOp, thing{Code: "go", Count: 2})
	if want := (thing{Code: "go", Count: 2}); got != want || err != nil {
		t.Errorf("Call(things.echo) = %+v, %v; want %+v, <nil>", got, err, want)
	}
	list, err := c.Call(t.Context(), tyr.Define[struct{}, []thing]("things.list"), struct{}{})
	if want := []thing{{Code: "a", Count: 1}, {Code: "b", Count: 2}}; !slices.Equal(list, want) || err != nil {
		t.Errorf("Call(things.list) = %+v, %v; want %+v, <nil>", list, err, want)
	}
	none, err := c.Call(t.Context(), tyr.Define[struct{}, *thing]("things.none"), struct{}{})
	if none != nil || err != nil {
		t.Errorf("Call(things.none) = %+v, %v; want <nil>, <nil>", none, err)
	}
}

func TestClientRequest(t *testing.T) {
	type sent struct {
		method, path, contentType, accept string
		requestID                         []string
		body                              string
	}
	requests := make(chan sent, 3)
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- sent{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Values("X-Request-ID"), string(body)}
		var req struct {
			ID jsontext.Value `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{},"id":`+string(req.ID)+`}`)
	}))

	for _, ctx := range []context.Context{
		t.Context(),
		tyr.WithRequestID(t.Context(), "0192f5e2"),
		tyr.WithRequestID(t.Context(), "a\nb"), // one the next service wouldn't take
	} {
		if _, err := c.Call(ctx, echoOp, thing{Code: "go", Count: 2}); err != nil {
			t.Fatal(err)
		}
	}
	close(requests)

	body := func(id int) string {
		return `{"jsonrpc":"2.0","method":"things.echo","params":{"code":"go","count":2},"id":` + strconv.Itoa(id) + `}`
	}
	want := []sent{
		{"POST", "/rpc", "application/json", "application/json", nil, body(1)},
		{"POST", "/rpc", "application/json", "application/json", []string{"0192f5e2"}, body(2)},
		{"POST", "/rpc", "application/json", "application/json", nil, body(3)},
	}
	i := 0
	for got := range requests {
		if w := want[i]; got.method != w.method || got.path != w.path || got.contentType != w.contentType ||
			got.accept != w.accept || !slices.Equal(got.requestID, w.requestID) || got.body != w.body {
			t.Errorf("request %d = %+v, want %+v", i, got, w)
		}
		i++
	}
}

func TestClientErrors(t *testing.T) {
	var v tyr.Violations
	v.Add("code", "only a-z, 0-9 and '-'")
	tests := []struct {
		name    string
		err     error
		kind    tyr.Kind
		message string
		details string // "" for none
	}{
		{"invalid argument", tyr.InvalidArgument("code is too long"), tyr.KindInvalidArgument, "code is too long", ""},
		{"violations", v.Err(), tyr.KindInvalidArgument, "validation failed", `[{"pointer":"/code","detail":"only a-z, 0-9 and '-'"}]`},
		{"unauthenticated", tyr.Unauthenticated("log in first"), tyr.KindUnauthenticated, "log in first", ""},
		{"permission denied", tyr.PermissionDenied("admins only"), tyr.KindPermissionDenied, "admins only", ""},
		{"not found", tyr.NotFound("link %q not found", "go"), tyr.KindNotFound, `link "go" not found`, ""},
		// These two have the same code, and the kind tells them apart.
		{"already exists", tyr.AlreadyExists("code is taken"), tyr.KindAlreadyExists, "code is taken", ""},
		{
			"failed precondition", tyr.FailedPrecondition("link expired").WithDetails(expiry{ExpiredAt: "2026-01-01"}),
			tyr.KindFailedPrecondition, "link expired", `{"expired_at":"2026-01-01"}`,
		},
		{"resource exhausted", tyr.ResourceExhausted("too many links"), tyr.KindResourceExhausted, "too many links", ""},
		{"canceled", tyr.Canceled("stopped"), tyr.KindCanceled, "stopped", ""},
		{"unavailable", tyr.Unavailable("try again later"), tyr.KindUnavailable, "try again later", ""},
		{"deadline exceeded", tyr.DeadlineExceeded("took too long"), tyr.KindDeadlineExceeded, "took too long", ""},
		// The server tells nothing of these.
		{"internal", tyr.Internal("storage is read-only").WithDetails(expiry{}), tyr.KindInternal, "internal error", ""},
		{"unmapped", errors.New("db: connection refused"), tyr.KindInternal, "internal error", ""},
		{"unknown kind", &tyr.Error{Kind: tyr.Kind(42), Message: "odd"}, tyr.KindInternal, "internal error", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			get := tyr.Define[struct{}, struct{}]("links.get")
			api := newAPI()
			api.Implement(get, func(ctx context.Context, req struct{}) (struct{}, error) {
				return struct{}{}, tt.err
			})
			_, err := newClient(t, jsonrpc.Handler(api)).Call(t.Context(), get, struct{}{})

			// What the server answered, wrapped with the name of the
			// operation, and no *tyr.Error, which a handler would pass on.
			se, ok := errors.AsType[*jsonrpc.ServerError](err)
			if !ok {
				t.Fatalf("Call() = %v (%T), want a *jsonrpc.ServerError", err, err)
			}
			if se.Kind != tt.kind || se.Message != tt.message || !sameDetails(se.Details, tt.details) {
				t.Errorf("Call() = %v, %q, %s; want %v, %q, %s", se.Kind, se.Message, se.Details, tt.kind, tt.message, tt.details)
			}
			if want := "jsonrpc: links.get: " + se.Error(); err.Error() != want {
				t.Errorf("Call() = %q, want %q", err, want)
			}
			if _, ok := errors.AsType[*tyr.Error](err); ok {
				t.Errorf("Call() = %v, a *tyr.Error", err)
			}
		})
	}
}

// sameDetails reports whether d, the details of an error that a client
// got, are want as JSON, or nil if want is "".
func sameDetails(d jsontext.Value, want string) bool {
	if want == "" {
		return d == nil
	}
	return string(d) == want
}

func TestClientViolations(t *testing.T) {
	c := newClient(t, jsonrpc.Handler(echoAPI()))
	_, err := c.Call(t.Context(), tyr.Define[struct{}, struct{}]("things.check"), struct{}{})

	// As the documentation of Call has it.
	var v tyr.Violations
	se, ok := errors.AsType[*jsonrpc.ServerError](err)
	if !ok || se.Kind != tyr.KindInvalidArgument {
		t.Fatalf("Call() = %v, want invalid_argument", err)
	}
	if err := json.Unmarshal(se.Details, &v); err != nil {
		t.Fatalf("details = %s, want violations: %v", se.Details, err)
	}
	if want := (tyr.Violations{{Pointer: "/code", Detail: "is required"}}); !slices.Equal(v, want) {
		t.Errorf("violations = %+v, want %+v", v, want)
	}
}

func TestClientErrorCodes(t *testing.T) {
	tests := []struct {
		name    string
		error   string // the error member of the response
		kind    tyr.Kind
		details string // "" for none
	}{
		// Without a kind, as from a server other than tyr, by the code.
		{"invalid params", `{"code":-32602,"message":"m"}`, tyr.KindInvalidArgument, ""},
		{"401", `{"code":401,"message":"m"}`, tyr.KindUnauthenticated, ""},
		{"403", `{"code":403,"message":"m"}`, tyr.KindPermissionDenied, ""},
		{"404", `{"code":404,"message":"m"}`, tyr.KindNotFound, ""},
		{"409", `{"code":409,"message":"m"}`, tyr.KindFailedPrecondition, ""},
		{"429", `{"code":429,"message":"m"}`, tyr.KindResourceExhausted, ""},
		{"499", `{"code":499,"message":"m"}`, tyr.KindCanceled, ""},
		{"503", `{"code":503,"message":"m"}`, tyr.KindUnavailable, ""},
		{"504", `{"code":504,"message":"m"}`, tyr.KindDeadlineExceeded, ""},
		{"internal error", `{"code":-32603,"message":"m"}`, tyr.KindInternal, ""},
		{"method not found", `{"code":-32601,"message":"m"}`, tyr.KindInternal, ""},
		{"parse error", `{"code":-32700,"message":"m"}`, tyr.KindInternal, ""},
		{"invalid request", `{"code":-32600,"message":"m"}`, tyr.KindInternal, ""},
		{"other code", `{"code":418,"message":"m"}`, tyr.KindInternal, ""},
		// The kind in data goes first.
		{"kind", `{"code":409,"message":"m","data":{"kind":"already_exists"}}`, tyr.KindAlreadyExists, ""},
		{"kind over the code", `{"code":404,"message":"m","data":{"kind":"unavailable"}}`, tyr.KindUnavailable, ""},
		{"kind of this package only", `{"code":-32603,"message":"m","data":{"kind":"Kind(42)"}}`, tyr.Kind(42), ""},
		{"unknown kind", `{"code":404,"message":"m","data":{"kind":"data_loss"}}`, tyr.KindNotFound, ""},
		{"kind not a string", `{"code":404,"message":"m","data":{"kind":4}}`, tyr.KindNotFound, ""},
		{"data not an object", `{"code":404,"message":"m","data":"gone"}`, tyr.KindNotFound, ""},
		{"details", `{"code":404,"message":"m","data":{"kind":"not_found","details":{"a":[1,2]}}}`, tyr.KindNotFound, `{"a":[1,2]}`},
		{"details without a kind", `{"code":404,"message":"m","data":{"details":[1]}}`, tyr.KindNotFound, `[1]`},
		{"null details", `{"code":404,"message":"m","data":{"kind":"not_found","details":null}}`, tyr.KindNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := replyClient(t, http.StatusOK, "application/json", `{"jsonrpc":"2.0","error":`+tt.error+`,"id":1}`)
			_, err := c.Call(t.Context(), echoOp, thing{})
			se, ok := errors.AsType[*jsonrpc.ServerError](err)
			if !ok {
				t.Fatalf("Call() = %v (%T), want a *jsonrpc.ServerError", err, err)
			}
			var sent struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal([]byte(tt.error), &sent); err != nil {
				t.Fatal(err)
			}
			if se.Kind != tt.kind || se.Code != sent.Code || se.Message != "m" || !sameDetails(se.Details, tt.details) {
				t.Errorf("Call() = %v, %d, %q, %s; want %v, %d, %q, %s", se.Kind, se.Code, se.Message, se.Details, tt.kind, sent.Code, "m", tt.details)
			}
		})
	}
}

func TestClientNullID(t *testing.T) {
	// The server can't tell the id of a request it can't parse.
	c := replyClient(t, http.StatusOK, "application/json", `{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error"},"id":null}`)
	_, err := c.Call(t.Context(), echoOp, thing{})
	const want = "jsonrpc: things.echo: internal (-32700): Parse error"
	if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindInternal || err.Error() != want {
		t.Errorf("Call() = %v, want %s", err, want)
	}
}

func TestClientInvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		contentType string // application/json if empty
		body        string
		want        string // the error, or its start if prefix
		prefix      bool
	}{
		{
			name: "not JSON", contentType: "text/html; charset=utf-8", body: "<html></html>",
			want: `invalid response: Content-Type is "text/html; charset=utf-8", want JSON`,
		},
		{name: "broken JSON", body: `{"jsonrpc":`, want: "invalid response: ", prefix: true},
		{name: "batch", body: `[{"jsonrpc":"2.0","result":{},"id":1}]`, want: "invalid response: ", prefix: true},
		{name: "duplicate member", body: `{"jsonrpc":"2.0","result":{},"result":{},"id":1}`, want: "invalid response: ", prefix: true},
		{name: "no version", body: `{"result":{},"id":1}`, want: `invalid response: jsonrpc is "", want "2.0"`},
		{name: "other version", body: `{"jsonrpc":"1.0","result":{},"id":1}`, want: `invalid response: jsonrpc is "1.0", want "2.0"`},
		{name: "no result or error", body: `{"jsonrpc":"2.0","id":1}`, want: "invalid response: want either a result or an error"},
		{
			name: "result and error", body: `{"jsonrpc":"2.0","result":{},"error":{"code":404,"message":"m"},"id":1}`,
			want: "invalid response: want either a result or an error",
		},
		{name: "no id", body: `{"jsonrpc":"2.0","result":{}}`, want: "invalid response: no id"},
		{name: "other id", body: `{"jsonrpc":"2.0","result":{},"id":2}`, want: "invalid response: id is 2, want 1"},
		{name: "id as a string", body: `{"jsonrpc":"2.0","result":{},"id":"1"}`, want: `invalid response: id is "1", want 1`},
		{name: "null id of a result", body: `{"jsonrpc":"2.0","result":{},"id":null}`, want: "invalid response: id is null, want 1"},
		{name: "other id of an error", body: `{"jsonrpc":"2.0","error":{"code":404,"message":"m"},"id":2}`, want: "invalid response: id is 2, want 1"},
		{name: "result that doesn't fit", body: `{"jsonrpc":"2.0","result":"go","id":1}`, want: "decoding the result: ", prefix: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType := tt.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			_, err := replyClient(t, http.StatusOK, contentType, tt.body).Call(t.Context(), echoOp, thing{})
			want := "jsonrpc: things.echo: " + tt.want
			if err == nil || !tt.prefix && err.Error() != want || tt.prefix && !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Call() = %v, want %q", err, want)
			}
			if _, ok := errors.AsType[*jsonrpc.ServerError](err); ok {
				t.Errorf("Call() = %v, a *jsonrpc.ServerError", err)
			}
		})
	}
}

func TestClientHTTPErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	} {
		_, err := replyClient(t, status, "text/plain", "busy").Call(t.Context(), echoOp, thing{})
		want := fmt.Sprintf("jsonrpc: things.echo: HTTP status %d %s", status, http.StatusText(status))
		if he, ok := errors.AsType[*jsonrpc.HTTPError](err); !ok || he.StatusCode != status || err.Error() != want {
			t.Errorf("Call() = %v, want an HTTPError: %s", err, want)
		}
		if _, ok := errors.AsType[*tyr.Error](err); ok {
			t.Errorf("Call() = %v, a *tyr.Error", err)
		}
	}

	// The problems of the HTTP request that Handler sends, such as a body
	// over its limit.
	c := newClient(t, jsonrpc.Handler(echoAPI(), jsonrpc.MaxBodyBytes(10)))
	_, err := c.Call(t.Context(), echoOp, thing{Code: "a long code"})
	if he, ok := errors.AsType[*jsonrpc.HTTPError](err); !ok || he.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("Call() = %v, want an HTTPError with 413", err)
	}
}

func TestHTTPError(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusBadGateway, "HTTP status 502 Bad Gateway"},
		{599, "HTTP status 599"}, // no text
	}
	for _, tt := range tests {
		if got := (&jsonrpc.HTTPError{StatusCode: tt.status}).Error(); got != tt.want {
			t.Errorf("HTTPError{%d}.Error() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestClientExchangeErrors(t *testing.T) {
	refused := errors.New("dial tcp 10.0.0.1:80: connect: connection refused")
	transports := []struct {
		name     string
		endpoint string
		rt       roundTripFunc
		cause    error
		want     string
	}{
		{
			name: "refused", endpoint: "http://user:secret@links/rpc",
			rt:    func(r *http.Request) (*http.Response, error) { return nil, refused },
			cause: refused,
			// As http.Client has it: without the password.
			want: `jsonrpc: things.echo: Post "http://user:***@links/rpc": dial tcp 10.0.0.1:80: connect: connection refused`,
		},
		{
			name: "cut short", endpoint: "http://user:secret@links/rpc",
			rt: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(io.MultiReader(strings.NewReader(`{"jsonrpc":`), iotest.ErrReader(io.ErrUnexpectedEOF))),
				}, nil
			},
			cause: io.ErrUnexpectedEOF,
			want:  `jsonrpc: things.echo: Post "http://user:***@links/rpc": unexpected EOF`,
		},
	}
	for _, tt := range transports {
		t.Run(tt.name, func(t *testing.T) {
			c := jsonrpc.NewClient(tt.endpoint, &http.Client{Transport: tt.rt})
			_, err := c.Call(t.Context(), echoOp, thing{})
			// The Op is "Post" either way, as http.Client names the method.
			if ue, ok := errors.AsType[*url.Error](err); !ok || ue.Op != "Post" || !errors.Is(err, tt.cause) || err.Error() != tt.want {
				t.Errorf("Call() = %v, want a *url.Error of Post: %s", err, tt.want)
			}
		})
	}
}

func TestClientContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api, started := waitAPI()
		wait := tyr.Define[thing, struct{}]("things.wait")
		srv := httptest.NewTestServer(t, jsonrpc.Handler(api))
		c := jsonrpc.NewClient("http://example.com/rpc", srv.Client())
		errBye := errors.New("bye")

		// A client of its own, since the one of the server is shared.
		timed := *srv.Client()
		timed.Timeout = time.Second

		// A server that sends the start of the response and waits.
		stalling := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0",`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))

		tests := []struct {
			name   string
			call   func(ctx context.Context) error
			ctx    func() (context.Context, func())
			causes []error // that errors.Is finds
			took   time.Duration
		}{
			{
				name: "canceled",
				call: func(ctx context.Context) error { _, err := c.Call(ctx, wait, thing{}); return err },
				ctx: func() (context.Context, func()) {
					ctx, cancel := context.WithCancel(t.Context())
					go func() { <-started; time.Sleep(time.Second); cancel() }()
					return ctx, cancel
				},
				causes: []error{context.Canceled},
				took:   time.Second,
			},
			{
				name: "canceled with a cause",
				call: func(ctx context.Context) error { _, err := c.Call(ctx, wait, thing{}); return err },
				ctx: func() (context.Context, func()) {
					ctx, cancel := context.WithCancelCause(t.Context())
					go func() { <-started; time.Sleep(time.Second); cancel(errBye) }()
					return ctx, func() { cancel(nil) }
				},
				causes: []error{context.Canceled, errBye},
				took:   time.Second,
			},
			{
				name: "deadline",
				call: func(ctx context.Context) error { _, err := c.Call(ctx, wait, thing{}); return err },
				ctx: func() (context.Context, func()) {
					go func() { <-started }()
					return context.WithTimeout(t.Context(), time.Second)
				},
				causes: []error{context.DeadlineExceeded},
				took:   time.Second,
			},
			{
				name: "timeout of the client",
				call: func(ctx context.Context) error {
					_, err := jsonrpc.NewClient("http://example.com/rpc", &timed).Call(ctx, wait, thing{})
					return err
				},
				ctx: func() (context.Context, func()) {
					go func() { <-started }()
					return t.Context(), func() {}
				},
				causes: []error{context.DeadlineExceeded},
				took:   time.Second,
			},
			{
				name: "canceled while reading",
				call: func(ctx context.Context) error {
					_, err := jsonrpc.NewClient("http://example.com/rpc", stalling.Client()).Call(ctx, echoOp, thing{})
					return err
				},
				ctx: func() (context.Context, func()) {
					ctx, cancel := context.WithCancelCause(t.Context())
					go func() { time.Sleep(time.Second); cancel(errBye) }()
					return ctx, func() { cancel(nil) }
				},
				causes: []error{context.Canceled, errBye},
				took:   time.Second,
			},
		}
		for _, tt := range tests {
			ctx, cancel := tt.ctx()
			start := time.Now()
			err := tt.call(ctx)
			cancel()
			synctest.Wait()
			if took := time.Since(start); took != tt.took {
				t.Errorf("%s: the call took %v, want %v", tt.name, took, tt.took)
			}
			if _, ok := errors.AsType[*url.Error](err); !ok {
				t.Errorf("%s: Call() = %v, want a *url.Error", tt.name, err)
			}
			for _, cause := range tt.causes {
				if !errors.Is(err, cause) {
					t.Errorf("%s: Call() = %v, want errors.Is to find %v", tt.name, err, cause)
				}
			}
		}
	})
}

func TestClientCanceledLeaksNothing(t *testing.T) {
	api, started := waitAPI()
	c := newClient(t, jsonrpc.Handler(api))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	if _, err := c.Call(ctx, tyr.Define[thing, struct{}]("things.wait"), thing{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() = %v, want context.Canceled", err)
	}
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutineleak").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile.String(), "jsonrpc") {
		t.Errorf("leaked goroutines:\n%s", profile.String())
	}
}

func TestClientZeroContract(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the zero Contract was sent")
	}))
	const want = "jsonrpc: Call: zero Contract, make one with tyr.Define"
	if _, err := c.Call(t.Context(), tyr.Contract[thing, thing]{}, thing{}); err == nil || err.Error() != want {
		t.Errorf("Call() = %v, want %s", err, want)
	}
}

func TestServerErrorAsIs(t *testing.T) {
	// Another service, which rejects the credentials of the caller, tells
	// of its roles and checks its own requests.
	lookup := tyr.Define[thing, thing]("links.lookup")
	other := newAPI()
	other.Implement(lookup, func(ctx context.Context, req thing) (thing, error) {
		switch req.Code {
		case "auth":
			return thing{}, tyr.Unauthenticated("service token expired")
		case "perm":
			return thing{}, tyr.PermissionDenied("links.lookup requires the role billing")
		}
		var v tyr.Violations
		v.Add("count", "must be at least 1")
		return thing{}, v.Err()
	})
	links := newClient(t, jsonrpc.Handler(other))

	// A service whose handler returns the errors of the other as they are.
	var logs bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))))
	api.Handle("pages.show", func(ctx context.Context, req struct {
		ID string `json:"id" path:"id"`
	}) (thing, error) {
		return links.Call(ctx, lookup, thing{Code: req.ID})
	}, rest.Route("GET /pages/{id}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api, rest.Challenge(`Bearer realm="pages"`))

	// Its clients get an internal error, whatever the other answered: not
	// a 401 with its own challenge, which would make them log in again, nor
	// the roles or the violations of the other.
	for _, id := range []string{"auth", "perm", "invalid"} {
		logs.Reset()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/pages/"+id, nil))
		const internal = `{"type":"/problems/internal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}`
		if rec.Code != http.StatusInternalServerError || rec.Header().Get("WWW-Authenticate") != "" || rec.Body.String() != internal {
			t.Errorf("GET /pages/%s = %d %v %s, want 500 %s", id, rec.Code, rec.Header(), rec.Body, internal)
		}
		// The log tells what the other service answered, and who.
		if !strings.Contains(logs.String(), `"msg":"tyr: operation failed","err":"internal: internal error: jsonrpc: links.lookup: `) {
			t.Errorf("GET /pages/%s logged %s, want the answer of links.lookup", id, logs.String())
		}
	}
	const want = `{"level":"ERROR","msg":"tyr: operation failed","err":"internal: internal error: jsonrpc: links.lookup: unauthenticated (401): service token expired"}` + "\n"
	logs.Reset()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/pages/auth", nil))
	if logs.String() != want {
		t.Errorf("logged %s, want %s", logs.String(), want)
	}
}

// recipe is the mapper that the documentation of Client gives, as it is
// there.
func recipe(err error) error {
	if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok {
		if se.Kind == tyr.KindUnavailable || se.Kind == tyr.KindDeadlineExceeded {
			return tyr.Unavailable("links are unavailable").WithCause(err)
		}
		return nil // whatever else it answered is internal to us
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil // the rules of Call are right for these
	}
	// A failed exchange of Call has the Op "Post", as http.Client names
	// the method; an error of url.Parse has "parse".
	ue, isURL := errors.AsType[*url.Error](err)
	he, isHTTP := errors.AsType[*jsonrpc.HTTPError](err)
	if (isURL && ue.Op == "Post") || (isHTTP && he.StatusCode >= 502 && he.StatusCode <= 504) {
		return tyr.Unavailable("links are unavailable").WithCause(err)
	}
	return nil
}

// TestMapErrorRecipe runs recipe for a handler that returns the error of a
// call of another service as it is.
func TestMapErrorRecipe(t *testing.T) {
	over := func(rt roundTripFunc) func(*testing.T) *jsonrpc.Client {
		return func(*testing.T) *jsonrpc.Client {
			return jsonrpc.NewClient("http://links.internal/rpc", &http.Client{Transport: rt})
		}
	}
	replying := func(status int, body string) func(*testing.T) *jsonrpc.Client {
		return func(t *testing.T) *jsonrpc.Client {
			return replyClient(t, status, "application/json", body)
		}
	}
	tests := []struct {
		name   string
		client func(*testing.T) *jsonrpc.Client // nil: the handler fails to parse a URL instead
		kind   tyr.Kind
	}{
		{"502", replying(http.StatusBadGateway, ""), tyr.KindUnavailable},
		{"503", replying(http.StatusServiceUnavailable, ""), tyr.KindUnavailable},
		{"504", replying(http.StatusGatewayTimeout, ""), tyr.KindUnavailable},
		{"refused", over(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}), tyr.KindUnavailable},
		{"cut short", over(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)),
			}, nil
		}), tyr.KindUnavailable},
		{"404", replying(http.StatusNotFound, ""), tyr.KindInternal},
		{"invalid response", replying(http.StatusOK, `{"jsonrpc":"2.0"}`), tyr.KindInternal},
		// What the service answered is internal but its failures, which
		// its clients may retry.
		{
			"answered not_found",
			replying(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":404,"message":"link not found","data":{"kind":"not_found"}},"id":1}`),
			tyr.KindInternal,
		},
		{
			"answered unavailable",
			replying(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":503,"message":"try later","data":{"kind":"unavailable"}},"id":1}`),
			tyr.KindUnavailable,
		},
		{
			"answered deadline_exceeded",
			replying(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":504,"message":"took too long","data":{"kind":"deadline_exceeded"}},"id":1}`),
			tyr.KindUnavailable,
		},
		// Not an exchange of a call: url.Parse names its Op "parse".
		{"url.Parse", nil, tyr.KindInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var links *jsonrpc.Client
			if tt.client != nil {
				links = tt.client(t)
			}
			api := newAPI()
			api.MapError(recipe)
			resolve := api.Handle("pages.resolve", func(ctx context.Context, req thing) (thing, error) {
				if links == nil {
					_, err := url.Parse("http://links/" + req.Code)
					return thing{}, err
				}
				return links.Call(ctx, echoOp, req)
			})

			// A code that url.Parse rejects: an invalid escape.
			_, err := resolve.Call(t.Context(), func(dst any) error {
				dst.(*thing).Code = "%zz"
				return nil
			})
			if e, ok := errors.AsType[*tyr.Error](err); !ok || e.Kind != tt.kind {
				t.Errorf("Call() = %v, want %v", err, tt.kind)
			}
		})
	}

	// The error of a context that is done passes the mapper by.
	synctest.Test(t, func(t *testing.T) {
		waiting, started := waitAPI()
		links := newClient(t, jsonrpc.Handler(waiting))
		api := newAPI()
		api.MapError(recipe)
		resolve := api.Handle("pages.resolve", func(ctx context.Context, req thing) (struct{}, error) {
			return links.Call(ctx, tyr.Define[thing, struct{}]("things.wait"), req)
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		go func() { <-started }()
		if _, err := resolve.Call(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Call() = %v, want deadline_exceeded", err)
		} else if e, _ := errors.AsType[*tyr.Error](err); e.Kind != tyr.KindDeadlineExceeded {
			t.Errorf("Call() = %v, want deadline_exceeded", err)
		}
	})
}

// tokenKey is a key of the context of a call, for TestHeaders.
type tokenKey struct{}

func TestHeaders(t *testing.T) {
	type sent struct {
		authorization, contentType, accept string
		tenant, requestID                  []string
	}
	requests := make(chan sent, 2)
	c := jsonrpc.NewClient("http://example.com/rpc", httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- sent{r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Accept"),
			r.Header.Values("X-Tenant"), r.Header.Values("X-Request-ID")}
		var req struct {
			ID jsontext.Value `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{},"id":`+string(req.ID)+`}`)
	})).Client(),
		// The token of the context of the call, and headers that the
		// client sets itself, which it keeps.
		jsonrpc.Headers(func(ctx context.Context, h http.Header) error {
			if token, ok := ctx.Value(tokenKey{}).(string); ok {
				h.Set("Authorization", "Bearer "+token)
			}
			h.Set("Content-Type", "text/plain")
			h.Set("Accept", "text/html")
			h.Set("X-Request-ID", "from-headers")
			h.Set("X-Tenant", "acme")
			return nil
		}),
		// The functions run in their order.
		jsonrpc.Headers(func(ctx context.Context, h http.Header) error {
			h.Add("X-Tenant", "globex")
			return nil
		}),
	)

	ctx := context.WithValue(t.Context(), tokenKey{}, "alice")
	for _, ctx := range []context.Context{ctx, tyr.WithRequestID(ctx, "req-1")} {
		if _, err := c.Call(ctx, echoOp, thing{Code: "go"}); err != nil {
			t.Fatal(err)
		}
	}
	close(requests)
	want := []sent{
		{"Bearer alice", "application/json", "application/json", []string{"acme", "globex"}, []string{"from-headers"}},
		{"Bearer alice", "application/json", "application/json", []string{"acme", "globex"}, []string{"req-1"}},
	}
	i := 0
	for got := range requests {
		if w := want[i]; got.authorization != w.authorization || got.contentType != w.contentType || got.accept != w.accept ||
			!slices.Equal(got.tenant, w.tenant) || !slices.Equal(got.requestID, w.requestID) {
			t.Errorf("request %d = %+v, want %+v", i, got, w)
		}
		i++
	}
}

func TestHeadersError(t *testing.T) {
	// An error of the headers fails the call, and nothing is sent.
	var sent atomic.Bool
	errNoToken := errors.New("no token")
	c := jsonrpc.NewClient("http://example.com/rpc", httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Store(true)
	})).Client(), jsonrpc.Headers(func(ctx context.Context, h http.Header) error { return errNoToken }))

	_, err := c.Call(t.Context(), echoOp, thing{Code: "go"})
	if !errors.Is(err, errNoToken) || err.Error() != "jsonrpc: things.echo: headers: no token" || sent.Load() {
		t.Errorf("Call() error = %v, sent %v; want jsonrpc: things.echo: headers: no token, nothing sent", err, sent.Load())
	}
	if _, ok := errors.AsType[*jsonrpc.ServerError](err); ok {
		t.Errorf("Call() error = %v, want no ServerError: the server answered nothing", err)
	}

	defer func() {
		if got := recover(); got != "jsonrpc: Headers: nil function" {
			t.Errorf("Headers(nil) panicked with %v", got)
		}
	}()
	jsonrpc.Headers(nil)
}
