// Command shortlink is a URL shortener built on tyr: a small service with
// an in-memory store, served over REST and JSON-RPC, written the way a user
// of tyr would write it, to find what the framework lacks.
//
//	SHORTLINK_ADMIN_TOKEN=secret go run ./examples/shortlink -addr :8080 -origin http://localhost:5173
//
//	curl -i localhost:8080/links -H 'Content-Type: application/json' -d '{"url":"https://go.dev"}'
//	curl -i localhost:8080/links/<code>
//	curl -i localhost:8080/<code>
//	curl -i -X DELETE localhost:8080/links/<code> -H 'Authorization: Bearer secret'
//	curl -i localhost:8080/rpc -H 'Content-Type: application/json' -H 'Authorization: Bearer secret' \
//		-d '{"jsonrpc":"2.0","method":"links.purge","params":{"host":"go.dev"},"id":1}'
//
// It describes itself in an OpenAPI document and an OpenRPC one:
//
//	curl localhost:8080/openapi.json
//	curl localhost:8080/rpc -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"rpc.discover","id":1}'
//
// Load balancers probe it past its middleware, and when it's told to
// stop, readiness fails while it serves on for a while (see -drain):
//
//	curl localhost:8080/health/live
//	curl localhost:8080/health/ready
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
	"github.com/tyr-go/tyr/health"
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
	var origins []string
	flag.Func("origin", "an origin whose pages may call the service, such as https://app.example.com; repeat for more", func(origin string) error {
		origins = append(origins, origin)
		return nil
	})
	drain := flag.Duration("drain", 5*time.Second, "how long to serve on, with readiness failing, once told to stop")
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
	// The store is in memory: there's nothing to check, only the drain.
	ready := health.NewReadiness(health.WithLogger(logger))
	srv := newServer(*addr, api, callers, origins, ready, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A second signal stops the service at once, rather than after the
	// drain.
	context.AfterFunc(ctx, stop)
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	logger.Info("serving", "addr", ln.Addr().String())
	return serve(ctx, srv, ln, ready, *drain)
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

	// The contract has the names and the routes; the timeouts and the
	// roles are the server's own business. Every operation has 5 seconds,
	// within the WriteTimeout of the server.
	ops := api.Group(tyr.Timeout(5 * time.Second))
	ops.Implement(contract.CreateLink, svc.Create)
	ops.Implement(contract.GetLink, svc.Get)
	ops.Implement(contract.FollowLink, svc.Follow)

	admin := ops.Group(authz.Require("admin"))
	admin.Implement(contract.DeleteLink, svc.Delete)
	admin.Implement(contract.PurgeLinks, svc.Purge)
	return api
}

// info describes the service in its documents.
var info = tyr.Info{
	Title:       "shortlink",
	Version:     "1.0.0",
	Description: "A URL shortener built on tyr, over REST and JSON-RPC.",
}

// newServer returns the HTTP server of the service: the routes of api and
// its documents, the middleware around them and the probes past it, with
// the timeouts of a server that faces the internet. The pages of origins
// may call the service from browsers.
func newServer(addr string, api *tyr.API, callers map[string]authz.Caller, origins []string, ready *health.Readiness, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	// The document tells of the same challenge that REST sends.
	routes := rest.Mount(mux, api, rest.Challenge(`Bearer realm="shortlink"`))
	mux.Handle("GET /openapi.json", routes.OpenAPI(info))
	mux.Handle("POST /rpc", jsonrpc.Handler(api, jsonrpc.Discover(info)))

	cors := middleware.CORS{
		Origins: origins,
		// The link that a create makes, and the ID of a request to report.
		Expose: []string{"Location", "X-Request-ID"},
		MaxAge: time.Hour,
	}
	// It trusts the origins of CORS, whose requests it would reject
	// otherwise. It denies with a kind, as the operations do: the document
	// promises their clients one with a 403.
	csrf := cors.CrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.WriteError(w, r, tyr.PermissionDenied("cross-origin request"))
	}))

	// The probes go past the middleware: the balancers call them every few
	// seconds, and the access log would drown in their records. Their
	// paths have two segments, which GET /{code} never takes.
	root := http.NewServeMux()
	root.Handle("GET /health/live", health.Live())
	root.Handle("GET /health/ready", ready)
	// The 404 and 405 of the mux are problems, as the errors of operations
	// are.
	root.Handle("/", middleware.Chain(rest.ProblemHandler(mux), // first = outermost
		middleware.RequestID(),
		middleware.Logger(logger),
		// It answers preflight requests before the mux, which would answer
		// them with 405, and Recover keeps its headers on a 500.
		cors.Handler,
		middleware.Recover(logger),
		csrf.Handler,
		// It passes on a request with another context, which Logger
		// doesn't mind: rest records the route and the operation for it.
		// Under Recover, its panics are recovered too.
		authz.Authenticate(callers),
	))

	return &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

// serve serves srv on ln until ctx is done, then shuts srv down
// gracefully. First it drains: ready fails, and srv serves on for drain,
// while the balancers take the traffic away, closing connections after
// their responses, so that clients reconnect elsewhere. Then srv stops
// accepting connections, and serve waits up to 10 seconds for the requests
// in flight.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, ready *health.Readiness, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain+10*time.Second)
	defer cancel()
	srv.SetKeepAlivesEnabled(false)
	ready.Drain(ctx, drain)
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
