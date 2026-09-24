package inprocess_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
	"github.com/tyr-go/tyr/inprocess"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/middleware"
)

// panicValue returns what f panics with, nil if it doesn't.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

func TestClientPanicsOnNil(t *testing.T) {
	const want = "inprocess: Client: nil handler"
	if got := panicValue(func() { inprocess.Client(nil) }); got != want {
		t.Errorf("Client(nil) panicked with %v, want %q", got, want)
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

func TestRequest(t *testing.T) {
	caller := ctxkey.New[string]("test.caller")
	var got *http.Request
	var body string
	hc := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// As over a network: the deadline of the context of the caller
		// reaches the handler, and none of its values.
		if who, ok := caller.Get(got.Context()); ok {
			t.Errorf("%s: the context of the handler has caller %q, want none", target, who)
		}
		want, _ := ctx.Deadline()
		if d, ok := got.Context().Deadline(); !ok || !d.Equal(want) {
			t.Errorf("%s: the context of the handler has deadline %v, %v; want %v", target, d, ok, want)
		}
		cancel()
	}
}

func TestCanceledWhileServed(t *testing.T) {
	// The cancellation of the context of the caller reaches the handler,
	// and the contexts it derives from its own.
	started := make(chan struct{})
	var err, derived error
	hc := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		close(started)
		<-ctx.Done()
		err, derived = r.Context().Err(), ctx.Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	req, e := http.NewRequestWithContext(ctx, "GET", "http://links/", nil)
	if e != nil {
		t.Fatal(e)
	}
	if resp, e := hc.Do(req); e == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(derived, context.Canceled) {
		t.Errorf("the contexts of the handler are done with %v and %v, want context.Canceled", err, derived)
	}
}

func TestDerivedContexts(t *testing.T) {
	// The contexts that the handler derives from its own follow that of the
	// caller without a goroutine each.
	var before, during int
	hc := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		before = runtime.NumGoroutine()
		for range 100 {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			_ = ctx
		}
		during = runtime.NumGoroutine()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://links/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if during > before+10 {
		t.Errorf("100 contexts derived in the handler took %d goroutines", during-before)
	}
}

func TestAuthentication(t *testing.T) {
	// Authentication happens by the headers only, as over a network: a
	// caller in the context of the caller of the client doesn't reach the
	// handler, so a test that authenticates through the context fails, as
	// its calls would over a network.
	caller := ctxkey.New[string]("test.caller")
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer secret" {
				r = r.WithContext(caller.Set(r.Context(), "admin"))
			}
			next.ServeHTTP(w, r)
		})
	}
	server := authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := caller.Get(r.Context()); !ok {
			http.Error(w, "log in first", http.StatusUnauthorized)
		}
	}))
	hc := inprocess.Client(server)
	get := func(ctx context.Context, token string) int {
		req, err := http.NewRequestWithContext(ctx, "GET", "http://links/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := get(caller.Set(t.Context(), "admin"), ""); code != http.StatusUnauthorized {
		t.Errorf("a request without a token, with a caller in the context, got %d, want 401", code)
	}
	if code := get(t.Context(), "secret"); code != http.StatusOK {
		t.Errorf("a request with the token got %d, want 200", code)
	}
}

func TestRequestOfARequest(t *testing.T) {
	// A request made within the request of another handler, with the
	// context of that one, is a request of its own: its access log has
	// its route and operation, and the RequestInfo of the other stays as
	// its transport recorded it.
	purge := tyr.Define[struct{}, int]("links.purge")
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
	api.Implement(purge, func(ctx context.Context, req struct{}) (int, error) { return 2, nil })
	mux := http.NewServeMux()
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	var logs bytes.Buffer
	server := middleware.Chain(mux, middleware.Logger(slog.New(slog.NewJSONHandler(&logs, nil))))

	outer, info := tyr.WithRequestInfo(t.Context())
	info.Record("POST /pages/{id}", nil)
	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(server))
	if n, err := c.Call(outer, purge, struct{}{}); n != 2 || err != nil {
		t.Fatalf("Call() = %d, %v; want 2", n, err)
	}

	var record struct {
		Route     string `json:"route"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil || record.Route != "POST /rpc" || record.Operation != "links.purge" {
		t.Errorf("the access log of the request = %s, want route POST /rpc and operation links.purge", logs.Bytes())
	}
	if op, ok := info.Operation(); info.Route() != "POST /pages/{id}" || ok {
		t.Errorf("RequestInfo of the outer request = %q, %v; want POST /pages/{id} without an operation", info.Route(), op)
	}
}

func TestResponse(t *testing.T) {
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
			resp, err := inprocess.Client(tt.h).Do(req)
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

func TestPanics(t *testing.T) {
	// A panic goes on in the goroutine of the call, as that of a function
	// call does.
	boom := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	if got := panicValue(func() { _, _ = boom.Get("http://links/") }); got != "boom" {
		t.Errorf("Get() panicked with %v, want boom", got)
	}

	// http.ErrAbortHandler fails the request, as a connection that breaks
	// does.
	abort := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic(http.ErrAbortHandler)
	}))
	resp, err := abort.Get("http://links/")
	if _, ok := errors.AsType[*url.Error](err); !ok || !errors.Is(err, http.ErrAbortHandler) || resp != nil {
		t.Errorf("Get() = %v, %v; want a *url.Error with http.ErrAbortHandler", resp, err)
	}
}

func TestCanceled(t *testing.T) {
	// As over a network, a request with a context that is done isn't sent.
	hc := inprocess.Client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
