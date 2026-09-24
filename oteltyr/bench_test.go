package oteltyr_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/middleware"
	"github.com/tyr-go/tyr/oteltyr"
	"github.com/tyr-go/tyr/rest"
)

// benchService returns a service of links.get, whose handler allocates
// nothing, with the middleware of shortlink, traced by tp and mp if they
// aren't nil.
func benchService(tp trace.TracerProvider, mp metric.MeterProvider) http.Handler {
	api := tyr.New(tyr.WithLogger(discard))
	if tp != nil {
		api.Use(oteltyr.Interceptor(oteltyr.WithTracerProvider(tp), oteltyr.WithMeterProvider(mp)))
	}
	goLink := &link{URL: "https://go.dev"}
	api.Handle("links.get", func(ctx context.Context, req getReq) (*link, error) { return goLink, nil },
		rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	h := middleware.Chain(mux, middleware.RequestID(), middleware.Logger(discard), middleware.Recover(discard))
	if tp != nil {
		h = oteltyr.Handler(h, otelhttp.WithTracerProvider(tp), otelhttp.WithMeterProvider(mp))
	}
	root := http.NewServeMux()
	root.Handle("/", h)
	return root
}

// benchDiscard is a ResponseWriter that keeps only the header.
type benchDiscard struct {
	header http.Header
}

func (d *benchDiscard) Header() http.Header         { return d.header }
func (d *benchDiscard) Write(b []byte) (int, error) { return len(b), nil }
func (d *benchDiscard) WriteHeader(int)             {}

func BenchmarkOTel(b *testing.B) {
	// Sampled: an SDK that records every span, without an exporter, and
	// aggregates the metrics; off: the providers of no-ops, as with no SDK.
	sampled := func() (trace.TracerProvider, metric.MeterProvider) {
		return sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())),
			sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	}
	off := func() (trace.TracerProvider, metric.MeterProvider) {
		return tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider()
	}
	without := func() (trace.TracerProvider, metric.MeterProvider) { return nil, nil }

	calls := make([]string, 10)
	for i := range calls {
		calls[i] = `{"jsonrpc":"2.0","method":"links.get","params":{"code":"go"},"id":1}`
	}
	requests := []struct {
		name, method, target, body string
	}{
		{"REST", "GET", "/links/go", ""},
		{"batch of 10", "POST", "/rpc", "[" + strings.Join(calls, ",") + "]"},
	}
	for _, rq := range requests {
		for _, v := range []struct {
			name      string
			providers func() (trace.TracerProvider, metric.MeterProvider)
		}{
			{"without oteltyr", without},
			{"sampled", sampled},
			{"off", off},
		} {
			b.Run(rq.name+", "+v.name, func(b *testing.B) {
				h := benchService(v.providers())
				req := httptest.NewRequest(rq.method, rq.target, nil)
				if rq.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				body := []byte(rq.body)
				w := &benchDiscard{header: make(http.Header)}
				b.ReportAllocs()
				for b.Loop() {
					clear(w.header)
					req.Body = io.NopCloser(bytes.NewReader(body))
					h.ServeHTTP(w, req)
				}
			})
		}
	}
}
