package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tyr-go/tyr"
)

type SetExpiryReq struct {
	Code  string    `json:"code"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Validate holds rules that struct tags can't express, such as the order of
// two fields. It writes its errors for the client.
func (r SetExpiryReq) Validate() error {
	if !r.End.After(r.Start) {
		return errors.New("end must be after start")
	}
	return nil
}

func ExampleValidator() {
	api := tyr.New()
	setExpiry := api.Handle("links.set_expiry", func(ctx context.Context, req SetExpiryReq) (string, error) {
		return "expires " + req.End.Format(time.DateOnly), nil
	})

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, end := range []time.Time{start.AddDate(0, 1, 0), start} {
		res, err := setExpiry.Call(context.Background(), func(dst any) error {
			*dst.(*SetExpiryReq) = SetExpiryReq{Code: "go", Start: start, End: end}
			return nil
		})
		if e, ok := errors.AsType[*tyr.Error](err); ok {
			fmt.Println(e.Kind, e.Message) // what the client gets
			continue
		}
		fmt.Println(res)
	}
	// Output:
	// expires 2026-02-01
	// invalid_argument end must be after start
}
