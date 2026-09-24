package oteltyr_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/middleware"
	"github.com/tyr-go/tyr/oteltyr"
	"github.com/tyr-go/tyr/rest"
)

// telemetry is an SDK that keeps the spans and the metrics it gets.
type telemetry struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
}

func newTelemetry() *telemetry {
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	return &telemetry{
		spans:  spans,
		reader: reader,
		tp:     sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
		mp:     sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	}
}

// span is what a test checks of a span.
type span struct {
	name   string
	kind   trace.SpanKind
	attrs  map[string]string // those of attrKeys that it has
	status codes.Code
	parent string // the name of the parent span, if it's one of the ended
}

// attrKeys are the attributes that the tests check.
var attrKeys = []string{
	"http.route", "http.request.method", "tyr.operation", "error.type",
	"rpc.system.name", "rpc.method", "jsonrpc.protocol.version", "jsonrpc.request.id", "rpc.response.status_code",
}

// ended returns the spans that ended, in the order they ended.
func (tel *telemetry) ended() []span {
	all := tel.spans.Ended()
	names := make(map[trace.SpanID]string)
	for _, s := range all {
		names[s.SpanContext().SpanID()] = s.Name()
	}
	out := make([]span, 0, len(all))
	for _, s := range all {
		sp := span{name: s.Name(), kind: s.SpanKind(), attrs: map[string]string{}, status: s.Status().Code, parent: names[s.Parent().SpanID()]}
		for _, kv := range s.Attributes() {
			if slices.Contains(attrKeys, string(kv.Key)) {
				sp.attrs[string(kv.Key)] = kv.Value.String()
			}
		}
		out = append(out, sp)
	}
	return out
}

// points returns the data points of the histogram name, each as its
// attributes of attrKeys, sorted.
func (tel *telemetry) points(t *testing.T, name string) []string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := tel.reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				var kvs []string
				for _, kv := range dp.Attributes.ToSlice() {
					if slices.Contains(attrKeys, string(kv.Key)) {
						kvs = append(kvs, fmt.Sprintf("%s=%s", kv.Key, kv.Value.String()))
					}
				}
				out = append(out, fmt.Sprintf("%s count=%d", strings.Join(kvs, " "), dp.Count))
			}
		}
	}
	slices.Sort(out)
	return out
}

type getReq struct {
	Code string `json:"code" path:"code" validate:"required"`
}

type link struct {
	URL string `json:"url"`
}

// get knows the link go, not nope, and fails at bug.
func get(ctx context.Context, req getReq) (link, error) {
	switch req.Code {
	case "nope":
		return link{}, tyr.NotFound("link not found")
	case "bug":
		return link{}, errors.New("db: connection refused")
	}
	return link{URL: "https://go.dev"}, nil
}

// withValue is a middleware that passes on another request, as
// authentication does, which hides the pattern of the mux from otelhttp.
func withValue(next http.Handler) http.Handler {
	type key struct{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, 1)))
	})
}

// service returns the handler of a service of links.get, links.first, which
// calls links.get itself, and a probe on an outer mux, and handler wraps its
// middleware and routes: oteltyr.Handler or none.
func service(tel *telemetry, logger *slog.Logger, handler func(http.Handler) http.Handler) http.Handler {
	api := tyr.New(tyr.WithLogger(logger))
	api.Use(oteltyr.Interceptor(oteltyr.WithTracerProvider(tel.tp), oteltyr.WithMeterProvider(tel.mp)))
	getOp := api.Handle("links.get", get, rest.Route("GET /links/{code}"))
	api.Handle("links.first", func(ctx context.Context, req struct{}) (link, error) {
		res, err := getOp.Call(ctx, func(dst any) error { dst.(*getReq).Code = "go"; return nil })
		if err != nil {
			return link{}, err
		}
		return res.(link), nil
	}, rest.Route("GET /first"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	root := http.NewServeMux()
	root.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {})
	root.Handle("/", handler(middleware.Chain(mux, middleware.RequestID(), middleware.Logger(logger), withValue)))
	return root
}

// traced is oteltyr.Handler with the providers of tel.
func traced(tel *telemetry) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return oteltyr.Handler(h, otelhttp.WithTracerProvider(tel.tp), otelhttp.WithMeterProvider(tel.mp))
	}
}

// send sends a request to h and returns the response.
func send(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// discard is a logger that drops its records.
var discard = slog.New(slog.DiscardHandler)

func TestREST(t *testing.T) {
	// A call of REST is its request: one span, of otelhttp, named after
	// the route that rest records, whatever the middleware in between and
	// the mux above, with the kind of an error.
	tel := newTelemetry()
	h := service(tel, discard, traced(tel))
	for _, code := range []string{"go", "nope", "bug"} {
		send(h, "GET", "/links/"+code, "")
	}
	send(h, "GET", "/health/live", "") // past the Handler: no span

	route := map[string]string{"http.request.method": "GET", "http.route": "/links/{code}", "tyr.operation": "links.get"}
	with := func(extra ...string) map[string]string {
		m := map[string]string{}
		for k, v := range route {
			m[k] = v
		}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	want := []span{
		{name: "GET /links/{code}", kind: trace.SpanKindServer, attrs: with()},
		{name: "GET /links/{code}", kind: trace.SpanKindServer, attrs: with("error.type", "not_found")},
		{name: "GET /links/{code}", kind: trace.SpanKindServer, attrs: with("error.type", "internal"), status: codes.Error},
	}
	if got := tel.ended(); !equalSpans(got, want) {
		t.Errorf("spans =\n%v\nwant\n%v", got, want)
	}
	wantPoints := []string{
		"error.type=internal http.request.method=GET http.route=/links/{code} count=1",
		"error.type=not_found http.request.method=GET http.route=/links/{code} count=1",
		"http.request.method=GET http.route=/links/{code} count=1",
	}
	if got := tel.points(t, "http.server.request.duration"); !slices.Equal(got, wantPoints) {
		t.Errorf("points =\n%q\nwant\n%q", got, wantPoints)
	}
}

func TestJSONRPC(t *testing.T) {
	// A call of JSON-RPC gets a SERVER span of its own under that of the
	// request, and a point of rpc.server.call.duration.
	tel := newTelemetry()
	h := service(tel, discard, traced(tel))
	send(h, "POST", "/rpc", `{"jsonrpc":"2.0","method":"links.get","params":{"code":"go"},"id":1}`)
	send(h, "POST", "/rpc", `[`+
		`{"jsonrpc":"2.0","method":"links.get","params":{"code":"nope"},"id":"a"},`+
		`{"jsonrpc":"2.0","method":"links.get","params":{"code":"bug"},"id":2},`+
		`{"jsonrpc":"2.0","method":"links.get","params":{}},`+
		`{"jsonrpc":"2.0","method":"links.nope","id":3}]`)

	rpc := func(extra ...string) map[string]string {
		m := map[string]string{"rpc.system.name": "jsonrpc", "rpc.method": "links.get", "jsonrpc.protocol.version": "2.0"}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	got := tel.ended()
	// The calls of a batch end in any order: by their ids, those without
	// one in the order they ended.
	slices.SortStableFunc(got, func(a, b span) int {
		return strings.Compare(a.attrs["jsonrpc.request.id"], b.attrs["jsonrpc.request.id"])
	})
	post := func(extra ...string) map[string]string {
		m := map[string]string{"http.request.method": "POST", "http.route": "/rpc"}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	want := []span{
		{name: "POST /rpc", kind: trace.SpanKindServer, attrs: post("tyr.operation", "links.get")}, // a single call
		// A notification has no id.
		{name: "links.get", kind: trace.SpanKindServer, parent: "POST /rpc",
			attrs: rpc("rpc.response.status_code", "-32602", "error.type", "invalid_argument")},
		{name: "POST /rpc", kind: trace.SpanKindServer, attrs: post()}, // a batch: no operation
		{name: "links.get", kind: trace.SpanKindServer, parent: "POST /rpc", attrs: rpc("jsonrpc.request.id", "1")},
		{name: "links.get", kind: trace.SpanKindServer, parent: "POST /rpc", status: codes.Error,
			attrs: rpc("jsonrpc.request.id", "2", "rpc.response.status_code", "-32603", "error.type", "internal")},
		{name: "links.get", kind: trace.SpanKindServer, parent: "POST /rpc",
			attrs: rpc("jsonrpc.request.id", "a", "rpc.response.status_code", "404", "error.type", "not_found")},
	}
	if !equalSpans(got, want) {
		t.Errorf("spans =\n%v\nwant\n%v", got, want)
	}
	wantPoints := []string{
		"error.type=internal rpc.method=links.get rpc.response.status_code=-32603 rpc.system.name=jsonrpc count=1",
		"error.type=invalid_argument rpc.method=links.get rpc.response.status_code=-32602 rpc.system.name=jsonrpc count=1",
		"error.type=not_found rpc.method=links.get rpc.response.status_code=404 rpc.system.name=jsonrpc count=1",
		"rpc.method=links.get rpc.system.name=jsonrpc count=1",
	}
	if got := tel.points(t, "rpc.server.call.duration"); !slices.Equal(got, wantPoints) {
		t.Errorf("points =\n%q\nwant\n%q", got, wantPoints)
	}
}

func TestWithoutHandler(t *testing.T) {
	// Without Handler, the interceptor makes a SERVER span of its own for
	// a call of REST, after the route that the RequestInfo of Logger gets,
	// so that the service isn't left without traces.
	tel := newTelemetry()
	h := service(tel, discard, func(h http.Handler) http.Handler { return h })
	send(h, "GET", "/links/nope", "")
	want := []span{{name: "GET /links/{code}", kind: trace.SpanKindServer, attrs: map[string]string{
		"http.request.method": "GET", "http.route": "/links/{code}", "tyr.operation": "links.get", "error.type": "not_found",
	}}}
	if got := tel.ended(); !equalSpans(got, want) {
		t.Errorf("spans =\n%v\nwant\n%v", got, want)
	}

	// Without a RequestInfo either, as for a call outside the transports,
	// the span is named after the operation.
	tel = newTelemetry()
	api := tyr.New(tyr.WithLogger(discard))
	api.Use(oteltyr.Interceptor(oteltyr.WithTracerProvider(tel.tp), oteltyr.WithMeterProvider(tel.mp)))
	op := api.Handle("links.get", get)
	if _, err := op.Call(t.Context(), func(dst any) error { dst.(*getReq).Code = "bug"; return nil }); err == nil {
		t.Fatal("Call() error = <nil>")
	}
	want = []span{{name: "links.get", kind: trace.SpanKindServer, status: codes.Error,
		attrs: map[string]string{"tyr.operation": "links.get", "error.type": "internal"}}}
	if got := tel.ended(); !equalSpans(got, want) {
		t.Errorf("spans =\n%v\nwant\n%v", got, want)
	}
}

func TestNested(t *testing.T) {
	// A call within another gets an INTERNAL span under the span of the
	// outer one.
	tel := newTelemetry()
	send(service(tel, discard, traced(tel)), "GET", "/first", "")
	want := []span{
		{name: "links.get", kind: trace.SpanKindInternal, parent: "GET /first", attrs: map[string]string{"tyr.operation": "links.get"}},
		{name: "GET /first", kind: trace.SpanKindServer, attrs: map[string]string{"http.request.method": "GET", "http.route": "/first", "tyr.operation": "links.first"}},
	}
	if got := tel.ended(); !equalSpans(got, want) {
		t.Errorf("spans =\n%v\nwant\n%v", got, want)
	}
}

func TestTraceIDs(t *testing.T) {
	// The records of a request, the access log among them, have the IDs of
	// its span.
	tel := newTelemetry()
	var buf bytes.Buffer
	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil), tyr.LogAttrs(oteltyr.TraceIDs)))
	send(service(tel, logger, traced(tel)), "GET", "/links/go", "")

	spans := tel.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}
	sc := spans[0].SpanContext()
	var record struct {
		Msg     string `json:"msg"`
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	}
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("%v: %s", err, buf.Bytes())
	}
	if record.Msg != "middleware: request" || record.TraceID != sc.TraceID().String() || record.SpanID != sc.SpanID().String() {
		t.Errorf("the record %s, want trace_id %s and span_id %s", buf.Bytes(), sc.TraceID(), sc.SpanID())
	}

	// Without a span, nothing.
	if got := oteltyr.TraceIDs(t.Context(), nil); got != nil {
		t.Errorf("TraceIDs() without a span = %v", got)
	}
}

func TestInterceptorNilOption(t *testing.T) {
	defer func() {
		if got := recover(); got != "oteltyr: Interceptor: nil option" {
			t.Errorf("Interceptor(nil) panicked with %v", got)
		}
	}()
	oteltyr.Interceptor(nil)
}

// equalSpans reports whether the spans a and b are the same.
func equalSpans(a, b []span) bool {
	return slices.EqualFunc(a, b, func(x, y span) bool {
		return x.name == y.name && x.kind == y.kind && x.status == y.status && x.parent == y.parent && equalAttrs(x.attrs, y.attrs)
	})
}

func equalAttrs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
