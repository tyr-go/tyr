package store_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/tyr-go/tyr/examples/shortlink/store"
)

func TestStore(t *testing.T) {
	ctx := t.Context()
	s := store.New()
	golang := store.Link{Code: "golang", URL: "https://go.dev"}
	if err := s.Create(ctx, golang); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := s.Create(ctx, store.Link{Code: "golang", URL: "https://golang.org"}); !errors.Is(err, store.ErrExists) {
		t.Errorf("Create() of a taken code: error = %v, want ErrExists", err)
	}
	if l, err := s.Get(ctx, "golang"); err != nil || l != golang {
		t.Errorf("Get() = %v, %v; want %v", l, err, golang)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() of a missing code: error = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "golang"); err != nil {
		t.Errorf("Delete() error = %v", err)
	}
	if err := s.Delete(ctx, "golang"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete() of a missing code: error = %v, want ErrNotFound", err)
	}
	if err := s.Create(ctx, golang); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := s.Create(ctx, store.Link{Code: "rust", URL: "https://rust-lang.org"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if n := s.DeleteFunc(ctx, func(l store.Link) bool { return l.Code == "rust" }); n != 1 {
		t.Errorf("DeleteFunc() = %d, want 1", n)
	}
	if _, err := s.Get(ctx, "rust"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() of a deleted code: error = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "golang"); err != nil {
		t.Errorf("Get() of a kept code: error = %v", err)
	}
}

func TestStoreConcurrent(t *testing.T) {
	ctx := t.Context()
	s := store.New()
	var wg sync.WaitGroup
	for i := range 8 {
		code := strconv.Itoa(i)
		wg.Go(func() {
			if err := s.Create(ctx, store.Link{Code: code}); err != nil {
				t.Errorf("Create(%s) error = %v", code, err)
			}
			if _, err := s.Get(ctx, code); err != nil {
				t.Errorf("Get(%s) error = %v", code, err)
			}
		})
	}
	wg.Go(func() { s.DeleteFunc(ctx, func(store.Link) bool { return false }) })
	wg.Wait()
}
