package oteltyr

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/rpcconv"
	"go.opentelemetry.io/otel/trace"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
	"github.com/tyr-go/tyr/jsonrpc"
)

// insideKey marks the context that the interceptor passes on, so that a
// call of an operation within another gets an INTERNAL span.
var insideKey = ctxkey.New[bool]("oteltyr.inside")

// Interceptor returns an interceptor that traces the calls of operations
// and measures those of JSON-RPC, as the package documentation describes;
// put it first, so that its spans cover the other interceptors. It panics on
// a nil option.
//
// error.type is the name of the kind of the error, such as not_found or
// invalid_argument, rather than the code of JSON-RPC, which goes into
// rpc.response.status_code: the conventions would put the code there, but a
// kind is what a failure is in tyr, over any transport, so a dashboard
// counts the not_found of REST and of JSON-RPC as the same failure, and the
// kinds are a short list that only tyr changes. The status of a span is
// Error for internal, unavailable, deadline_exceeded and a kind that tyr
// doesn't define: the failures of the server, not of the request.
func Interceptor(opts ...Option) tyr.Interceptor {
	var c config
	for _, opt := range opts {
		if opt == nil {
			panic("oteltyr: Interceptor: nil option")
		}
		opt(&c)
	}
	tp, mp := c.tp, c.mp
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	duration, err := rpcconv.NewServerCallDuration(mp.Meter(scope))
	if err != nil {
		otel.Handle(err) // the metric is left out, as otelhttp does
	}
	i := &interceptor{tracer: tp.Tracer(scope), duration: duration}
	return i.intercept
}

// interceptor is what Interceptor returns.
type interceptor struct {
	tracer   trace.Tracer
	duration rpcconv.ServerCallDuration
}

func (i *interceptor) intercept(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
	if inside, _ := insideKey.Get(ctx); inside {
		return i.span(ctx, op, trace.SpanKindInternal, op.Name(), nil, req, next)
	}
	if call, ok := jsonrpc.CallFrom(ctx); ok {
		return i.rpc(ctx, op, call, req, next)
	}
	info, _ := tyr.RequestInfoFrom(ctx)
	recorded := false
	if info != nil {
		o, _ := info.Operation()
		recorded = o == op
	}
	if span, ok := spanKey.Get(ctx); ok {
		if recorded {
			return i.request(ctx, span, req, next)
		}
		// Not the call of the request: one within a handler outside tyr.
		return i.span(ctx, op, trace.SpanKindInternal, op.Name(), nil, req, next)
	}
	// No Handler above: a span of its own, after the route of REST if a
	// RequestInfo records it.
	name, attrs := op.Name(), []attribute.KeyValue(nil)
	if recorded && info.Route() != "" {
		method, _, _ := strings.Cut(info.Route(), " ")
		name = spanName(method, info.Route())
		attrs = []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(method), semconv.HTTPRoute(httpRoute(info.Route()))}
	}
	return i.span(ctx, op, trace.SpanKindServer, name, attrs, req, next)
}

// span calls next in a span of its own, of kind, with the name and attrs.
func (i *interceptor) span(ctx context.Context, op *tyr.Operation, kind trace.SpanKind, name string, attrs []attribute.KeyValue, req any, next tyr.Invoker) (any, error) {
	attrs = append(attrs, operationKey.String(op.Name()))
	ctx, span := i.tracer.Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
	defer span.End()
	res, err := next(insideKey.Set(ctx, true), req)
	if kind, failed := kindOf(err); failed && span.IsRecording() {
		span.SetAttributes(semconv.ErrorTypeKey.String(kind.name))
		if kind.server {
			span.SetStatus(codes.Error, "")
		}
	}
	return res, err
}

// request calls next in the span of the request that Handler traces, and
// gives it and its metrics the error of the call of REST.
func (i *interceptor) request(ctx context.Context, span trace.Span, req any, next tyr.Invoker) (any, error) {
	res, err := next(insideKey.Set(ctx, true), req)
	if kind, failed := kindOf(err); failed {
		errType := semconv.ErrorTypeKey.String(kind.name)
		span.SetAttributes(errType)
		if labeler, ok := otelhttp.LabelerFromContext(ctx); ok {
			labeler.Add(errType)
		}
	}
	return res, err
}

// rpc calls next in a SERVER span of the call of JSON-RPC and records its
// duration. The span starts with the attributes that samplers may look at,
// as the conventions ask; the others, and those of the metric, cost nothing
// when nothing records them.
func (i *interceptor) rpc(ctx context.Context, op *tyr.Operation, call jsonrpc.CallInfo, req any, next tyr.Invoker) (any, error) {
	start := time.Now()
	method := semconv.RPCMethod(op.Name())
	ctx, span := i.tracer.Start(ctx, op.Name(), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(semconv.RPCSystemNameJSONRPC, method))
	defer span.End()
	recording := span.IsRecording()
	if recording {
		version := semconv.JSONRPCProtocolVersion("2.0")
		if id, ok := requestID(call.ID); ok {
			span.SetAttributes(version, semconv.JSONRPCRequestID(id))
		} else {
			span.SetAttributes(version)
		}
	}
	res, err := next(insideKey.Set(ctx, true), req)

	measuring := i.duration.Enabled(ctx)
	if !recording && !measuring {
		return res, err
	}
	var failure []attribute.KeyValue
	if kind, failed := kindOf(err); failed {
		failure = []attribute.KeyValue{
			semconv.RPCResponseStatusCode(strconv.Itoa(jsonrpc.ErrorCode(kind.kind))),
			semconv.ErrorTypeKey.String(kind.name),
		}
		if recording {
			span.SetAttributes(failure...)
			if kind.server {
				span.SetStatus(codes.Error, "")
			}
		}
	}
	if measuring {
		i.duration.Record(ctx, time.Since(start).Seconds(), rpcconv.SystemNameJSONRPC, append(failure, method)...)
	}
	return res, err
}

// requestID returns the id of a request object as jsonrpc.request.id has
// it, a string without its quotes or a number as the request has it, and
// reports whether there is one: a notification or a null id has none.
func requestID(id []byte) (string, bool) {
	switch {
	case len(id) == 0 || string(id) == "null":
		return "", false
	case id[0] == '"':
		s, err := strconv.Unquote(string(id))
		if err != nil {
			return string(id), true // an escape of JSON that Go doesn't read
		}
		return s, true
	}
	return string(id), true
}

// failure is the kind of the error of a failed call, as the spans have it.
type failure struct {
	kind   tyr.Kind
	name   string // for error.type
	server bool   // a failure of the server rather than the request
}

// kindOf returns the kind of err, nil or a *tyr.Error, as next returns, and
// reports whether the call failed.
func kindOf(err error) (failure, bool) {
	if err == nil {
		return failure{}, false
	}
	e, ok := errors.AsType[*tyr.Error](err)
	if !ok || e == nil {
		return failure{kind: tyr.KindInternal, name: tyr.KindInternal.String(), server: true}, true
	}
	switch e.Kind {
	case tyr.KindInternal, tyr.KindUnavailable, tyr.KindDeadlineExceeded:
		return failure{kind: e.Kind, name: e.Kind.String(), server: true}, true
	case tyr.KindInvalidArgument, tyr.KindUnauthenticated, tyr.KindPermissionDenied, tyr.KindNotFound,
		tyr.KindAlreadyExists, tyr.KindFailedPrecondition, tyr.KindResourceExhausted, tyr.KindCanceled:
		return failure{kind: e.Kind, name: e.Kind.String()}, true
	}
	// A kind that tyr doesn't define goes to clients as internal.
	return failure{kind: tyr.KindInternal, name: tyr.KindInternal.String(), server: true}, true
}
