package middleware_test

import (
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/middleware"
)

const internalError = `{"type":"about:blank","title":"Internal Server Error","status":500}`

func TestRecover(t *testing.T) {
	logs := &logs{}
	h := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers for the response the handler fails to make.
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Set-Cookie", "session=1")
		panic("boom")
	}), middleware.RequestID(), middleware.Recover(slog.New(tyr.NewLogHandler(logs))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	// The headers of the middleware above Recover stay.
	id := rec.Header().Get("X-Request-ID")
	want := http.Header{"X-Request-Id": {id}, "Content-Type": {"application/problem+json"}}
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != internalError || !maps.EqualFunc(rec.Header(), want, slices.Equal) {
		t.Errorf("response = %d %v %s, want 500 %v %s", rec.Code, rec.Header(), rec.Body, want, internalError)
	}

	got := logs.get()
	if len(got) != 1 || got[0].level != slog.LevelError || got[0].msg != "middleware: panic" {
		t.Fatalf("logged %+v, want one middleware: panic at ERROR", got)
	}
	attrs := got[0].attrs
	if attrs["panic"] != "boom" || attrs["request_id"] != id || !strings.Contains(attrs["stack"], "middleware_test.TestRecover") {
		t.Errorf("logged %v, want the panic, the request ID %s and a stack from the handler", attrs, id)
	}
}

func TestRecoverStarted(t *testing.T) {
	logs := &logs{}
	h := middleware.Recover(slog.New(logs))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	got := panicValue(func() { h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil)) })

	if got != http.ErrAbortHandler {
		t.Errorf("Recover panicked with %v, want http.ErrAbortHandler", got)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Errorf("response = %d %s, want the 200 partial the handler started", rec.Code, rec.Body)
	}
	if l := logs.get(); len(l) != 1 || l[0].msg != "middleware: panic" {
		t.Errorf("logged %+v, want one middleware: panic", l)
	}
}

func TestRecoverAbort(t *testing.T) {
	logs := &logs{}
	h := middleware.Recover(slog.New(logs))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	rec := httptest.NewRecorder()
	got := panicValue(func() { h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil)) })

	if got != http.ErrAbortHandler || rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("Recover panicked with %v, response %d %q; want http.ErrAbortHandler going through", got, rec.Code, rec.Body)
	}
	if l := logs.get(); len(l) != 0 {
		t.Errorf("logged %+v, want nothing", l)
	}
}

func TestRecoverOverServer(t *testing.T) {
	t.Run("breaks a started response", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			logs := &logs{}
			logger := slog.New(logs)
			client, errs := serve(t, middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "partial")
				w.(http.Flusher).Flush()
				panic("boom")
			}), middleware.Logger(logger), middleware.Recover(logger)))

			resp, err := client.Get("http://example.com/")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			synctest.Wait()

			// The client can tell the response is broken.
			if resp.StatusCode != http.StatusOK || string(body) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("response = %d %q, error %v; want 200 partial, unexpected EOF", resp.StatusCode, body, err)
			}
			l := logs.get()
			if len(l) != 2 || l[0].msg != "middleware: panic" || l[1].attrs["status"] != "200" || l[1].attrs["aborted"] != "true" {
				t.Errorf("logged %+v, want the panic, then the request, aborted", l)
			}
			if e := errs.get(); len(e) != 0 {
				t.Errorf("the server logged %+v, want nothing", e)
			}
		})
	})
	t.Run("after 1xx", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client, _ := serve(t, middleware.Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", "</style.css>; rel=preload")
				w.WriteHeader(http.StatusEarlyHints)
				panic("boom")
			})))

			resp, err := client.Get("http://example.com/")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError || string(body) != internalError || resp.Header.Get("Link") != "" {
				t.Errorf("response = %d %v %s, want 500 %s without the Link of the 103", resp.StatusCode, resp.Header, body, internalError)
			}
		})
	})
	t.Run("after hijacking", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			logs := &logs{}
			logger := slog.New(logs)
			client, errs := serve(t, middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("Hijack() error = %v", err)
					return
				}
				_ = conn.Close()
				panic("boom")
			}), middleware.Logger(logger), middleware.Recover(logger)))

			if resp, err := client.Get("http://example.com/"); err == nil {
				_ = resp.Body.Close()
				t.Errorf("Get() = %d, want an error: the handler closed the connection", resp.StatusCode)
			}
			synctest.Wait()

			// Recover doesn't write to the hijacked connection, which net/http
			// would log.
			if e := errs.get(); len(e) != 0 {
				t.Errorf("the server logged %+v, want nothing", e)
			}
			l := logs.get()
			if len(l) != 2 || l[0].msg != "middleware: panic" || l[1].attrs["hijacked"] != "true" || l[1].attrs["aborted"] != "true" {
				t.Errorf("logged %+v, want the panic, then the request, hijacked and aborted", l)
			}
		})
	})
}
