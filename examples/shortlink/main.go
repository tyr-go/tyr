// Command shortlink is a URL shortener built on tyr: a small service with
// an in-memory store, served over REST and JSON-RPC, written the way a user
// of tyr would write it, to find what the framework lacks.
//
//	SHORTLINK_ADMIN_TOKEN=secret go run ./examples/shortlink -addr :8080
//
//	curl -i localhost:8080/links -H 'Content-Type: application/json' -d '{"url":"https://go.dev"}'
//	curl -i localhost:8080/links/<code>
//	curl -i localhost:8080/<code>
//	curl -i -X DELETE localhost:8080/links/<code> -H 'Authorization: Bearer secret'
//	curl -i localhost:8080/rpc -H 'Content-Type: application/json' -H 'Authorization: Bearer secret' \
//		-d '{"jsonrpc":"2.0","method":"links.purge","params":{"host":"go.dev"},"id":1}'
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/examples/shortlink/authz"
	"github.com/tyr-go/tyr/examples/shortlink/contract"
	"github.com/tyr-go/tyr/examples/shortlink/links"
	"github.com/tyr-go/tyr/examples/shortlink/store"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/middleware"
	"github.com/tyr-go/tyr/rest"
)

func main() {
	if err := run(); err != nil {
		slog.Error("shortlink stopped", "err", err)
		os.Exit(1)
	}
}

// run runs the service until it gets SIGINT or SIGTERM.
func run() error {
	addr := flag.String("addr", ":8080", "the address to listen on")
	flag.Parse()

	// Records get the request ID and the operation of their context.
	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil)))
	slog.SetDefault(logger)

	// The token comes from the environment: a flag would show it in the
	// list of processes.
	callers := make(map[string]authz.Caller)
	if token := os.Getenv("SHORTLINK_ADMIN_TOKEN"); token != "" {
		callers[token] = authz.Caller{Name: "admin", Roles: []string{"admin"}}
	}

	api := newAPI(links.New(store.New()), logger)
	srv := newServer(*addr, api, callers, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	logger.Info("serving", "addr", ln.Addr().String())
	return serve(ctx, srv, ln)
}

// newAPI returns the API of the service: its operations, the interceptors
// around them and the translation of the errors of the store.
func newAPI(svc *links.Service, logger *slog.Logger) *tyr.API {
	api := tyr.New(tyr.WithLogger(logger))
	api.Use(authz.Interceptor)
	api.MapError(func(err error) error {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return tyr.NotFound("link not found").WithCause(err)
		case errors.Is(err, store.ErrExists):
			return tyr.AlreadyExists("code is taken").WithCause(err)
		}
		return err // unmapped: the client gets an internal error
	})

	// The contract has the names and the routes; the roles are the
	// server's own business.
	api.Implement(contract.CreateLink, svc.Create)
	api.Implement(contract.GetLink, svc.Get)
	api.Implement(contract.FollowLink, svc.Follow)

	admin := api.Group(authz.Require("admin"))
	admin.Implement(contract.DeleteLink, svc.Delete)
	admin.Implement(contract.PurgeLinks, svc.Purge)
	return api
}

// newServer returns the HTTP server of the service: the routes of api and
// the middleware around them, with the timeouts of a server that faces the
// internet.
func newServer(addr string, api *tyr.API, callers map[string]authz.Caller, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	rest.Mount(mux, api, rest.Challenge(`Bearer realm="shortlink"`))
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest.WriteProblem(w, http.StatusForbidden)
	}))

	return &http.Server{
		Addr: addr,
		// The 404 and 405 of the mux are problems, as the errors of
		// operations are.
		Handler: middleware.Chain(rest.ProblemHandler(mux), // first = outermost
			middleware.RequestID(),
			middleware.Logger(logger),
			middleware.Recover(logger),
			csrf.Handler,
			// It passes on a request with another context, which Logger
			// doesn't mind: rest records the route and the operation for
			// it. Under Recover, its panics are recovered too.
			authz.Authenticate(callers),
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

// serve serves srv on ln until ctx is done, then shuts srv down
// gracefully: srv stops accepting connections, and serve waits up to 10
// seconds for the requests in flight.
func serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
