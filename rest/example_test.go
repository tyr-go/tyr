package rest_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

func ExampleMount() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code"`
	}

	api := tyr.New()
	api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		if req.Code != "go" {
			return "", tyr.NotFound("link %q not found", req.Code)
		}
		return "https://go.dev", nil
	}, rest.Route("GET /links/{code}"))

	mux := http.NewServeMux()
	rest.Mount(mux, api)

	for _, target := range []string{"/links/go", "/links/rust"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 200 "https://go.dev"
	// 404 {"type":"about:blank","title":"Not Found","status":404,"detail":"link \"rust\" not found","kind":"not_found"}
}

func ExampleStatus() {
	type CreateReq struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	type Created struct {
		Code     string `json:"code"`
		URL      string `json:"url"`
		Location string `json:"-" header:"Location"` // a header, not a member
	}
	type FollowReq struct {
		Code string `json:"code" path:"code"`
	}
	type FollowRes struct {
		URL string `json:"url" header:"Location"` // JSON-RPC gets it as a member
	}

	api := tyr.New()
	api.Handle("links.create", func(ctx context.Context, req CreateReq) (Created, error) {
		return Created{Code: req.Code, URL: req.URL, Location: "/links/" + req.Code}, nil
	}, rest.Route("POST /links"), rest.Status(http.StatusCreated))
	api.Handle("links.follow", func(ctx context.Context, req FollowReq) (FollowRes, error) {
		return FollowRes{URL: "https://go.dev"}, nil
	}, rest.Route("GET /{code}"), rest.Status(http.StatusFound))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	create := httptest.NewRequest("POST", "/links", strings.NewReader(`{"url":"https://go.dev","code":"golang"}`))
	create.Header.Set("Content-Type", "application/json")
	for _, req := range []*http.Request{create, httptest.NewRequest("GET", "/golang", nil)} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		fmt.Println(rec.Code, "Location:", rec.Header().Get("Location"))
		if rec.Body.Len() > 0 {
			fmt.Println(rec.Body)
		}
	}
	// Output:
	// 201 Location: /links/golang
	// {"code":"golang","url":"https://go.dev"}
	// 302 Location: https://go.dev
}
