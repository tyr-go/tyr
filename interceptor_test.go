package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
)

// passThrough is an interceptor that only calls next.
func passThrough(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
	return next(ctx, req)
}

func TestUse(t *testing.T) {
	var trace []string
	record := func(name string) tyr.Interceptor {
		return func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
			trace = append(trace, name+" before")
			res, err := next(ctx, req)
			trace = append(trace, name+" after")
			return res, err
		}
	}
	api := tyr.New()
	api.Use(record("a"), record("b"))
	api.Use(record("c"))
	op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		trace = append(trace, "handler")
		return getLink(ctx, req)
	})

	want := []string{"a before", "b before", "c before", "handler", "c after", "b after", "a after"}
	for _, sealed := range []bool{false, true} {
		if sealed {
			api.Seal()
		}
		trace = nil
		if _, err := op.Call(t.Context(), nil); err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !slices.Equal(trace, want) {
			t.Errorf("sealed: %t, trace:\n%q\nwant:\n%q", sealed, trace, want)
		}
	}
}

func TestUseBeforeSeal(t *testing.T) {
	api := tyr.New()
	op := api.Handle("links.get", getLink)
	if _, err := op.Call(t.Context(), nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	// Until the API is sealed, every call builds its chain, so it sees the
	// interceptors added after earlier calls.
	called := false
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		called = true
		return next(ctx, req)
	})
	if _, err := op.Call(t.Context(), nil); err != nil || !called {
		t.Errorf("Call() error = %v, interceptor called: %t; want <nil>, true", err, called)
	}
}

func TestSealBuildsChains(t *testing.T) {
	allocs := func(interceptors int) float64 {
		api := tyr.New()
		for range interceptors {
			api.Use(passThrough)
		}
		op := api.Handle("links.get", getLink)
		api.Seal()
		ctx, decode := t.Context(), decodeTo(getLinkReq{Code: "go"})
		return testing.AllocsPerRun(100, func() { _, _ = op.Call(ctx, decode) })
	}

	// A call to a sealed API doesn't build its chain, so what it allocates
	// doesn't depend on the number of interceptors.
	if none, three := allocs(0), allocs(3); three != none {
		t.Errorf("a sealed call allocates %v times with 3 interceptors, want %v as with none", three, none)
	}
}

func TestInterceptorRequest(t *testing.T) {
	const want = ", want *tyr_test.getLinkReq"
	tests := []struct {
		name    string
		pass    func(req any) any // what the interceptor passes to next
		wantReq *getLinkReq       // what the handler gets; nil if it isn't called
		wantErr string            // the cause of the call's error; "" for none
	}{
		{
			name: "changed in place",
			pass: func(req any) any {
				req.(*getLinkReq).Code = "changed"
				return req
			},
			wantReq: &getLinkReq{Code: "changed"},
		},
		{
			name:    "replaced",
			pass:    func(any) any { return &getLinkReq{Code: "replaced"} },
			wantReq: &getLinkReq{Code: "replaced"},
		},
		{
			name:    "value instead of a pointer",
			pass:    func(req any) any { return *req.(*getLinkReq) },
			wantErr: "tyr: an interceptor passed tyr_test.getLinkReq to next" + want,
		},
		{
			name:    "nil pointer",
			pass:    func(any) any { return (*getLinkReq)(nil) },
			wantErr: "tyr: an interceptor passed a nil *tyr_test.getLinkReq to next",
		},
		{
			name:    "nil",
			pass:    func(any) any { return nil },
			wantErr: "tyr: an interceptor passed <nil> to next" + want,
		},
		{
			name:    "other type",
			pass:    func(any) any { return "go" },
			wantErr: "tyr: an interceptor passed string to next" + want,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return next(ctx, tt.pass(req))
			})
			var got *getLinkReq
			op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
				got = &req
				return nil, nil
			})
			_, err := op.Call(t.Context(), decodeTo(getLinkReq{Code: "go"}))

			if !reflect.DeepEqual(got, tt.wantReq) {
				t.Errorf("the handler got %+v, want %+v", got, tt.wantReq)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Call() error = %v, want <nil>", err)
				}
				return
			}
			wantErr := "internal: internal error: " + tt.wantErr
			if err == nil || err.Error() != wantErr {
				t.Errorf("Call() error = %v, want %s", err, wantErr)
			}
			wantLogs := []logged{{
				level: slog.LevelError,
				msg:   "tyr: operation failed",
				attrs: map[string]string{"err": wantErr},
				op:    "links.get",
			}}
			if !reflect.DeepEqual(rec.logs, wantLogs) {
				t.Errorf("logged %+v, want %+v", rec.logs, wantLogs)
			}
		})
	}
}

func TestInterceptorShortCircuit(t *testing.T) {
	denied := tyr.PermissionDenied("requires admin")
	tests := []struct {
		name string
		res  any
		err  error
	}{
		{"result", &link{Code: "cached"}, nil},
		{"error", nil, denied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := tyr.New()
			api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return tt.res, tt.err
			})
			called := false
			op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
				called = true
				return nil, nil
			})

			res, err := op.Call(t.Context(), nil)
			if res != tt.res || err != tt.err {
				t.Errorf("Call() = %v, %v; want %v, %v", res, err, tt.res, tt.err)
			}
			if called {
				t.Error("the handler was called")
			}
		})
	}
}

func TestInterceptorResult(t *testing.T) {
	tests := []struct {
		name    string
		call    func(api *tyr.API, ic tyr.Interceptor) *tyr.Operation // registers the operation
		res     any                                                   // what the interceptor returns
		wantErr string                                                // the cause of the call's error; "" for none
	}{
		{name: "Res", call: withRes[*link], res: &link{Code: "cached"}},
		{name: "nil for a pointer", call: withRes[*link], res: nil},
		{name: "nil for a slice", call: withRes[[]string], res: nil},
		{name: "nil for an interface", call: withRes[fmt.Stringer], res: nil},
		{name: "a type that implements the interface", call: withRes[fmt.Stringer], res: time.Second},
		{
			name:    "another type",
			call:    withRes[*link],
			res:     "cached",
			wantErr: "tyr: an interceptor returned string, want *tyr_test.link",
		},
		{
			name:    "a value for a pointer",
			call:    withRes[*link],
			res:     link{Code: "cached"},
			wantErr: "tyr: an interceptor returned tyr_test.link, want *tyr_test.link",
		},
		{
			name:    "nil for a struct",
			call:    withRes[link],
			res:     nil,
			wantErr: "tyr: an interceptor returned <nil>, want tyr_test.link",
		},
		{
			name:    "a type that doesn't implement the interface",
			call:    withRes[fmt.Stringer],
			res:     42,
			wantErr: "tyr: an interceptor returned int, want fmt.Stringer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			op := tt.call(api, func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return tt.res, nil
			})
			res, err := op.Call(t.Context(), nil)

			if tt.wantErr == "" {
				if err != nil || res != tt.res {
					t.Errorf("Call() = %v, %v; want %v, <nil>", res, err, tt.res)
				}
				return
			}
			// The transport gets an internal error rather than a result it
			// can't encode, and the logs get the cause.
			wantErr := "internal: internal error: " + tt.wantErr
			if res != nil || err == nil || err.Error() != wantErr {
				t.Errorf("Call() = %v, %v; want <nil>, %s", res, err, wantErr)
			}
			if len(rec.logs) != 1 || rec.logs[0].attrs["err"] != wantErr {
				t.Errorf("logged %+v, want the error", rec.logs)
			}
		})
	}
}

// withRes registers an operation with the result type Res, which never runs
// its handler, behind the interceptor ic.
func withRes[Res any](api *tyr.API, ic tyr.Interceptor) *tyr.Operation {
	api.Use(ic)
	return api.Handle("links.get", func(ctx context.Context, req getLinkReq) (Res, error) {
		panic("the handler ran")
	})
}

func TestInvokerErrors(t *testing.T) {
	errStoreNotFound := errors.New("store: not found")
	var nilErr *tyr.Error
	panics := func(ctx context.Context, req getLinkReq) (*link, error) { panic("boom") }
	fails := func(err error) tyr.Handler[getLinkReq, *link] {
		return func(ctx context.Context, req getLinkReq) (*link, error) { return nil, err }
	}

	tests := []struct {
		name       string
		inner      tyr.Interceptor // runs between the spy and the handler, if set
		handler    tyr.Handler[getLinkReq, *link]
		wantKind   tyr.Kind
		wantMsg    string
		wantMapped int // how many times the mapper runs
	}{
		{"mapped handler error", nil, fails(errStoreNotFound), tyr.KindNotFound, "link not found", 1},
		{"unmapped handler error", nil, fails(errors.New("db: connection refused")), tyr.KindInternal, "internal error", 1},
		{"handler panic", nil, panics, tyr.KindInternal, "internal error", 0},
		{"nil *tyr.Error from the handler", nil, fails(nilErr), tyr.KindInternal, "internal error", 0},
		{
			name: "interceptor error",
			inner: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return nil, errStoreNotFound
			},
			handler:    getLink,
			wantKind:   tyr.KindNotFound,
			wantMsg:    "link not found",
			wantMapped: 1,
		},
		{
			name: "interceptor panic",
			inner: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				panic("boom")
			},
			handler:  getLink,
			wantKind: tyr.KindInternal,
			wantMsg:  "internal error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
			mapped := 0
			api.MapError(func(err error) error {
				mapped++
				if errors.Is(err, errStoreNotFound) {
					return tyr.NotFound("link not found").WithCause(err)
				}
				return nil
			})
			var got error // what next returned to the spy
			api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				res, err := next(ctx, req)
				got = err
				return res, err
			})
			if tt.inner != nil {
				api.Use(tt.inner)
			}
			_, _ = api.Handle("links.get", tt.handler).Call(t.Context(), nil)

			// Exactly an *Error, not an error that wraps one.
			if e, ok := got.(*tyr.Error); !ok || e.Kind != tt.wantKind || e.Message != tt.wantMsg {
				t.Errorf("next returned %#v, want a *tyr.Error of kind %v with message %q", got, tt.wantKind, tt.wantMsg)
			}
			if mapped != tt.wantMapped {
				t.Errorf("the mapper ran %d times, want %d", mapped, tt.wantMapped)
			}
		})
	}
}

func TestInterceptorPanicLogging(t *testing.T) {
	tests := []struct {
		name    string
		outer   tyr.Interceptor // what the outermost interceptor does with the panic's error
		wantErr string          // the error of the call; "" for none
	}{
		{
			name:    "passes the error on",
			outer:   passThrough,
			wantErr: "internal: internal error: panic: boom",
		},
		{
			name: "turns it into a success",
			outer: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				if _, err := next(ctx, req); err != nil {
					return &link{Code: "fallback"}, nil
				}
				return nil, errors.New("the handler didn't panic")
			},
		},
		{
			name: "turns it into another error",
			outer: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				res, err := next(ctx, req)
				if err != nil {
					return nil, tyr.Unavailable("try again later").WithCause(err)
				}
				return res, nil
			},
			wantErr: "unavailable: try again later: internal: internal error: panic: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			api.Use(tt.outer)
			op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
				panic("boom")
			})

			_, err := op.Call(t.Context(), nil)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Errorf("Call() error = %v, want %q", err, tt.wantErr)
			}
			// The panic is logged once, where it was recovered, whatever
			// becomes of its error; the final result doesn't log it again.
			if len(rec.logs) != 1 || rec.logs[0].msg != "tyr: panic" || rec.logs[0].attrs["panic"] != "boom" {
				t.Errorf("logged %+v, want exactly one %q record", rec.logs, "tyr: panic")
			}
		})
	}
}

func TestInterceptorHandlesError(t *testing.T) {
	rec := &recorder{}
	api := tyr.New(tyr.WithLogger(slog.New(rec)))
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		if _, err := next(ctx, req); err != nil {
			return &link{Code: "fallback"}, nil
		}
		return nil, errors.New("the handler didn't fail")
	})
	op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, errors.New("db: connection refused")
	})

	// Errors other than panics are logged for the final result only, and
	// this call succeeds.
	if _, err := op.Call(t.Context(), nil); err != nil || len(rec.logs) != 0 {
		t.Errorf("Call() error = %v, logged %+v; want <nil>, nothing", err, rec.logs)
	}
}

func TestInterceptorContext(t *testing.T) {
	tenant := ctxkey.New[string]("tenant")
	var gotOp, fromCtx *tyr.Operation
	var gotTenant string

	api := tyr.New()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		gotOp = op
		fromCtx, _ = tyr.OperationFrom(ctx)
		return next(tenant.Set(ctx, "acme"), req)
	})
	op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		gotTenant, _ = tenant.Get(ctx)
		return nil, nil
	})
	if _, err := op.Call(t.Context(), nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if gotOp != op || fromCtx != op {
		t.Errorf("the interceptor got operations %v and %v from its context, want %q", gotOp, fromCtx, op.Name())
	}
	if gotTenant != "acme" {
		t.Errorf("the handler got tenant %q from its context, want %q", gotTenant, "acme")
	}
}
