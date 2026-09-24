package tyr_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/tyr-go/tyr"
)

func ExampleOp_Example() {
	type GetLinkReq struct {
		Code string `json:"code" validate:"required,min=2"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}

	// The contract documents the operation for the documents of its API.
	getLink := tyr.Define[GetLinkReq, Link]("links.get",
		tyr.Summary("Get a link"),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindNotFound),
	).Example("go", GetLinkReq{Code: "go"}, Link{Code: "go", URL: "https://go.dev"})

	api := tyr.New()
	op := api.Implement(getLink, func(ctx context.Context, req GetLinkReq) (Link, error) {
		return Link{Code: req.Code, URL: "https://go.dev"}, nil
	})

	doc := op.Doc()
	fmt.Println(doc.Summary)
	fmt.Println("tags:", strings.Join(doc.Tags, ", "))
	for _, k := range doc.Errors {
		fmt.Println("may fail with", k)
	}
	for _, ex := range doc.Examples {
		fmt.Printf("example %s: %+v -> %+v\n", ex.Name, ex.Req, ex.Res)
	}
	// Output:
	// Get a link
	// tags: links
	// may fail with not_found
	// example go: {Code:go} -> {Code:go URL:https://go.dev}
}
