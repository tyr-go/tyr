// Package oteltyr traces and measures a tyr API with OpenTelemetry, by the
// semantic conventions v1.43.0: [Handler] makes the spans and the metrics
// of HTTP requests, with otelhttp, and [Interceptor] those of the calls of
// operations. Set them up together, and the logs join the traces:
//
//	api.Use(oteltyr.Interceptor(), authz.Interceptor) // first: its spans cover the others
//
//	root.Handle("/", oteltyr.Handler(middleware.Chain(rest.ProblemHandler(mux), ...)))
//
//	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil), tyr.LogAttrs(oteltyr.TraceIDs)))
//
// The providers are those of the options, or the global ones of
// go.opentelemetry.io/otel, which a program sets with otel.SetTracerProvider
// and otel.SetMeterProvider.
//
// # Spans
//
// The span of an HTTP request is that of otelhttp, named after the route of
// the request, such as GET /links/{code}, with it in http.route and the
// operation in tyr.operation. otelhttp alone takes the route from the
// ServeMux right under it, and loses it to middleware in between that
// passes on another request; Handler takes it from the [tyr.RequestInfo]
// that the transport records.
//
// A call of REST is the request: its span is that of Handler, which the
// interceptor gives the kind of an error in error.type. A call of JSON-RPC
// gets a span of its own, a SERVER one under that of the request, as a
// batch has many calls: named after the method, with rpc.system.name
// jsonrpc, rpc.method, jsonrpc.protocol.version and jsonrpc.request.id, and
// for an error rpc.response.status_code and error.type. Without Handler
// above it, the interceptor makes a SERVER span of its own for a call of
// REST too, named after the route that a RequestInfo records, if any, or
// else after the operation, so a service without Handler still has traces.
// A call of an operation within another gets an INTERNAL span.
//
// # Errors
//
// error.type is the name of the kind of the error, such as not_found, on
// the spans of every transport and on their metrics, while the code of
// JSON-RPC, such as 404 or -32602, goes into rpc.response.status_code: the
// conventions would put the code into error.type too, but a kind is what a
// failure is in tyr, whatever the transport, so a dashboard counts the
// not_found of both transports as one and the same, and the kinds are a
// short list that changes with tyr only. The status of a span is Error for
// the kinds of failures of the server, internal, unavailable and
// deadline_exceeded, and for a kind that tyr doesn't define, as the
// conventions of gRPC have it for server spans, and not for invalid_argument
// or not_found: those are failures of the request, not of the server. A
// span of Handler gets its status from otelhttp, which agrees: 5xx is an
// error, and those kinds are the 5xx of rest.
//
// # Metrics
//
// A call of JSON-RPC records rpc.server.call.duration, with rpc.system.name,
// rpc.method and, for an error, rpc.response.status_code and error.type.
// A request records the metrics of otelhttp, such as
// http.server.request.duration, with http.route from the RequestInfo and,
// for a call of REST that fails, error.type.
//
// # Queries of pgx and sqlc
//
// github.com/exaring/otelpgx traces the queries of pgx, each in the trace
// of the call that runs it. It names the span of a query after the first
// word of its SQL, which for every query of sqlc is "--", the start of its
// -- name: comment; name the spans after the queries instead, as the recipe
// tasks of github.com/tyr-go/recipes does:
//
//	config.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithSpanNameFunc(func(sql string) string {
//		if rest, ok := strings.CutPrefix(sql, "-- name: "); ok {
//			if name, _, ok := strings.Cut(rest, " "); ok {
//				return name // such as GetProject
//			}
//		}
//		for word := range strings.FieldsSeq(sql) {
//			return strings.ToUpper(word) // such as BEGIN
//		}
//		return "query"
//	}))
package oteltyr

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scope is the name of the instrumentation scope of the spans and metrics.
const scope = "github.com/tyr-go/tyr/oteltyr"

// Option configures the [Interceptor].
type Option func(*config)

// config is what the options of Interceptor set.
type config struct {
	tp trace.TracerProvider
	mp metric.MeterProvider
}

// WithTracerProvider sets the provider of the tracer of the spans, the
// global one of go.opentelemetry.io/otel by default. For the spans of
// HTTP requests, give Handler otelhttp.WithTracerProvider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tp = tp }
}

// WithMeterProvider sets the provider of the meter of the metrics, the
// global one of go.opentelemetry.io/otel by default. For the metrics of
// HTTP requests, give Handler otelhttp.WithMeterProvider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(c *config) { c.mp = mp }
}

// TraceIDs appends the trace_id and the span_id of the span of ctx to
// attrs, if it has one, for [github.com/tyr-go/tyr.LogAttrs], so that the
// records of a request join its trace:
//
//	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil), tyr.LogAttrs(oteltyr.TraceIDs)))
func TraceIDs(ctx context.Context, attrs []slog.Attr) []slog.Attr {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return attrs
	}
	return append(attrs, slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
}
