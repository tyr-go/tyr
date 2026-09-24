package tyr

import (
	"context"
	"sync"

	"github.com/tyr-go/tyr/ctxkey"
)

// requestIDKey carries the ID of the request a context belongs to; see
// RequestIDFrom.
var requestIDKey = ctxkey.New[string]("request_id")

// operationKey carries the operation a call runs; see OperationFrom.
var operationKey = ctxkey.New[*Operation]("operation")

// requestInfoKey carries the RequestInfo of the request a context belongs
// to; see WithRequestInfo.
var requestInfoKey = ctxkey.New[*RequestInfo]("request_info")

// RequestIDFrom returns the ID of the request ctx belongs to, which
// middleware of a transport sets with [WithRequestID].
func RequestIDFrom(ctx context.Context) (string, bool) {
	return requestIDKey.Get(ctx)
}

// WithRequestID returns a derived context that carries id as the ID of its
// request, as [RequestIDFrom] reads it and [NewLogHandler] adds it to log
// records. Middleware of a transport sets it, such as
// [github.com/tyr-go/tyr/middleware.RequestID].
func WithRequestID(ctx context.Context, id string) context.Context {
	return requestIDKey.Set(ctx, id)
}

// OperationFrom returns the operation whose call ctx belongs to.
// [Operation.Call] puts it in the context it passes to the handler.
func OperationFrom(ctx context.Context) (*Operation, bool) {
	return operationKey.Get(ctx)
}

// WithOperation returns a derived context that carries op, as
// [OperationFrom] reads it. [Operation.Call] puts its operation in the
// context itself, unless the context already carries it. A transport uses
// WithOperation to keep the operation in the context beyond the call, e.g.
// to log a failure to encode the result with its operation.
//
// WithOperation panics if op is nil.
func WithOperation(ctx context.Context, op *Operation) context.Context {
	if op == nil {
		panic("tyr: WithOperation: nil operation")
	}
	return operationKey.Set(ctx, op)
}

// RequestInfo records what served a request: its route and its operation.
// It is for middleware above a transport, which reads it after next, and
// for transports, which call [RequestInfo.Record]:
//
//	// middleware, such as an access log or metrics
//	ctx, info := tyr.WithRequestInfo(r.Context())
//	next.ServeHTTP(w, r.WithContext(ctx))
//	route := info.Route()
//
//	// a transport, before it calls the operation
//	if info, ok := tyr.RequestInfoFrom(r.Context()); ok {
//		info.Record(r.Pattern, op)
//	}
//
// Middleware above a transport can't find out either by itself. A ServeMux
// sets the route in the request it gets, which middleware in between may
// have replaced, as r.WithContext makes, and only the transport knows the
// operation: JSON-RPC serves every one at the same route.
//
// A RequestInfo is safe for concurrent use.
type RequestInfo struct {
	mu       sync.Mutex
	recorded bool
	route    string
	op       *Operation
}

// WithRequestInfo returns a context that carries a new RequestInfo, and
// the RequestInfo. If ctx carries one already, WithRequestInfo returns ctx
// and that one, so that all the middleware above a transport reads what
// the transport records.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	if info, ok := requestInfoKey.Get(ctx); ok {
		return ctx, info
	}
	info := new(RequestInfo)
	return requestInfoKey.Set(ctx, info), info
}

// RequestInfoFrom returns the RequestInfo that ctx carries, for a transport
// to record in it what serves the request.
func RequestInfoFrom(ctx context.Context) (*RequestInfo, bool) {
	return requestInfoKey.Get(ctx)
}

// Record records the route and the operation that serve the request: the
// pattern of the ServeMux that matched it, and the operation, or nil if
// there is none or there are several, as in a JSON-RPC batch. A transport
// calls Record once, before it calls the operation. Only the first Record
// counts: a request that the operation makes in-process, with a context
// derived from its own, can't overwrite what the transport recorded.
func (i *RequestInfo) Record(route string, op *Operation) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.recorded {
		i.recorded, i.route, i.op = true, route, op
	}
}

// Route returns the recorded route, or "" if there is none.
func (i *RequestInfo) Route() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.route
}

// Operation returns the recorded operation and reports whether there is
// one.
func (i *RequestInfo) Operation() (*Operation, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.op, i.op != nil
}
