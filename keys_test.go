package tyr_test

import (
	"context"
	"sync"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestRequestID(t *testing.T) {
	if id, ok := tyr.RequestIDFrom(t.Context()); id != "" || ok {
		t.Errorf("RequestIDFrom(context without an ID) = %q, %t; want \"\", false", id, ok)
	}
	ctx := tyr.WithRequestID(t.Context(), "req-1")
	if id, ok := tyr.RequestIDFrom(ctx); id != "req-1" || !ok {
		t.Errorf("RequestIDFrom(WithRequestID(ctx, %q)) = %q, %t; want %[1]q, true", "req-1", id, ok)
	}
}

func TestOperationFrom(t *testing.T) {
	if op, ok := tyr.OperationFrom(t.Context()); op != nil || ok {
		t.Errorf("OperationFrom(context without an operation) = %v, %t; want <nil>, false", op, ok)
	}

	var got *tyr.Operation
	op := tyr.New().Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		got, _ = tyr.OperationFrom(ctx)
		return nil, nil
	})
	if _, err := op.Call(t.Context(), nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got != op {
		t.Errorf("OperationFrom(handler context) = %v, want the operation %q", got, op.Name())
	}
}

func TestWithOperation(t *testing.T) {
	op := tyr.New().Handle("links.get", getLink)
	if got, ok := tyr.OperationFrom(tyr.WithOperation(t.Context(), op)); got != op || !ok {
		t.Errorf("OperationFrom(WithOperation(ctx, op)) = %v, %t; want the operation, true", got, ok)
	}
	if got, want := panicValue(func() { tyr.WithOperation(t.Context(), nil) }), "tyr: WithOperation: nil operation"; got != want {
		t.Errorf("WithOperation(ctx, nil) panicked with %v, want %q", got, want)
	}
}

func TestCallContext(t *testing.T) {
	var got context.Context // what the handler got
	record := func(ctx context.Context, req getLinkReq) (*link, error) {
		got = ctx
		return nil, nil
	}
	api := tyr.New()
	get := api.Handle("links.get", record)
	list := api.Handle("links.list", record)

	// A context that carries the operation already is passed on as is.
	ctx := tyr.WithOperation(t.Context(), get)
	if _, err := get.Call(ctx, nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got != ctx {
		t.Error("Call() added the operation to a context that carries it already")
	}

	// A context of another operation gets this one.
	if _, err := list.Call(ctx, nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if op, _ := tyr.OperationFrom(got); op != list {
		t.Error("the handler's context doesn't carry links.list")
	}
}

func TestRequestInfo(t *testing.T) {
	get := tyr.New().Handle("links.get", getLink)
	list := tyr.New().Handle("links.list", getLink)

	if info, ok := tyr.RequestInfoFrom(t.Context()); info != nil || ok {
		t.Errorf("RequestInfoFrom(context without one) = %v, %t; want <nil>, false", info, ok)
	}
	ctx, info := tyr.WithRequestInfo(t.Context())
	if got, ok := tyr.RequestInfoFrom(ctx); got != info || !ok {
		t.Errorf("RequestInfoFrom(WithRequestInfo(ctx)) = %p, %t; want %p, true", got, ok, info)
	}
	if op, ok := info.Operation(); info.Route() != "" || op != nil || ok {
		t.Errorf("Route(), Operation() before Record = %q, %v, %t; want \"\", <nil>, false", info.Route(), op, ok)
	}

	// Middleware below gets the same one, and its context as it is.
	below := tyr.WithRequestID(ctx, "req-1")
	if got, again := tyr.WithRequestInfo(below); got != below || again != info {
		t.Error("WithRequestInfo(context that carries one) made another")
	}

	// Only the first Record counts.
	info.Record("GET /links/{code}", get)
	info.Record("POST /rpc", list)
	if op, ok := info.Operation(); info.Route() != "GET /links/{code}" || op != get || !ok {
		t.Errorf("Route(), Operation() = %q, %v, %t; want %q, links.get, true", info.Route(), op, ok, "GET /links/{code}")
	}
}

func TestRequestInfoWithoutOperation(t *testing.T) {
	// A JSON-RPC batch runs several operations at one route.
	_, info := tyr.WithRequestInfo(t.Context())
	info.Record("POST /rpc", nil)
	info.Record("POST /rpc", tyr.New().Handle("links.get", getLink))
	if op, ok := info.Operation(); info.Route() != "POST /rpc" || op != nil || ok {
		t.Errorf("Route(), Operation() = %q, %v, %t; want %q, <nil>, false", info.Route(), op, ok, "POST /rpc")
	}
}

func TestRequestInfoConcurrent(t *testing.T) {
	// Middleware may read it while the transport records, as when
	// http.TimeoutHandler gives up on a handler that goes on.
	op := tyr.New().Handle("links.get", getLink)
	_, info := tyr.WithRequestInfo(t.Context())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { info.Record("GET /links/{code}", op) })
		wg.Go(func() {
			_ = info.Route()
			_, _ = info.Operation()
		})
	}
	wg.Wait()
	if got, _ := info.Operation(); info.Route() != "GET /links/{code}" || got != op {
		t.Errorf("Route(), Operation() = %q, %v; want the recorded ones", info.Route(), got)
	}
}
