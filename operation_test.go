package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
)

// decodeTo returns a decode that fills in req, as a transport would.
func decodeTo(req getLinkReq) func(dst any) error {
	return func(dst any) error {
		*dst.(*getLinkReq) = req
		return nil
	}
}

func TestCall(t *testing.T) {
	op := tyr.New().Handle("links.get", getLink)

	res, err := op.Call(t.Context(), decodeTo(getLinkReq{Code: "go"}))
	// A nil *tyr.Error in the error interface isn't nil and fails here.
	if err != nil {
		t.Fatalf("Call() error = %#v, want nil", err)
	}
	want := link{Code: "go", URL: "https://go.dev/go"}
	if got, ok := res.(*link); !ok || *got != want {
		t.Errorf("Call() = %#v, want &%#v", res, want)
	}
}

func TestCallDecode(t *testing.T) {
	errSyntax := errors.New("unexpected end of JSON input")
	errLong := tyr.InvalidArgument("code is too long")

	var got *getLinkReq // the request the handler got
	op := tyr.New().Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		got = &req
		return nil, nil
	})

	tests := []struct {
		name    string
		decode  func(dst any) error
		wantReq *getLinkReq // nil if the handler must not be called
		wantErr error       // matched with errors.Is
		wantMsg string
	}{
		{"fills in the request", decodeTo(getLinkReq{Code: "go"}), &getLinkReq{Code: "go"}, nil, ""},
		{"nil decode leaves the request zero", nil, &getLinkReq{}, nil, ""},
		{"plain error", func(any) error { return errSyntax }, nil, errSyntax, "invalid request"},
		{"Error", func(any) error { return fmt.Errorf("decode: %w", errLong) }, nil, errLong, "code is too long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			_, err := op.Call(t.Context(), tt.decode)

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Call() error = %v, want <nil>", err)
				}
			} else {
				e, ok := err.(*tyr.Error)
				if !ok || e.Kind != tyr.KindInvalidArgument || e.Message != tt.wantMsg || !errors.Is(err, tt.wantErr) {
					t.Errorf("Call() error = %v, want invalid_argument %q from %v", err, tt.wantMsg, tt.wantErr)
				}
			}
			if !reflect.DeepEqual(got, tt.wantReq) {
				t.Errorf("the handler got %+v, want %+v", got, tt.wantReq)
			}
		})
	}
}

func TestCallErrors(t *testing.T) {
	errStoreNotFound := errors.New("store: not found")
	notFound := tyr.NotFound("link not found")

	var mapped []error // what the first mapper got
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
	api.MapError(func(err error) error {
		mapped = append(mapped, err)
		return nil // ignored
	})
	api.MapError(func(err error) error {
		return errors.New("no tyr.Error here") // ignored
	})
	api.MapError(func(err error) error {
		// This only matches if the mapper gets the original error rather
		// than the previous mapper's result.
		if errors.Is(err, errStoreNotFound) {
			return fmt.Errorf("mapped: %w", notFound.WithCause(err))
		}
		return err
	})
	var handlerErr error
	op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, handlerErr
	})

	tests := []struct {
		name       string
		err        error // returned by the handler
		wantKind   tyr.Kind
		wantMsg    string
		wantMapped bool // whether the error goes through the mappers
	}{
		{"Error", notFound, tyr.KindNotFound, "link not found", false},
		{"wrapped Error", fmt.Errorf("get: %w", notFound), tyr.KindNotFound, "link not found", false},
		{"mapped", fmt.Errorf("get: %w", errStoreNotFound), tyr.KindNotFound, "link not found", true},
		{"deadline", fmt.Errorf("db: %w", context.DeadlineExceeded), tyr.KindDeadlineExceeded, "deadline exceeded", true},
		// The context of the call is alive: see TestCallCanceled.
		{"canceled", fmt.Errorf("db: %w", context.Canceled), tyr.KindInternal, "internal error", true},
		{"unmapped", errors.New("db: connection refused"), tyr.KindInternal, "internal error", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlerErr, mapped = tt.err, nil
			res, err := op.Call(t.Context(), nil)

			e, ok := err.(*tyr.Error)
			if !ok {
				t.Fatalf("Call() error = %#v, want a *tyr.Error", err)
			}
			if res != nil {
				t.Errorf("Call() result = %#v, want nil", res)
			}
			if e.Kind != tt.wantKind || e.Message != tt.wantMsg {
				t.Errorf("Call() error = %v, want %v with message %q", e, tt.wantKind, tt.wantMsg)
			}
			// The handler's error is reduced to the Error it contains or
			// becomes the cause.
			if inner, ok := errors.AsType[*tyr.Error](tt.err); ok {
				if e != inner {
					t.Errorf("Call() error = %v, want the handler's own *tyr.Error", e)
				}
			} else if !errors.Is(e, tt.err) {
				t.Errorf("Call() error %v doesn't wrap the handler's error %v", e, tt.err)
			}
			var wantMapped []error
			if tt.wantMapped {
				wantMapped = []error{tt.err}
			}
			if !slices.Equal(mapped, wantMapped) {
				t.Errorf("the mappers got %v, want %v", mapped, wantMapped)
			}
		})
	}
}

func TestCallCanceled(t *testing.T) {
	// A context.Canceled is a canceled call only if the context of the link
	// it came from is canceled too.
	canceled := func(ctx context.Context) context.Context {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	tests := []struct {
		name     string
		byCaller bool            // whether the caller of Call cancels it
		inner    tyr.Interceptor // runs around the handler, if set
		wantKind tyr.Kind
		wantMsg  string
	}{
		{name: "by the caller", byCaller: true, wantKind: tyr.KindCanceled, wantMsg: "canceled"},
		{name: "not by the caller", wantKind: tyr.KindInternal, wantMsg: "internal error"},
		{
			// The interceptor is the caller of the handler.
			name: "by an interceptor",
			inner: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return next(canceled(ctx), req)
			},
			wantKind: tyr.KindCanceled,
			wantMsg:  "canceled",
		},
		{
			name:     "in an interceptor",
			byCaller: true,
			inner: func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return nil, ctx.Err()
			},
			wantKind: tyr.KindCanceled,
			wantMsg:  "canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			if tt.inner != nil {
				api.Use(tt.inner)
			}
			op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
				return nil, fmt.Errorf("db: %w", context.Canceled) // whatever its context
			})
			ctx := t.Context()
			if tt.byCaller {
				ctx = canceled(ctx)
			}
			_, err := op.Call(ctx, nil)

			if e, ok := err.(*tyr.Error); !ok || e.Kind != tt.wantKind || e.Message != tt.wantMsg || !errors.Is(err, context.Canceled) {
				t.Errorf("Call() error = %v, want %v with message %q, caused by context.Canceled", err, tt.wantKind, tt.wantMsg)
			}
			// Nothing failed on the side of the service.
			if tt.wantKind == tyr.KindCanceled && len(rec.logs) != 0 {
				t.Errorf("logged %+v, want nothing", rec.logs)
			}
		})
	}
}

func TestCallNilError(t *testing.T) {
	var nilErr *tyr.Error
	const fromNil = "internal: internal error: tyr: a nil *tyr.Error was returned as an error"

	tests := []struct {
		name  string
		setup func(api *tyr.API) tyr.Handler[getLinkReq, *link]
		want  string // the error of the call
	}{
		{
			name: "from the handler",
			setup: func(api *tyr.API) tyr.Handler[getLinkReq, *link] {
				return func(ctx context.Context, req getLinkReq) (*link, error) { return nil, nilErr }
			},
			want: fromNil,
		},
		{
			name: "from an interceptor",
			setup: func(api *tyr.API) tyr.Handler[getLinkReq, *link] {
				api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
					return nil, nilErr
				})
				return getLink
			},
			want: fromNil,
		},
		{
			name: "from a mapper, which is skipped",
			setup: func(api *tyr.API) tyr.Handler[getLinkReq, *link] {
				api.MapError(func(error) error { return nilErr })
				return func(ctx context.Context, req getLinkReq) (*link, error) {
					return nil, errors.New("db: connection refused")
				}
			},
			want: "internal: internal error: db: connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
			op := api.Handle("links.get", tt.setup(api))
			if _, err := op.Call(t.Context(), nil); err == nil || err.Error() != tt.want {
				t.Errorf("Call() error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestCallPanics(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name  string
		where string // "handler", "decode" or "mapper"
		value any    // what to panic with
	}{
		{"in the handler", "handler", errBoom},
		{"in the handler, with a non-error value", "handler", "boom"},
		{"in decode", "decode", errBoom},
		{"in a mapper", "mapper", errBoom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			boom := func() { panic(tt.value) }
			handler := getLink
			var decode func(dst any) error
			switch tt.where {
			case "handler":
				handler = func(ctx context.Context, req getLinkReq) (*link, error) {
					boom()
					return nil, nil
				}
			case "decode":
				decode = func(any) error {
					boom()
					return nil
				}
			case "mapper":
				api.MapError(func(error) error {
					boom()
					return nil
				})
				handler = func(ctx context.Context, req getLinkReq) (*link, error) {
					return nil, errors.New("db: connection refused")
				}
			}
			res, err := api.Handle("links.get", handler).Call(t.Context(), decode)

			if want := "internal: internal error: panic: boom"; res != nil || err == nil || err.Error() != want {
				t.Errorf("Call() = %v, %v; want <nil>, %s", res, err, want)
			}
			if v, ok := tt.value.(error); ok && !errors.Is(err, v) {
				t.Errorf("Call() error %v doesn't wrap the panic value %v", err, v)
			}
			if len(rec.logs) != 1 {
				t.Fatalf("logged %d records, want 1", len(rec.logs))
			}
			l := rec.logs[0]
			if l.level != slog.LevelError || l.msg != "tyr: panic" || l.attrs["panic"] != "boom" || l.op != "links.get" {
				t.Errorf("logged %+v, want an error %q with the panic and the operation", l, "tyr: panic")
			}
			if !strings.HasPrefix(l.attrs["stack"], "goroutine ") {
				t.Errorf("logged stack %q, want a goroutine stack", l.attrs["stack"])
			}
		})
	}

	t.Run("http.ErrAbortHandler", func(t *testing.T) {
		rec := &recorder{}
		op := tyr.New(tyr.WithLogger(slog.New(rec))).Handle("links.get",
			func(ctx context.Context, req getLinkReq) (*link, error) { panic(http.ErrAbortHandler) })

		if got := panicValue(func() { _, _ = op.Call(t.Context(), nil) }); got != http.ErrAbortHandler {
			t.Errorf("Call() panicked with %v, want http.ErrAbortHandler", got)
		}
		if len(rec.logs) != 0 {
			t.Errorf("logged %+v, want nothing", rec.logs)
		}
	})
}

func TestCallLogging(t *testing.T) {
	tests := []struct {
		name      string
		err       error // returned by the handler
		canceled  bool  // whether the caller canceled the call
		wantLevel slog.Level
		wantErr   string // the logged error; "" if nothing is logged
	}{
		{"internal", errors.New("db: connection refused"), false, slog.LevelError, "internal: internal error: db: connection refused"},
		{"explicit internal", tyr.Internal("storage is read-only"), false, slog.LevelError, "internal: storage is read-only"},
		{"deadline", context.DeadlineExceeded, false, slog.LevelWarn, "deadline_exceeded: deadline exceeded: context deadline exceeded"},
		{"unavailable", tyr.Unavailable("try again later"), false, slog.LevelWarn, "unavailable: try again later"},
		{"not found", tyr.NotFound("link not found"), false, 0, ""},
		{"invalid argument", tyr.InvalidArgument("code is too long"), false, 0, ""},
		{"canceled by the caller", context.Canceled, true, 0, ""},
		{"canceled inside", context.Canceled, false, slog.LevelError, "internal: internal error: context canceled"},
		{"canceled kind", tyr.Canceled("stopped"), false, 0, ""},
		{"internal, caused by the caller's cancellation", tyr.Internal("query failed").WithCause(context.Canceled), true, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			op := tyr.New(tyr.WithLogger(slog.New(rec))).Handle("links.get",
				func(ctx context.Context, req getLinkReq) (*link, error) { return nil, tt.err })
			ctx, cancel := context.WithCancel(t.Context())
			if tt.canceled {
				cancel()
			}
			defer cancel()

			_, _ = op.Call(ctx, nil)

			var want []logged
			if tt.wantErr != "" {
				// No attributes but the error: the context carries the rest.
				want = []logged{{
					level: tt.wantLevel,
					msg:   "tyr: operation failed",
					attrs: map[string]string{"err": tt.wantErr},
					op:    "links.get",
				}}
			}
			if !reflect.DeepEqual(rec.logs, want) {
				t.Errorf("logged %+v, want %+v", rec.logs, want)
			}
		})
	}
}

func TestCallLoggingInternal(t *testing.T) {
	// Clients get neither the message nor the details of an internal error,
	// or of an error of a kind tyr doesn't define, so the logs get both.
	tests := []struct {
		name string
		err  error
		want map[string]string // the attributes of the record
	}{
		{
			name: "internal with details",
			err:  tyr.Internal("storage is read-only").WithDetails("disk 3"),
			want: map[string]string{"err": "internal: storage is read-only", "details": "disk 3"},
		},
		{
			name: "unknown kind",
			err:  &tyr.Error{Kind: tyr.Kind(42), Message: "odd", Details: 7},
			want: map[string]string{"err": "Kind(42): odd", "details": "7"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			op := tyr.New(tyr.WithLogger(slog.New(rec))).Handle("links.get",
				func(ctx context.Context, req getLinkReq) (*link, error) { return nil, tt.err })
			_, _ = op.Call(t.Context(), nil)

			want := []logged{{level: slog.LevelError, msg: "tyr: operation failed", attrs: tt.want, op: "links.get"}}
			if !reflect.DeepEqual(rec.logs, want) {
				t.Errorf("logged %+v, want %+v", rec.logs, want)
			}
		})
	}
}

// TestCallLoggingDefault replaces the global default logger, so it must not
// run in parallel with other tests.
func TestCallLoggingDefault(t *testing.T) {
	// Without WithLogger, the API logs to slog.Default as it is at the time
	// of logging, so it sees a default set after the API was created.
	op := tyr.New().Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, errors.New("db: connection refused")
	})

	rec := &recorder{}
	useDefaultLogger(t, slog.New(rec))

	_, _ = op.Call(t.Context(), nil)
	if len(rec.logs) != 1 || rec.logs[0].msg != "tyr: operation failed" {
		t.Errorf("slog.Default() got %+v, want the failed call", rec.logs)
	}
}

// useDefaultLogger makes l the default logger until the end of the test.
// That's global state, so a test that calls it must not run in parallel
// with other tests.
func useDefaultLogger(t *testing.T, l *slog.Logger) {
	// slog.SetDefault redirects the log package too, so restore it as well.
	logger, out, flags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(logger)
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	slog.SetDefault(l)
}

func TestCallConcurrent(t *testing.T) {
	api := tyr.New()
	api.Use(passThrough)
	op := api.Handle("links.get", getLink)
	api.Seal()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			code := strconv.Itoa(i)
			res, err := op.Call(t.Context(), decodeTo(getLinkReq{Code: code}))
			if l, ok := res.(*link); err != nil || !ok || l.Code != code {
				t.Errorf("Call(%q) = %v, %v", code, res, err)
			}
		})
	}
	wg.Wait()
}

func BenchmarkCall(b *testing.B) {
	// The handler allocates nothing: what's measured is Call's own.
	goLink := &link{Code: "go", URL: "https://go.dev/go"}
	for _, n := range []int{0, 3} {
		b.Run(fmt.Sprintf("%d interceptors", n), func(b *testing.B) {
			api := tyr.New()
			for range n {
				api.Use(passThrough)
			}
			op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
				return goLink, nil
			})
			api.Seal()
			ctx, decode := b.Context(), decodeTo(getLinkReq{Code: "go"})
			b.ReportAllocs()
			for b.Loop() {
				_, _ = op.Call(ctx, decode)
			}
		})
	}
	// The checks of enums: a request of one and a result of ten, against
	// the same shapes of strings.
	type (
		plainReq  struct{ Status string }
		plainTask struct {
			Title  string `json:"title"`
			Status string `json:"status"`
		}
		enumReq struct{ Status state }
	)
	plainTasks, enumTasks := make([]plainTask, 10), make([]task, 10)
	for i := range 10 {
		plainTasks[i], enumTasks[i] = plainTask{"Task", "done"}, task{"Task", stateDone}
	}
	b.Run("strings", func(b *testing.B) {
		api := tyr.New()
		op := api.Handle("tasks.list", func(ctx context.Context, req plainReq) ([]plainTask, error) { return plainTasks, nil })
		api.Seal()
		ctx := b.Context()
		decode := func(dst any) error { *dst.(*plainReq) = plainReq{Status: "done"}; return nil }
		b.ReportAllocs()
		for b.Loop() {
			_, _ = op.Call(ctx, decode)
		}
	})
	b.Run("enums", func(b *testing.B) {
		api := tyr.New()
		op := api.Handle("tasks.list", func(ctx context.Context, req enumReq) ([]task, error) { return enumTasks, nil })
		api.Seal()
		ctx := b.Context()
		decode := func(dst any) error { *dst.(*enumReq) = enumReq{Status: stateDone}; return nil }
		b.ReportAllocs()
		for b.Loop() {
			_, _ = op.Call(ctx, decode)
		}
	})
	// The context of a timeout and its timer.
	b.Run("timeout", func(b *testing.B) {
		api := tyr.New()
		op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
			return goLink, nil
		}, tyr.Timeout(time.Second))
		api.Seal()
		ctx, decode := b.Context(), decodeTo(getLinkReq{Code: "go"})
		b.ReportAllocs()
		for b.Loop() {
			_, _ = op.Call(ctx, decode)
		}
	})
}

// recorder is a slog.Handler that keeps what it logs.
type recorder struct {
	logs []logged
}

// logged is a record a recorder got.
type logged struct {
	level slog.Level
	msg   string
	attrs map[string]string
	op    string // the operation the record's context carries
}

func (r *recorder) Enabled(context.Context, slog.Level) bool {
	return true
}

func (r *recorder) Handle(ctx context.Context, rec slog.Record) error {
	l := logged{level: rec.Level, msg: rec.Message, attrs: map[string]string{}}
	rec.Attrs(func(a slog.Attr) bool {
		l.attrs[a.Key] = a.Value.String()
		return true
	})
	if op, ok := tyr.OperationFrom(ctx); ok {
		l.op = op.Name()
	}
	r.logs = append(r.logs, l)
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler {
	return r
}

func (r *recorder) WithGroup(string) slog.Handler {
	return r
}
