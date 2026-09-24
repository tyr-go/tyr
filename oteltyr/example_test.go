package oteltyr_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/oteltyr"
	"github.com/tyr-go/tyr/rest"
)

func Example() {
	// A program sets the global providers instead, with an exporter.
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	api := tyr.New()
	api.Use(oteltyr.Interceptor(oteltyr.WithTracerProvider(tp)))
	api.Handle("links.get", func(ctx context.Context, req getReq) (link, error) {
		if req.Code != "go" {
			return link{}, tyr.NotFound("link not found")
		}
		return link{URL: "https://go.dev"}, nil
	}, rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	handler := oteltyr.Handler(mux, otelhttp.WithTracerProvider(tp))

	for _, r := range []*http.Request{
		httptest.NewRequest("GET", "/links/go", nil),
		httptest.NewRequest("GET", "/links/rust", nil),
		httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"jsonrpc":"2.0","method":"links.get","params":{"code":"rust"},"id":7}`)),
	} {
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	for _, s := range spans.Ended() {
		fmt.Print(s.SpanKind(), " ", s.Name())
		for _, kv := range s.Attributes() {
			switch kv.Key {
			case "error.type", "rpc.response.status_code", "jsonrpc.request.id":
				fmt.Print(" ", kv.Key, "=", kv.Value.String())
			}
		}
		fmt.Println(" status:", s.Status().Code)
	}
	// Output:
	// server GET /links/{code} status: Unset
	// server GET /links/{code} error.type=not_found status: Unset
	// server links.get jsonrpc.request.id=7 rpc.response.status_code=404 error.type=not_found status: Unset
	// server POST /rpc status: Unset
}
