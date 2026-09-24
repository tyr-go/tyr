package tyr_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

func ExampleContract_Example() {
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

// shortLink names its schemas, as a type of a contract may.
type shortLink struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

func (shortLink) SchemaName() string { return "ShortLink" }

type importLinksReq struct {
	Links []shortLink `json:"links"`
}

type getShortLinkReq struct {
	Code string `json:"code" path:"code"`
}

func ExampleSchemaNamer() {
	api := tyr.New()
	api.Handle("links.get", func(ctx context.Context, req getShortLinkReq) (shortLink, error) {
		return shortLink{Code: req.Code, URL: "https://go.dev"}, nil
	}, rest.Route("GET /links/{code}"))
	api.Handle("links.import", func(ctx context.Context, req importLinksReq) (struct{}, error) {
		return struct{}{}, nil
	}, rest.Route("POST /links/import"))

	// The schemas of shortLink: in results, and in the requests of
	// links.import, whose body is part of the operation.
	rec := httptest.NewRecorder()
	rest.Mount(http.NewServeMux(), api).OpenAPI(tyr.Info{Title: "links", Version: "1.0.0"}).
		ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc struct {
		Components struct {
			Schemas map[string]jsontext.Value `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		fmt.Println(err)
	}
	fmt.Println(slices.Sorted(maps.Keys(doc.Components.Schemas)))
	// Output:
	// [Problem ShortLink ShortLinkInput Violation]
}
