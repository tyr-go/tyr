package tyr_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/rest"
)

// The Quickstart of README.md, with a Get that knows one link. TestREADME
// checks that README.md shows these responses.
func Example_quickstart() {
	type GetReq struct {
		Code string `json:"code" path:"code" validate:"required,min=4"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	Get := func(ctx context.Context, req GetReq) (*Link, error) {
		if req.Code != "golang" {
			return nil, tyr.NotFound("link %q not found", req.Code)
		}
		return &Link{Code: "golang", URL: "https://go.dev"}, nil
	}

	api := tyr.New()
	api.Handle("links.get", Get, rest.Route("GET /links/{code}"))

	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	rpc := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"jsonrpc":"2.0","method":"links.get","params":{"code":"golang"},"id":1}`))
	rpc.Header.Set("Content-Type", "application/json")
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/links/golang", nil),
		httptest.NewRequest("GET", "/links/x", nil),
		rpc,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 200 {"code":"golang","url":"https://go.dev"}
	// 400 {"type":"about:blank","title":"Bad Request","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/code","detail":"must be at least 4 characters"}]}
	// 200 {"jsonrpc":"2.0","result":{"code":"golang","url":"https://go.dev"},"id":1}
}
