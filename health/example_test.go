package health_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/tyr-go/tyr/health"
)

func ExampleReadiness() {
	var up atomic.Bool // whether the database is up
	ready := health.NewReadiness(
		health.Check("db", func(ctx context.Context) error {
			if !up.Load() {
				return errors.New("connection refused")
			}
			return nil
		}),
		health.WithLogger(slog.New(slog.DiscardHandler)), // the errors of the checks go here
	)
	// A service passes everything else to its middleware.
	root := http.NewServeMux()
	root.Handle("GET /livez", health.Live())
	root.Handle("GET /readyz", ready)

	get := func(path string) {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		fmt.Println(path, rec.Code, rec.Body)
	}
	get("/readyz")
	up.Store(true)
	get("/readyz")

	// Told to stop, the service drains: readiness fails while the server
	// serves on. A service waits for the balancers; the example doesn't.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ready.Drain(ctx, 5*time.Second)
	get("/readyz")
	get("/livez")
	// Output:
	// /readyz 503 {"status":"failed","checks":{"db":"failed"}}
	// /readyz 200 {"status":"ok","checks":{"db":"ok"}}
	// /readyz 503 {"status":"draining"}
	// /livez 200 {"status":"ok"}
}
