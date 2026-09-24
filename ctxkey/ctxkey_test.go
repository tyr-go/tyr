package ctxkey_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/tyr-go/tyr/ctxkey"
)

func TestKeyGet(t *testing.T) {
	id := ctxkey.New[string]("id")
	sameName := ctxkey.New[string]("id")
	sameNameInt := ctxkey.New[int]("id")

	tests := []struct {
		name   string
		ctx    func(context.Context) context.Context
		want   string
		wantOK bool
	}{
		{
			name: "missing",
			ctx:  func(ctx context.Context) context.Context { return ctx },
		},
		{
			name:   "set",
			ctx:    func(ctx context.Context) context.Context { return id.Set(ctx, "a") },
			want:   "a",
			wantOK: true,
		},
		{
			name:   "zero value is found",
			ctx:    func(ctx context.Context) context.Context { return id.Set(ctx, "") },
			wantOK: true,
		},
		{
			name:   "nearest value wins",
			ctx:    func(ctx context.Context) context.Context { return id.Set(id.Set(ctx, "a"), "b") },
			want:   "b",
			wantOK: true,
		},
		{
			name: "parent is unchanged",
			ctx: func(ctx context.Context) context.Context {
				parent := id.Set(ctx, "a")
				_ = id.Set(parent, "b")
				return parent
			},
			want:   "a",
			wantOK: true,
		},
		{
			name: "inherited by derived contexts",
			ctx: func(ctx context.Context) context.Context {
				return context.WithoutCancel(sameNameInt.Set(id.Set(ctx, "a"), 1))
			},
			want:   "a",
			wantOK: true,
		},
		{
			name: "no collision: same name and type",
			ctx:  func(ctx context.Context) context.Context { return sameName.Set(ctx, "a") },
		},
		{
			name: "no collision: same name, other type",
			ctx:  func(ctx context.Context) context.Context { return sameNameInt.Set(ctx, 1) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := id.Get(tt.ctx(t.Context()))
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Get() = %q, %t; want %q, %t", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestKeyGetNil(t *testing.T) {
	t.Run("nil pointer is found", func(t *testing.T) {
		k := ctxkey.New[*int]("ptr")
		got, ok := k.Get(k.Set(t.Context(), nil))
		if got != nil || !ok {
			t.Errorf("Get() = %v, %t; want <nil>, true", got, ok)
		}
	})
	t.Run("nil interface is not found", func(t *testing.T) {
		k := ctxkey.New[error]("err")
		got, ok := k.Get(k.Set(t.Context(), nil))
		if got != nil || ok {
			t.Errorf("Get() = %v, %t; want <nil>, false", got, ok)
		}
	})
	t.Run("interface value is found", func(t *testing.T) {
		k := ctxkey.New[error]("err")
		got, ok := k.Get(k.Set(t.Context(), io.EOF))
		if got != io.EOF || !ok {
			t.Errorf("Get() = %v, %t; want %v, true", got, ok, io.EOF)
		}
	})
}

func TestKeyMustGet(t *testing.T) {
	k := ctxkey.New[string]("request_id")

	if got := k.MustGet(k.Set(t.Context(), "abc")); got != "abc" {
		t.Errorf("MustGet() = %q, want %q", got, "abc")
	}

	got := panicValue(func() { k.MustGet(t.Context()) })
	if want := `ctxkey: no value for key "request_id"`; got != want {
		t.Errorf("MustGet() on missing value panicked with %v, want %q", got, want)
	}
}

func TestKeyString(t *testing.T) {
	k := ctxkey.New[string]("request_id")

	if got := k.String(); got != "request_id" {
		t.Errorf("String() = %q, want %q", got, "request_id")
	}
	// The context package prints keys through their String method.
	if got := fmt.Sprint(k.Set(t.Context(), "abc")); !strings.Contains(got, "request_id") {
		t.Errorf("fmt.Sprint(ctx) = %q, want it to contain %q", got, "request_id")
	}
}

func TestNilKey(t *testing.T) {
	var k *ctxkey.Key[string]

	tests := []struct {
		name string
		call func(context.Context)
	}{
		{"Set", func(ctx context.Context) { _ = k.Set(ctx, "a") }},
		{"Get", func(ctx context.Context) { _, _ = k.Get(ctx) }},
		{"MustGet", func(ctx context.Context) { _ = k.MustGet(ctx) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := panicValue(func() { tt.call(t.Context()) })
			if want := "ctxkey: nil key"; got != want {
				t.Errorf("%s on nil key panicked with %v, want %q", tt.name, got, want)
			}
		})
	}
}

// panicValue calls f and returns the value it panicked with, or nil.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}
