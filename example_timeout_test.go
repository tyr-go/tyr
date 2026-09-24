package tyr_test

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tyr-go/tyr"
)

type buildReq struct {
	Month string `json:"month"`
}

type report struct {
	Total int `json:"total"`
}

func ExampleTimeout() {
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
	// The operations of the group get 10 milliseconds; one of its own could
	// set another timeout.
	reports := api.Group(tyr.Timeout(10 * time.Millisecond))
	build := reports.Handle("reports.build", func(ctx context.Context, req buildReq) (*report, error) {
		// The handler listens to its context: Timeout doesn't cut it short.
		select {
		case <-time.After(time.Minute): // the report takes a while
			return &report{Total: 42}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	_, err := build.Call(context.Background(), nil)
	fmt.Println(err)
	// The documents of the transports tell of the timeout too.
	for _, kind := range build.Doc().Errors {
		fmt.Println("declared:", kind)
	}
	// Output:
	// deadline_exceeded: deadline exceeded: context deadline exceeded
	// declared: deadline_exceeded
}
