package tyr_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestMetaKeyGet(t *testing.T) {
	key := tyr.NewMetaKey[[]string]("authz.roles")
	api := tyr.New()
	without := api.Handle("links.get", getLink)
	with := api.Handle("links.purge", getLink, key.Option([]string{"admin"}))

	if got, ok := key.Get(without); got != nil || ok {
		t.Errorf("Get(operation without the option) = %v, %t; want <nil>, false", got, ok)
	}
	if got, ok := key.Get(with); !ok || !slices.Equal(got, []string{"admin"}) {
		t.Errorf("Get(operation with the option) = %v, %t; want [admin], true", got, ok)
	}
}

func TestMetaKeyLastWins(t *testing.T) {
	key := tyr.NewMetaKey[string]("level")
	api := tyr.New()
	outer := api.Group(key.Option("outer"))
	inner := outer.Group(key.Option("inner"))

	tests := []struct {
		op   *tyr.Operation
		want string
	}{
		{outer.Handle("levels.outer", getLink), "outer"},
		{inner.Handle("levels.inner", getLink), "inner"},
		{inner.Handle("levels.own", getLink, key.Option("own")), "own"},
		{api.Handle("levels.twice", getLink, key.Option("first"), key.Option("second")), "second"},
	}
	for _, tt := range tests {
		if got, ok := key.Get(tt.op); !ok || got != tt.want {
			t.Errorf("Get(%s) = %q, %t; want %q, true", tt.op.Name(), got, ok, tt.want)
		}
	}
}

func TestMetaKeyIdentity(t *testing.T) {
	key := tyr.NewMetaKey[string]("key")
	sameName := tyr.NewMetaKey[string]("key")
	sameNameInt := tyr.NewMetaKey[int]("key")
	op := tyr.New().Handle("links.get", getLink, key.Option("value"))

	if got, ok := key.Get(op); !ok || got != "value" {
		t.Errorf("Get() = %q, %t; want %q, true", got, ok, "value")
	}
	if got, ok := sameName.Get(op); ok {
		t.Errorf("Get() with another key of the same name and type = %q, true; want false", got)
	}
	if got, ok := sameNameInt.Get(op); ok {
		t.Errorf("Get() with another key of the same name = %d, true; want false", got)
	}
}

func TestMetaKeyNilValue(t *testing.T) {
	t.Run("nil interface", func(t *testing.T) {
		key := tyr.NewMetaKey[error]("err")
		op := tyr.New().Handle("links.get", getLink, key.Option(nil))
		// Unlike with ctxkey, a nil value that an option set is found.
		if got, ok := key.Get(op); got != nil || !ok {
			t.Errorf("Get() = %v, %t; want <nil>, true", got, ok)
		}
	})
	t.Run("nil pointer", func(t *testing.T) {
		key := tyr.NewMetaKey[*int]("ptr")
		op := tyr.New().Handle("links.get", getLink, key.Option(nil))
		if got, ok := key.Get(op); got != nil || !ok {
			t.Errorf("Get() = %v, %t; want <nil>, true", got, ok)
		}
	})
}

func TestMetaKeyString(t *testing.T) {
	if got := tyr.NewMetaKey[[]string]("authz.roles").String(); got != "authz.roles" {
		t.Errorf("String() = %q, want %q", got, "authz.roles")
	}
}

func TestNilMetaKey(t *testing.T) {
	var key *tyr.MetaKey[string]
	op := tyr.New().Handle("links.get", getLink)

	tests := []struct {
		name string
		call func()
	}{
		{"Option", func() { _ = key.Option("value") }},
		{"Get", func() { _, _ = key.Get(op) }},
	}
	for _, tt := range tests {
		if got, want := panicValue(tt.call), "tyr: nil MetaKey"; got != want {
			t.Errorf("%s on a nil key panicked with %v, want %q", tt.name, got, want)
		}
	}
}

func TestMetaKeyLateWrite(t *testing.T) {
	key := tyr.NewMetaKey[[]string]("authz.roles")
	const want = `tyr: MetaKey "authz.roles": option applied to registered operation "links.get"`

	t.Run("after registration", func(t *testing.T) {
		op := tyr.New().Handle("links.get", getLink)
		if got := panicValue(func() { key.Option([]string{"admin"})(op) }); got != want {
			t.Errorf("applying an option to a registered operation panicked with %v, want %q", got, want)
		}
		if _, ok := key.Get(op); ok {
			t.Error("the late option set the key")
		}
	})

	t.Run("in a call", func(t *testing.T) {
		rec := &recorder{}
		api := tyr.New(tyr.WithLogger(slog.New(rec)))
		api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
			key.Option([]string{"admin"})(op)
			return next(ctx, req)
		})
		op := api.Handle("links.get", getLink)

		// The panic becomes an internal error, logged with its stack.
		if _, err := op.Call(t.Context(), nil); err == nil || err.Error() != "internal: internal error: panic: "+want {
			t.Errorf("Call() error = %v, want the recovered panic", err)
		}
		if len(rec.logs) != 1 || rec.logs[0].attrs["panic"] != want || !strings.HasPrefix(rec.logs[0].attrs["stack"], "goroutine ") {
			t.Errorf("logged %+v, want one record of the panic with its stack", rec.logs)
		}
	})
}

func TestMetaKeyConcurrent(t *testing.T) {
	key := tyr.NewMetaKey[[]string]("authz.roles")
	api := tyr.New()
	api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		if roles, ok := key.Get(op); !ok || !slices.Equal(roles, []string{"admin"}) {
			return nil, errors.New("no roles")
		}
		return next(ctx, req)
	})
	op := api.Handle("links.get", getLink, key.Option([]string{"admin"}))
	api.Seal()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := op.Call(t.Context(), nil); err != nil {
				t.Errorf("Call() error = %v", err)
			}
		})
	}
	wg.Wait()
}
