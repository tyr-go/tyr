package playground_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/validate/playground"
)

type createReq struct {
	URL  string   `json:"url" validate:"required,http_url"`
	Code string   `json:"code" validate:"omitempty,min=4,code"`
	Tags []string `json:"tags" validate:"max=3,dive,min=2"`
}

func ExampleNew() {
	// All of go-playground, and a rule of our own with its detail.
	api := tyr.New(tyr.WithValidator(playground.New(playground.Rule("code", validCode, "only a-z, 0-9 and '-'"))))
	create := api.Handle("links.create", func(ctx context.Context, req createReq) (struct{}, error) {
		return struct{}{}, nil
	})

	_, err := create.Call(context.Background(), func(dst any) error {
		*dst.(*createReq) = createReq{URL: "https://go.dev", Code: "Go_Dev", Tags: []string{"go", "x"}}
		return nil
	})
	e, _ := errors.AsType[*tyr.Error](err)
	for _, v := range e.Details.(tyr.Violations) {
		fmt.Println(v.Pointer, v.Detail)
	}
	// Output:
	// /code only a-z, 0-9 and '-'
	// /tags/1 must be at least 2 characters
}
