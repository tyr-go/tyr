package middleware_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/middleware"
)

func ExampleChain() {
	// An application would log the time, to stderr. This logger drops the
	// time, the duration and the stack to keep the output stable.
	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == "duration" || a.Key == "stack") {
				return slog.Attr{}
			}
			return a
		},
	})))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /links/{code}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") == "bug" {
			panic("index out of range")
		}
		_, _ = io.WriteString(w, "https://go.dev")
	})
	handler := middleware.Chain(mux, // first = outermost
		middleware.RequestID(),
		middleware.Logger(logger),
		middleware.Recover(logger),
	)

	for _, code := range []string{"go", "bug"} {
		req := httptest.NewRequest("GET", "/links/"+code, nil)
		req.Header.Set("X-Request-ID", "req-"+code)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Header().Get("X-Request-ID"), rec.Body)
	}
	// Output:
	// {"level":"INFO","msg":"middleware: request","request_id":"req-go","method":"GET","route":"GET /links/{code}","status":200}
	// 200 req-go https://go.dev
	// {"level":"ERROR","msg":"middleware: panic","request_id":"req-bug","panic":"index out of range"}
	// {"level":"INFO","msg":"middleware: request","request_id":"req-bug","method":"GET","route":"GET /links/{code}","status":500}
	// 500 req-bug {"type":"about:blank","title":"Internal Server Error","status":500}
}

func ExampleCORS() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /links", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/links/go")
		w.WriteHeader(http.StatusCreated)
	})
	cors := middleware.CORS{
		Origins: []string{"https://app.example.com"},
		Expose:  []string{"Location"},
	}
	handler := middleware.Chain(mux, cors.Handler, cors.CrossOriginProtection().Handler)

	// send sends a request from a page of origin, as a browser does.
	send := func(method, origin string, header ...string) {
		req := httptest.NewRequest(method, "/links", strings.NewReader(`{"url":"https://go.dev"}`))
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Printf("%s from %s: %d, allow origin %q, expose %q\n", method, origin, rec.Code,
			rec.Header().Get("Access-Control-Allow-Origin"), rec.Header().Get("Access-Control-Expose-Headers"))
	}
	// A page of the origin asks whether it may post JSON, and posts it.
	send("OPTIONS", "https://app.example.com", "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type")
	send("POST", "https://app.example.com", "Content-Type", "application/json")
	// A page of another origin may do neither.
	send("OPTIONS", "https://other.example.com", "Access-Control-Request-Method", "POST")
	send("POST", "https://other.example.com", "Content-Type", "text/plain")
	// Output:
	// OPTIONS from https://app.example.com: 204, allow origin "https://app.example.com", expose ""
	// POST from https://app.example.com: 201, allow origin "https://app.example.com", expose "Location"
	// OPTIONS from https://other.example.com: 403, allow origin "", expose ""
	// POST from https://other.example.com: 403, allow origin "", expose ""
}
