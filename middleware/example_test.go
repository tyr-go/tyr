package middleware_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

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
