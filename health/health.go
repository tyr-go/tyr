// Package health answers the probes of load balancers and orchestrators,
// such as Kubernetes: liveness, whether the process is up, with [Live],
// and readiness, whether it should get traffic, with a [Readiness], which
// runs checks of what the service needs and fails while the service
// drains before it stops.
//
// Serve the probes past the middleware of the service: balancers call them
// every few seconds, an access log would drown in their records, and they
// need neither authentication nor CORS. An outer mux serves them itself
// and passes everything else to the chain:
//
//	ready := health.NewReadiness(health.Check("db", db.PingContext))
//
//	root := http.NewServeMux()
//	root.Handle("GET /livez", health.Live())
//	root.Handle("GET /readyz", ready)
//	root.Handle("/", middleware.Chain(mux, ...))
//
// The responses tell statuses only, such as
// {"status":"failed","checks":{"db":"failed"}}: the errors of the checks
// go to the log.
package health

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// defaultTimeout is the time the checks get without Timeout.
const defaultTimeout = time.Second

// Live returns the handler of the liveness probe. It answers 200 with
// {"status":"ok"} for as long as the server serves requests, and runs no
// checks: a service whose database is down is still alive, and restarting
// it doesn't bring the database back.
func Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, `{"status":"ok"}`)
	})
}

// Readiness is the handler of the readiness probe, which tells whether
// the service should get traffic. Create it with [NewReadiness]. It
// answers 200 if every check passes, and 503 if one fails or the service
// drains (see [Readiness.Drain]):
//
//	200 {"status":"ok","checks":{"db":"ok"}}
//	503 {"status":"failed","checks":{"db":"failed","cache":"ok"}}
//	503 {"status":"draining"}
//
// The checks run in parallel, each with a context that is done at the
// timeout (see [Timeout]), and a check passes if it returns nil within
// it. A check must return once its context is done: readiness waits for
// it rather than leave it running, until the client of the probe goes
// away. A check doesn't run twice at a time: a request that finds it
// running waits for that run, so a check that hangs holds one goroutine
// rather than one per probe, and the probes of several balancers don't
// multiply the pings to the database.
//
// The errors of the checks go to the log rather than to the response:
// "health: check failed" at the warning level, with the check, its error
// and the time it took, "health: check returned after its timeout" for a
// check that returned nil too late, and "health: check panicked" at the
// error level, with the stack.
//
// Check only what keeps this instance from serving. When a dependency that
// every instance shares slows down, a check of it takes all of them out of
// the balancers at once.
type Readiness struct {
	checks   []*namedCheck
	timeout  time.Duration
	log      *slog.Logger
	draining atomic.Bool
}

// namedCheck is a check of a Readiness, with its run in flight.
type namedCheck struct {
	name   string
	quoted string // name as a JSON string
	fn     func(ctx context.Context) error

	mu  sync.Mutex
	run *run // in flight, or nil
}

// run is a run of a check.
type run struct {
	done   chan struct{} // closed once the check has returned
	passed bool          // set before done is closed
}

// Option configures a [Readiness] made by [NewReadiness].
type Option func(*Readiness)

// Check adds a check named name to readiness: check gets a context that is
// done at the timeout, and returns nil if what it checks is usable, as
// [database/sql.DB.PingContext] does. The name shows in the responses and
// in the log. Check panics if the name is empty or not valid UTF-8, or if
// check is nil.
func Check(name string, check func(ctx context.Context) error) Option {
	switch {
	case name == "" || !utf8.ValidString(name):
		panic(fmt.Sprintf("health: Check(%q): want a name of valid UTF-8", name))
	case check == nil:
		panic(fmt.Sprintf("health: Check(%q): nil check", name))
	}
	quoted, _ := jsontext.AppendQuote(nil, name) // valid UTF-8
	return func(r *Readiness) {
		r.checks = append(r.checks, &namedCheck{name: name, quoted: string(quoted), fn: check})
	}
}

// Timeout sets how long a check may take, 1 second by default. A probe
// whose client gives up sooner fails all the same, without the statuses
// of the checks. Timeout panics if d isn't positive.
func Timeout(d time.Duration) Option {
	if d <= 0 {
		panic(fmt.Sprintf("health: Timeout(%v): want a positive duration", d))
	}
	return func(r *Readiness) { r.timeout = d }
}

// WithLogger sets the logger of the checks that fail. By default, it is
// [slog.Default] as it is at the time of logging.
func WithLogger(l *slog.Logger) Option {
	return func(r *Readiness) { r.log = l }
}

// NewReadiness returns a readiness probe configured by opts. It panics if
// an option is nil or two checks have the same name.
func NewReadiness(opts ...Option) *Readiness {
	r := &Readiness{timeout: defaultTimeout}
	for _, opt := range opts {
		if opt == nil {
			panic("health: NewReadiness: nil option")
		}
		opt(r)
	}
	names := make(map[string]bool, len(r.checks))
	for _, c := range r.checks {
		if names[c.name] {
			panic(fmt.Sprintf("health: NewReadiness: two checks named %q", c.name))
		}
		names[c.name] = true
	}
	return r
}

// ServeHTTP answers a readiness probe, as described at [Readiness].
func (r *Readiness) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.draining.Load() {
		write(w, http.StatusServiceUnavailable, `{"status":"draining"}`)
		return
	}
	runs := make([]*run, len(r.checks))
	for i, c := range r.checks {
		runs[i] = r.start(req.Context(), c)
	}
	passed := make([]bool, len(runs))
	code, status := http.StatusOK, "ok"
	for i, rn := range runs {
		select {
		case <-rn.done:
			passed[i] = rn.passed
		case <-req.Context().Done(): // the client has gone
		}
		if !passed[i] {
			code, status = http.StatusServiceUnavailable, "failed"
		}
	}
	write(w, code, r.body(status, passed))
}

// start returns the run of c in flight, starting one if there is none. The
// run gets the values of ctx but not its cancellation, as the requests
// that join it don't depend on the one that started it.
func (r *Readiness) start(ctx context.Context, c *namedCheck) *run {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		c.run = &run{done: make(chan struct{})}
		go r.exec(context.WithoutCancel(ctx), c, c.run)
	}
	return c.run
}

// exec runs c within the timeout, records in rn whether it passed and logs
// why it didn't.
func (r *Readiness) exec(ctx context.Context, c *namedCheck, rn *run) {
	defer func() {
		c.mu.Lock()
		c.run = nil
		c.mu.Unlock()
		close(rn.done)
	}()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	defer func() {
		if v := recover(); v != nil {
			r.logger().ErrorContext(ctx, "health: check panicked", "check", c.name, "panic", v, "stack", string(debug.Stack()))
		}
	}()
	err := c.fn(ctx)
	took := time.Since(start)
	switch {
	case err != nil:
		r.logger().WarnContext(ctx, "health: check failed", "check", c.name, "err", err, "took", took)
	case took > r.timeout:
		r.logger().WarnContext(ctx, "health: check returned after its timeout", "check", c.name, "timeout", r.timeout, "took", took)
	default:
		rn.passed = true
	}
}

// body returns the JSON of a response with the status and whether each
// check passed.
func (r *Readiness) body(status string, passed []bool) string {
	b := []byte(`{"status":"` + status + `"`)
	if len(r.checks) > 0 {
		b = append(b, `,"checks":{`...)
		for i, c := range r.checks {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, c.quoted...)
			if passed[i] {
				b = append(b, `:"ok"`...)
			} else {
				b = append(b, `:"failed"`...)
			}
		}
		b = append(b, '}')
	}
	return string(append(b, '}'))
}

// Drain makes readiness fail from now on, with {"status":"draining"} and
// no checks run, and then waits for d, or until ctx is done, while the
// server goes on serving: that's the time for the balancers to notice and
// send the traffic elsewhere. Call it when the service is told to stop,
// before [http.Server.Shutdown], which stops accepting connections at
// once:
//
//	srv.SetKeepAlivesEnabled(false) // clients reconnect after their next response
//	ready.Drain(ctx, 5*time.Second)
//	err := srv.Shutdown(ctx)
//
// d is the time a balancer takes to notice: the interval of its probes
// times the failures it waits for. Kubernetes takes a stopping pod out of
// its endpoints by itself, and d covers the time that takes to spread.
// The grace period of the pod must fit both d and the shutdown. Liveness
// doesn't change: the service is still alive.
func (r *Readiness) Drain(ctx context.Context, d time.Duration) {
	r.draining.Store(true)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// logger returns the logger of r.
func (r *Readiness) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	return slog.Default()
}

// write sends a response of a probe with the status code and the body.
func write(w http.ResponseWriter, code int, body string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}
