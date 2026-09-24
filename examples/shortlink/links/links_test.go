package links

import (
	"errors"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/examples/shortlink/contract"
	"github.com/tyr-go/tyr/examples/shortlink/store"
)

func TestCreateTakenRandomCode(t *testing.T) {
	s := New(store.New())
	tries := 0
	s.newCode = func() string {
		tries++
		return "same"
	}
	ctx := t.Context()
	if _, err := s.Create(ctx, contract.CreateReq{URL: "https://go.dev"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Every random code is taken: the client didn't choose it, so the
	// error is an internal one, which MapError doesn't turn into the
	// conflict of ErrExists, with ErrExists as its cause for the logs.
	tries = 0
	_, err := s.Create(ctx, contract.CreateReq{URL: "https://go.dev"})
	if e, ok := errors.AsType[*tyr.Error](err); !ok || e.Kind != tyr.KindInternal || !errors.Is(err, store.ErrExists) || tries != 3 {
		t.Errorf("Create() error = %v after %d tries, want an internal error caused by ErrExists after 3", err, tries)
	}
}
