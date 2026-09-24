package oteltyr

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
)

// operationKey is the attribute of the name of an operation, which the
// conventions have no attribute for.
const operationKey = attribute.Key("tyr.operation")

// spanKey marks the context of a request that Handler traces, with its
// span, which the interceptor gives the error of a call of REST.
var spanKey = ctxkey.New[trace.Span]("oteltyr.span")

// Handler wraps h, the middleware and the routes of a service, in otelhttp
// with opts: a span and metrics of every request, named after its route,
// the one that the transport records in the [tyr.RequestInfo] of the
// request, whatever the middleware in between; see the package
// documentation. It marks the context of the request, so that the
// interceptor gives the span the errors of the call of REST rather than
// make one of its own.
//
// A ServeMux above Handler, as one that serves probes past the middleware,
// sets its pattern in the request, "/" for the rest, which isn't the route:
// Handler passes the request on without it. The route goes into the
// metrics by the Labeler of otelhttp.
func Handler(h http.Handler, opts ...otelhttp.Option) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		h.ServeHTTP(w, r.WithContext(spanKey.Set(r.Context(), span)))
		info, ok := tyr.RequestInfoFrom(r.Context())
		if !ok {
			return
		}
		if route := info.Route(); route != "" {
			attr := semconv.HTTPRoute(httpRoute(route))
			span.SetName(spanName(r.Method, route))
			span.SetAttributes(attr)
			if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
				labeler.Add(attr)
			}
		}
		if op, ok := info.Operation(); ok {
			span.SetAttributes(operationKey.String(op.Name()))
		}
	})
	traced := otelhttp.NewHandler(inner, "", opts...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tyr.WithRequestInfo(r.Context())
		r = r.WithContext(ctx)
		r.Pattern = "" // of a mux above: the route is the one that the transport records
		traced.ServeHTTP(w, r)
	})
}

// httpRoute returns the route of the pattern of a ServeMux, as http.route
// has it: the path, from its first slash, without the method and the host.
func httpRoute(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return pattern
}

// spanName returns the name of the span of a request of method at the
// pattern route, as the conventions of HTTP have it: the method, HTTP for
// one they don't know, and the route.
func spanName(method, route string) string {
	switch method = strings.ToUpper(method); method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
	default:
		method = "HTTP"
	}
	return method + " " + httpRoute(route)
}
