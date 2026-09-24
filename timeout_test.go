package tyr_test

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr"
)

func TestTimeout(t *testing.T) {
	const d = time.Second
	goLink := &link{Code: "go", URL: "https://go.dev/go"}
	tests := []struct {
		name     string
		handler  tyr.Handler[getLinkReq, *link]
		wantKind tyr.Kind // of the error, if the call fails
		wantErr  bool
		wantTook time.Duration
		wantLogs []logged
	}{
		{
			name: "in time",
			handler: func(ctx context.Context, req getLinkReq) (*link, error) {
				time.Sleep(d / 2)
				return goLink, nil
			},
			wantTook: d / 2,
		},
		{
			name: "listens to its context",
			handler: func(ctx context.Context, req getLinkReq) (*link, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			wantKind: tyr.KindDeadlineExceeded,
			wantErr:  true,
			wantTook: d,
			wantLogs: []logged{{
				level: slog.LevelWarn,
				msg:   "tyr: operation failed",
				attrs: map[string]string{"err": "deadline_exceeded: deadline exceeded: context deadline exceeded"},
				op:    "links.get",
			}},
		},
		{
			// The work is done, so the client gets its result.
			name: "ignores its context",
			handler: func(ctx context.Context, req getLinkReq) (*link, error) {
				time.Sleep(2 * d)
				return goLink, nil
			},
			wantTook: 2 * d,
			wantLogs: []logged{{
				level: slog.LevelWarn,
				msg:   "tyr: call outlived its timeout",
				attrs: map[string]string{"timeout": "1s", "took": "2s"},
				op:    "links.get",
			}},
		},
		{
			name: "fails late",
			handler: func(ctx context.Context, req getLinkReq) (*link, error) {
				time.Sleep(2 * d)
				return nil, tyr.NotFound("link not found")
			},
			wantKind: tyr.KindNotFound,
			wantErr:  true,
			wantTook: 2 * d,
			wantLogs: []logged{{
				level: slog.LevelWarn,
				msg:   "tyr: call outlived its timeout",
				attrs: map[string]string{"timeout": "1s", "took": "2s"},
				op:    "links.get",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				op := tyr.New(tyr.WithLogger(slog.New(rec))).Handle("links.get", tt.handler, tyr.Timeout(d))

				start := time.Now()
				res, err := op.Call(t.Context(), nil)
				took := time.Since(start)

				switch e, ok := err.(*tyr.Error); {
				case !tt.wantErr && err != nil:
					t.Errorf("Call() error = %v, want <nil>", err)
				case !tt.wantErr && res != goLink:
					t.Errorf("Call() = %v, want the handler's result", res)
				case tt.wantErr && (!ok || e.Kind != tt.wantKind):
					t.Errorf("Call() error = %v, want %v", err, tt.wantKind)
				}
				if took != tt.wantTook {
					t.Errorf("Call() took %v, want %v", took, tt.wantTook)
				}
				if !reflect.DeepEqual(rec.logs, tt.wantLogs) {
					t.Errorf("logged %+v, want %+v", rec.logs, tt.wantLogs)
				}
			})
		})
	}
}

func TestTimeoutOptions(t *testing.T) {
	// The handler reports the time its context has left.
	left := func(ctx context.Context, req getLinkReq) (*link, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, nil
		}
		return &link{Code: time.Until(deadline).String()}, nil
	}
	synctest.Test(t, func(t *testing.T) {
		api := tyr.New()
		group := api.Group(tyr.Timeout(time.Second))
		nested := group.Group(tyr.Timeout(2 * time.Second))
		ops := map[string]*tyr.Operation{
			"none":   api.Handle("links.none", left),
			"group":  group.Handle("links.group", left),
			"nested": nested.Handle("links.nested", left),
			"own":    nested.Handle("links.own", left, tyr.Timeout(3*time.Second)),
		}
		// The last option applied wins: the group's, the nested group's,
		// then the operation's own.
		want := map[string]string{"none": "", "group": "1s", "nested": "2s", "own": "3s"}
		for name, op := range ops {
			res, err := op.Call(t.Context(), nil)
			got := ""
			if l, _ := res.(*link); l != nil {
				got = l.Code
			}
			if err != nil || got != want[name] {
				t.Errorf("%s: the handler had %q left, error %v; want %q", name, got, err, want[name])
			}
		}

		// An earlier deadline of the caller stays.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second/2)
		defer cancel()
		if res, err := ops["own"].Call(ctx, nil); err != nil || res.(*link).Code != "500ms" {
			t.Errorf("with the caller's deadline, the handler had %v left, error %v; want 500ms", res, err)
		}
	})
}

func TestTimeoutInterceptors(t *testing.T) {
	// The timeout covers the whole chain: the interceptors get its
	// deadline too, and the time they take counts.
	synctest.Test(t, func(t *testing.T) {
		api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
		var seen []time.Duration // the time the interceptor's context had left
		api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
			deadline, _ := ctx.Deadline()
			seen = append(seen, time.Until(deadline))
			time.Sleep(time.Second / 2)
			return next(ctx, req)
		})
		op := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, tyr.Timeout(time.Second))

		start := time.Now()
		_, err := op.Call(t.Context(), nil)
		if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindDeadlineExceeded {
			t.Errorf("Call() error = %v, want deadline_exceeded", err)
		}
		if took := time.Since(start); took != time.Second {
			t.Errorf("Call() took %v, want 1s", took)
		}
		if !slices.Equal(seen, []time.Duration{time.Second}) {
			t.Errorf("the interceptor had %v left, want [1s]", seen)
		}
	})
}

func TestTimeoutCanceled(t *testing.T) {
	// A caller that goes away cancels the call, as without a timeout, and
	// the call that stopped then isn't one that outlived its timeout.
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		op := tyr.New(tyr.WithLogger(slog.New(rec))).Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
			<-ctx.Done()
			time.Sleep(time.Second) // it takes a while to stop
			return nil, ctx.Err()
		}, tyr.Timeout(time.Second))
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Second/2, cancel)

		_, err := op.Call(ctx, nil)
		if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindCanceled {
			t.Errorf("Call() error = %v, want canceled", err)
		}
		if len(rec.logs) != 0 {
			t.Errorf("logged %+v, want nothing", rec.logs)
		}
	})
}

func TestTimeoutErrors(t *testing.T) {
	// Timeout declares deadline_exceeded for the documentation, once, after
	// the kinds declared before it.
	api := tyr.New()
	op := api.Group(tyr.Errors(tyr.KindNotFound), tyr.Timeout(time.Second)).
		Handle("links.get", getLink, tyr.Timeout(time.Minute), tyr.Errors(tyr.KindUnavailable))
	want := []tyr.Kind{tyr.KindNotFound, tyr.KindDeadlineExceeded, tyr.KindUnavailable}
	if got := op.Doc().Errors; !slices.Equal(got, want) {
		t.Errorf("Doc().Errors = %v, want %v", got, want)
	}
}

func TestTimeoutPanics(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		got := panicValue(func() { tyr.Timeout(d) })
		if s, _ := got.(string); !strings.HasPrefix(s, "tyr: Timeout(") || !strings.HasSuffix(s, "): want a positive duration") {
			t.Errorf("Timeout(%v) panicked with %v, want a positive duration", d, got)
		}
	}
}
