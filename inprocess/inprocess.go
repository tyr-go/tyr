// Package inprocess serves HTTP requests with a handler in memory, for tests
// and for calls within one program, such as those of the typed client of
// [github.com/tyr-go/tyr/jsonrpc]:
//
//	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(api)))
//
// It is not a network, but the handler gets what it would get over one: the
// request, with its headers, and a context with the deadline and the
// cancellation of the context of the caller, but none of its values.
package inprocess

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Client returns an HTTP client that serves its requests with h, in memory,
// for tests and for calls within one program:
//
//	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(api)))
//
// h gets what a server over a network gets: the request with its headers,
// and a context with the deadline and the cancellation of the context of
// the caller, but none of its values. So authentication happens only by
// the headers, which a Transport of the client that wraps its own sets, and
// a caller in the context of the caller doesn't reach h: test
// authentication through the whole handler of the server, with the headers
// that clients send. Likewise, the request ID of a jsonrpc.Client reaches
// the context of h in its X-Request-ID header, by middleware.RequestID, as
// it does over a network.
//
// As in a request of httptest.NewRequest, the remote address is
// 192.0.2.1:1234, of a network for documentation (RFC 5737), which
// middleware can take apart with net.SplitHostPort, and a request to an
// https URL has a TLS connection state. The response is made in memory, as
// net/http would send it: with the headers as they are when the response
// starts, 200 if h sends no status, and a Content-Type from the body if h
// sets none. h runs in the goroutine of the call; it can't hijack the
// connection, and nothing reaches the client before h returns. A panic of
// h, other than [http.ErrAbortHandler], goes on in the goroutine of the
// call, as that of a function call does; ErrAbortHandler fails the
// request, as a connection that breaks does.
//
// Client panics if h is nil.
func Client(h http.Handler) *http.Client {
	if h == nil {
		panic("inprocess: Client: nil handler")
	}
	return &http.Client{Transport: transport{h}}
}

// transport is the http.RoundTripper of Client.
type transport struct {
	h http.Handler
}

// remoteAddr is the remote address of a request of Client: that of
// httptest.NewRequest, in TEST-NET-1 of RFC 5737.
const remoteAddr = "192.0.2.1:1234"

func (t transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	body := req.Body
	if body == nil {
		body = http.NoBody
	}
	defer func() { _ = body.Close() }() // RoundTrip must close it
	if req.Context().Err() != nil {
		return nil, context.Cause(req.Context()) // as net/http: nothing is sent
	}
	header := req.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	// The request as a server gets it.
	r := (&http.Request{
		Method:        req.Method,
		URL:           &url.URL{Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          body,
		ContentLength: req.ContentLength,
		Host:          cmp.Or(req.Host, req.URL.Host),
		RemoteAddr:    remoteAddr,
		RequestURI:    req.URL.RequestURI(),
	}).WithContext(detached{req.Context()})
	if req.URL.Scheme == "https" {
		r.TLS = &tls.ConnectionState{Version: tls.VersionTLS12, HandshakeComplete: true, ServerName: r.Host}
	}

	w := &recorder{header: make(http.Header), head: req.Method == http.MethodHead}
	defer func() {
		if v := recover(); v != nil {
			if v != http.ErrAbortHandler {
				panic(v)
			}
			resp, err = nil, fmt.Errorf("inprocess: the handler aborted the response: %w", http.ErrAbortHandler)
		}
	}()
	t.h.ServeHTTP(w, r)
	return w.response(req), nil
}

// detached is the context of a request that Client serves: the context of
// the caller without its values, as a server over a network has none of
// them, but with its deadline and cancellation.
type detached struct {
	context.Context
}

// Value returns nil: no value of the context of the caller reaches the
// handler.
func (detached) Value(key any) any {
	return nil
}

// AfterFunc arranges to call f once the context of the caller is done, so
// that the contexts that the handler derives from c stop with it without a
// goroutine each: the context package takes the method from c.
func (c detached) AfterFunc(f func()) (stop func() bool) {
	return context.AfterFunc(c.Context, f)
}

// recorder is the http.ResponseWriter of Client: it keeps the response in
// memory, as net/http would send it.
type recorder struct {
	header http.Header // that the handler sets
	sent   http.Header // as of the start of the response
	status int         // 0 until the response starts
	body   bytes.Buffer
	head   bool // the request is a HEAD: no body is sent
}

func (w *recorder) Header() http.Header {
	return w.header
}

func (w *recorder) WriteHeader(code int) {
	if code < 100 || code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code)) // as net/http panics
	}
	if w.status != 0 || code < 200 && code != http.StatusSwitchingProtocols {
		return // a response started, or an informational one: not final
	}
	w.status = code
	w.sent = w.header.Clone()
}

func (w *recorder) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK) // if the response hasn't started
	switch {
	case !bodyAllowed(w.status):
		return 0, http.ErrBodyNotAllowed
	case w.head:
		return len(p), nil
	}
	return w.body.Write(p)
}

// Flush starts the response, as net/http does. The client gets it when
// the handler returns.
func (w *recorder) Flush() {
	w.WriteHeader(http.StatusOK)
}

// response returns the response to req that the handler made.
func (w *recorder) response(req *http.Request) *http.Response {
	w.WriteHeader(http.StatusOK) // if the handler sent nothing
	if _, ok := w.sent["Content-Type"]; !ok && w.body.Len() > 0 {
		w.sent.Set("Content-Type", http.DetectContentType(w.body.Bytes()[:min(w.body.Len(), 512)]))
	}
	return &http.Response{
		Status:        strconv.Itoa(w.status) + " " + http.StatusText(w.status),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.sent,
		Body:          io.NopCloser(bytes.NewReader(w.body.Bytes())),
		ContentLength: int64(w.body.Len()),
		Request:       req,
	}
}

// bodyAllowed reports whether a response with status may have a body.
func bodyAllowed(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}
