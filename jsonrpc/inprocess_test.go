package jsonrpc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
	"github.com/tyr-go/tyr/jsonrpc"
)

func TestInProcessPanicsOnNil(t *testing.T) {
	const want = "jsonrpc: InProcess: nil handler"
	if got := panicValue(func() { jsonrpc.InProcess(nil) }); got != want {
		t.Errorf("InProcess(nil) panicked with %v, want %q", got, want)
	}
}

// trackedBody is a request body that tells whether it was closed.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestInProcessRequest(t *testing.T) {
	caller := ctxkey.New[string]("test.caller")
	var got *http.Request
	var body string
	hc := jsonrpc.InProcess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))

	for _, target := range []string{"https://links.internal/rpc?v=2", "http://links.internal/rpc?v=2"} {
		ctx, cancel := context.WithTimeout(caller.Set(t.Context(), "admin"), time.Minute)
		sent := &trackedBody{Reader: strings.NewReader("hi")}
		req, err := http.NewRequestWithContext(ctx, "POST", target, sent)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = 2
		req.Header.Set("X-Token", "t")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()

		// The request as a server gets it, with the remote address of
		// httptest.NewRequest, which net.SplitHostPort takes apart.
		if host, port, err := net.SplitHostPort(got.RemoteAddr); err != nil || host != "192.0.2.1" || port != "1234" {
			t.Errorf("%s: RemoteAddr = %q, want 192.0.2.1:1234", target, got.RemoteAddr)
		}
		if got.Method != "POST" || got.URL.String() != "/rpc?v=2" || got.RequestURI != "/rpc?v=2" ||
			got.Host != "links.internal" || got.Proto != "HTTP/1.1" || got.Header.Get("X-Token") != "t" ||
			got.ContentLength != 2 || body != "hi" || !sent.closed {
			t.Errorf("%s: the handler got %s %s (%s) of %s, %s, X-Token %q, %d bytes %q; body closed: %v",
				target, got.Method, got.URL, got.RequestURI, got.Host, got.Proto, got.Header.Get("X-Token"), got.ContentLength, body, sent.closed)
		}
		if https := strings.HasPrefix(target, "https:"); https != (got.TLS != nil) || https && got.TLS.ServerName != "links.internal" {
			t.Errorf("%s: TLS = %+v", target, got.TLS)
		}
		// It isn't a network: the values and the deadline of the context of
		// the caller reach the handler.
		if who, _ := caller.Get(got.Context()); who != "admin" {
			t.Errorf("%s: the context of the handler has caller %q, want admin", target, who)
		}
		want, _ := ctx.Deadline()
		if d, ok := got.Context().Deadline(); !ok || !d.Equal(want) {
			t.Errorf("%s: the context of the handler has deadline %v, %v; want %v", target, d, ok, want)
		}
		cancel()
	}
}

func TestInProcessResponse(t *testing.T) {
	tests := []struct {
		name   string
		method string // GET if empty
		h      http.HandlerFunc
		status string
		header http.Header // those to check, nil for absent ones
		body   string
	}{
		{
			name:   "nothing sent",
			h:      func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Set", "1") },
			status: "200 OK", header: http.Header{"X-Set": {"1"}},
		},
		{
			name: "body",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
			},
			status: "200 OK", header: http.Header{"Content-Type": {"application/json"}}, body: `{}`,
		},
		{
			name:   "sniffed Content-Type",
			h:      func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html></html>") },
			status: "200 OK", header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, body: "<html></html>",
		},
		{
			name: "headers after the start",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Early", "1")
				w.WriteHeader(http.StatusCreated)
				w.Header().Set("X-Late", "1")
			},
			status: "201 Created", header: http.Header{"X-Early": {"1"}, "X-Late": nil},
		},
		{
			name: "flushed",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				w.Header().Set("X-Late", "1")
				_, _ = io.WriteString(w, "x")
			},
			status: "200 OK", header: http.Header{"X-Late": nil}, body: "x",
		},
		{
			name: "informational first",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusAccepted)
			},
			status: "202 Accepted",
		},
		{
			name: "status twice",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				w.WriteHeader(http.StatusInternalServerError)
			},
			status: "202 Accepted",
		},
		{
			name: "no body allowed",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
				if _, err := io.WriteString(w, "x"); err != http.ErrBodyNotAllowed {
					t.Errorf("Write() = %v, want http.ErrBodyNotAllowed", err)
				}
			},
			status: "204 No Content",
		},
		{
			name: "HEAD", method: "HEAD",
			h:      func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") },
			status: "200 OK",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = "GET"
			}
			req, err := http.NewRequestWithContext(t.Context(), method, "http://links/", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := jsonrpc.InProcess(tt.h).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.Status != tt.status || string(body) != tt.body || resp.ContentLength != int64(len(tt.body)) {
				t.Errorf("response = %s, %d bytes %q; want %s, %q", resp.Status, resp.ContentLength, body, tt.status, tt.body)
			}
			for name, want := range tt.header {
				if got := resp.Header.Values(name); strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestInProcessPanics(t *testing.T) {
	// A panic goes on in the goroutine of the call, as that of a function
	// call does.
	boom := jsonrpc.InProcess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	if got := panicValue(func() { _, _ = boom.Get("http://links/") }); got != "boom" {
		t.Errorf("Get() panicked with %v, want boom", got)
	}

	// http.ErrAbortHandler fails the request, as a connection that breaks
	// does.
	abort := jsonrpc.InProcess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic(http.ErrAbortHandler)
	}))
	resp, err := abort.Get("http://links/")
	if _, ok := errors.AsType[*url.Error](err); !ok || !errors.Is(err, http.ErrAbortHandler) || resp != nil {
		t.Errorf("Get() = %v, %v; want a *url.Error with http.ErrAbortHandler", resp, err)
	}
}

func TestInProcessCanceled(t *testing.T) {
	// As over a network, a request with a context that is done isn't sent.
	hc := jsonrpc.InProcess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the handler got a request with a context that is done")
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://links/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hc.Do(req); !errors.Is(err, context.Canceled) {
		t.Errorf("Do() = %v, want context.Canceled", err)
	}
}

func TestInProcessCall(t *testing.T) {
	// The whole lifecycle of a call: interceptors, validation, errors.
	var calls []string
	api := echoAPI()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		id, _ := tyr.RequestIDFrom(ctx)
		calls = append(calls, op.Name()+" "+id)
		return next(ctx, req)
	})
	c := jsonrpc.NewClient("http://links/rpc", jsonrpc.InProcess(jsonrpc.Handler(api)))

	// The request of another handler, whose transport recorded what
	// serves it.
	outer := tyr.New().Handle("pages.get", func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	ctx, info := tyr.WithRequestInfo(tyr.WithRequestID(t.Context(), "0192f5e2"))
	info.Record("GET /pages/{id}", outer)

	got, err := c.Call(ctx, echoOp, thing{Code: "go", Count: 2})
	if want := (thing{Code: "go", Count: 2}); got != want || err != nil {
		t.Errorf("Call(things.echo) = %+v, %v; want %+v, <nil>", got, err, want)
	}
	_, err = c.Call(ctx, tyr.Define[struct{}, struct{}]("things.check"), struct{}{})
	if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindInvalidArgument {
		t.Errorf("Call(things.check) = %v, want invalid_argument", err)
	}

	if want := []string{"things.echo 0192f5e2", "things.check 0192f5e2"}; strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	// Only the first Record counts: the calls in process leave the route
	// and the operation of the request of the handler.
	if op, _ := info.Operation(); info.Route() != "GET /pages/{id}" || op != outer {
		t.Errorf("RequestInfo = %q, %v; want GET /pages/{id}, pages.get", info.Route(), op)
	}
}

func TestInProcessAuthentication(t *testing.T) {
	// Authentication is middleware, so it happens in process only as part
	// of the handler, and a test sends the headers that clients send.
	caller := ctxkey.New[string]("test.caller")
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer secret" {
				r = r.WithContext(caller.Set(r.Context(), "admin"))
			}
			next.ServeHTTP(w, r)
		})
	}
	api := echoAPI()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		if _, ok := caller.Get(ctx); !ok {
			return nil, tyr.Unauthenticated("log in first")
		}
		return next(ctx, req)
	})
	mux := http.NewServeMux()
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	server := authenticate(mux)

	hc := jsonrpc.InProcess(server)
	_, err := jsonrpc.NewClient("http://links/rpc", hc).Call(t.Context(), echoOp, thing{})
	if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindUnauthenticated {
		t.Errorf("Call() without a token = %v, want unauthenticated", err)
	}

	// The header goes through a Transport that wraps that of InProcess.
	hc = jsonrpc.InProcess(server)
	next := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer secret")
		return next.RoundTrip(r)
	})
	if _, err := jsonrpc.NewClient("http://links/rpc", hc).Call(t.Context(), echoOp, thing{}); err != nil {
		t.Errorf("Call() with a token = %v, want <nil>", err)
	}
}

func BenchmarkClient(b *testing.B) {
	// The handler allocates nothing: what's measured is the client, the
	// handler of jsonrpc and InProcess.
	res := thing{Code: "go", Count: 1}
	get := tyr.Define[thing, thing]("things.get")
	api := tyr.New()
	api.Implement(get, func(ctx context.Context, req thing) (thing, error) {
		return res, nil
	})
	c := jsonrpc.NewClient("http://links/rpc", jsonrpc.InProcess(jsonrpc.Handler(api)))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Call(ctx, get, thing{Code: "go"}); err != nil {
			b.Fatal(err)
		}
	}
}
