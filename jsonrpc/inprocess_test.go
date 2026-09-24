package jsonrpc_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
	"github.com/tyr-go/tyr/inprocess"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/middleware"
)

func TestClientInProcess(t *testing.T) {
	// The whole lifecycle of a call, in memory: interceptors, validation,
	// errors. The request ID goes in its header, as over a network, and
	// middleware.RequestID of the handler puts it in the context.
	var calls []string
	api := echoAPI()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		id, _ := tyr.RequestIDFrom(ctx)
		calls = append(calls, op.Name()+" "+id)
		return next(ctx, req)
	})
	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(middleware.RequestID()(jsonrpc.Handler(api))))
	ctx := tyr.WithRequestID(t.Context(), "0192f5e2")

	got, err := c.Call(ctx, echoOp, thing{Code: "go", Count: 2})
	if want := (thing{Code: "go", Count: 2}); got != want || err != nil {
		t.Errorf("Call(things.echo) = %+v, %v; want %+v, <nil>", got, err, want)
	}
	_, err = c.Call(ctx, tyr.Define[struct{}, struct{}]("things.check"), struct{}{})
	if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindInvalidArgument {
		t.Errorf("Call(things.check) = %v, want invalid_argument", err)
	}
	if want := []string{"things.echo 0192f5e2", "things.check 0192f5e2"}; strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

func TestClientInProcessAuthentication(t *testing.T) {
	// Authentication is middleware, which in process takes the caller from
	// the header, as over a network: a caller in the context of the one
	// who calls doesn't reach the handler.
	caller := ctxkey.New[string]("test.caller")
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer secret" {
				r = r.WithContext(caller.Set(r.Context(), "admin"))
			}
			next.ServeHTTP(w, r)
		})
	}
	api := echoAPI()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		if _, ok := caller.Get(ctx); !ok {
			return nil, tyr.Unauthenticated("log in first")
		}
		return next(ctx, req)
	})
	mux := http.NewServeMux()
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	server := authenticate(mux)

	hc := inprocess.Client(server)
	for _, ctx := range []context.Context{t.Context(), caller.Set(t.Context(), "admin")} {
		_, err := jsonrpc.NewClient("http://links/rpc", hc).Call(ctx, echoOp, thing{})
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindUnauthenticated {
			t.Errorf("Call() without a token = %v, want unauthenticated", err)
		}
	}

	// The header goes through a Transport that wraps that of the client.
	next := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer secret")
		return next.RoundTrip(r)
	})
	if _, err := jsonrpc.NewClient("http://links/rpc", hc).Call(t.Context(), echoOp, thing{}); err != nil {
		t.Errorf("Call() with a token = %v, want <nil>", err)
	}
}

func BenchmarkClient(b *testing.B) {
	// The handler allocates nothing: what's measured is the client, the
	// handler of jsonrpc and inprocess.Client.
	res := thing{Code: "go", Count: 1}
	get := tyr.Define[thing, thing]("things.get")
	api := tyr.New()
	api.Implement(get, func(ctx context.Context, req thing) (thing, error) {
		return res, nil
	})
	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(api)))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Call(ctx, get, thing{Code: "go"}); err != nil {
			b.Fatal(err)
		}
	}
}
