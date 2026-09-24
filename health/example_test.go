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
	"github.com/tyr-go/tyr/middleware"
)

// database stands for a database that is down until it's up.
type database struct {
	up atomic.Bool
}

func (d *database) PingContext(ctx context.Context) error {
	if !d.up.Load() {
		return errors.New("connection refused")
	}
	return nil
}

func ExampleReadiness() {
	db := &database{}
	ready := health.NewReadiness(
		health.Check("db", db.PingContext),
		health.WithLogger(slog.New(slog.DiscardHandler)), // the errors of the checks go here
	)
	// The probes go past the middleware of the service.
	service := middleware.Chain(http.NewServeMux(), middleware.RequestID())
	root := http.NewServeMux()
	root.Handle("GET /health/live", health.Live())
	root.Handle("GET /health/ready", ready)
	root.Handle("/", service)

	get := func(path string) {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		fmt.Println(path, rec.Code, rec.Body)
	}
	get("/health/ready")
	db.up.Store(true)
	get("/health/ready")

	// Told to stop, the service drains: readiness fails while the server
	// serves on. A service waits for the balancers; the example doesn't.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ready.Drain(ctx, 5*time.Second)
	get("/health/ready")
	get("/health/live")
	// Output:
	// /health/ready 503 {"status":"failed","checks":{"db":"failed"}}
	// /health/ready 200 {"status":"ok","checks":{"db":"ok"}}
	// /health/ready 503 {"status":"draining"}
	// /health/live 200 {"status":"ok"}
}
