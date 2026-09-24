package middleware_test

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr/middleware"
)

// wrapped returns h behind Logger and Recover, which both wrap the writer.
func wrapped(h http.HandlerFunc) http.Handler {
	discard := slog.New(slog.DiscardHandler)
	return middleware.Chain(h, middleware.Logger(discard), middleware.Recover(discard))
}

func TestFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		next := make(chan struct{})
		client, _ := serve(t, wrapped(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: 1\n\n")
			w.(http.Flusher).Flush() // as old code flushes
			<-next
			_, _ = io.WriteString(w, "data: 2\n\n")
		}))

		// The first event comes while the handler waits: if it didn't, the
		// bubble would deadlock.
		resp, err := client.Get("http://example.com/")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body := bufio.NewReader(resp.Body)
		first, err := body.ReadString('\n')
		if first != "data: 1\n" || err != nil {
			t.Fatalf("first line = %q, %v; want data: 1", first, err)
		}
		close(next)
		rest, err := io.ReadAll(body)
		if string(rest) != "\ndata: 2\n\n" || err != nil {
			t.Errorf("rest = %q, %v; want data: 2", rest, err)
		}
	})
}

// plainWriter is an http.ResponseWriter without optional methods, which
// can't flush.
type plainWriter struct {
	header http.Header
	status int
}

func (w *plainWriter) Header() http.Header {
	return w.header
}

func (w *plainWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (w *plainWriter) WriteHeader(code int) {
	w.status = code
}

func TestFlushError(t *testing.T) {
	w := &plainWriter{header: make(http.Header)}
	h := wrapped(func(w http.ResponseWriter, r *http.Request) {
		// The error of the writer below reaches http.ResponseController.
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("Flush() error = %v, want http.ErrNotSupported", err)
		}
		w.(http.Flusher).Flush()
		panic("boom")
	})
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	// A flush that fails doesn't start the response.
	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 from Recover", w.status)
	}
}

func TestUnwrap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, _ := serve(t, wrapped(func(w http.ResponseWriter, r *http.Request) {
			// http.ResponseController gets to the writer of net/http.
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
				t.Errorf("SetWriteDeadline() error = %v", err)
			}
		}))
		resp, err := client.Get("http://example.com/")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		_ = resp.Body.Close()
	})
}

func TestHijack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := &logs{}
		logger := slog.New(logs)
		// An echo protocol, upgraded to the way websocket libraries do: they
		// find http.Hijacker by a type assertion.
		client, errs := serve(t, middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("the writer isn't an http.Hijacker")
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				t.Errorf("Hijack() error = %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
			_ = buf.Flush()
			line, _ := buf.ReadString('\n')
			_, _ = buf.WriteString(line)
			_ = buf.Flush()
		}), middleware.RequestID(), middleware.Logger(logger), middleware.Recover(logger)))

		req, _ := http.NewRequest("GET", "http://example.com/echo", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "echo")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		conn, ok := resp.Body.(io.ReadWriteCloser)
		if resp.StatusCode != http.StatusSwitchingProtocols || !ok {
			t.Fatalf("response = %d, body %T; want 101 and a connection", resp.StatusCode, resp.Body)
		}
		_, _ = io.WriteString(conn, "ping\n")
		if line, err := bufio.NewReader(conn).ReadString('\n'); line != "ping\n" {
			t.Errorf("echo = %q, %v; want ping", line, err)
		}
		synctest.Wait()

		// The access log has no status: the handler sent it itself.
		want := map[string]string{"method": "GET", "route": "", "duration": "0s", "hijacked": "true"}
		if l := logs.get(); len(l) != 1 || l[0].msg != "middleware: request" || !maps.Equal(l[0].attrs, want) {
			t.Errorf("logged %+v, want one middleware: request with %v", l, want)
		}
		if e := errs.get(); len(e) != 0 {
			t.Errorf("the server logged %+v, want nothing", e)
		}
	})
}

func TestNoHijack(t *testing.T) {
	// A writer without Hijack, as the one of HTTP/2, gets a wrapper without it.
	check := func(t *testing.T, w http.ResponseWriter) {
		if _, ok := w.(http.Hijacker); ok {
			t.Error("the writer is an http.Hijacker")
		}
		if _, _, err := http.NewResponseController(w).Hijack(); !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("Hijack() error = %v, want http.ErrNotSupported", err)
		}
	}
	t.Run("recorder", func(t *testing.T) {
		wrapped(func(w http.ResponseWriter, r *http.Request) {
			check(t, w)
		}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	})
	t.Run("HTTP/2", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv := httptest.NewTestServer(t, wrapped(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("the request came over %s, want HTTP/2", r.Proto)
				}
				check(t, w)
			}))
			srv.EnableHTTP2 = true
			resp, err := srv.Client().Get("https://example.com/")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			_ = resp.Body.Close()
		})
	})
}
