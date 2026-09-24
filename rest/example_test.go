package rest_test

import (
	"context"
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
	// 404 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindNotFound","title":"Not Found","status":404,"detail":"link \"rust\" not found","kind":"not_found"}
}

func ExampleProblemTypes() {
	api := tyr.New()
	api.Handle("links.create", func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, tyr.AlreadyExists("code is taken")
	}, rest.Route("POST /links"))

	// The kinds get URIs of the service's own. Handlers outside operations
	// pass the same options to WriteError.
	opts := []rest.MountOption{rest.ProblemTypes("https://shortlink.example/problems/")}
	mux := http.NewServeMux()
	rest.Mount(mux, api, opts...)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/links", nil))
	fmt.Println(rec.Code, rec.Body)
	// Output:
	// 409 {"type":"https://shortlink.example/problems/already_exists","title":"Already Exists","status":409,"detail":"code is taken","kind":"already_exists"}
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

func ExampleOpenAPI() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code" validate:"required" doc:"The code of the link."`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}

	api := tyr.New()
	api.Implement(tyr.Define[GetLinkReq, Link]("links.get", rest.Route("GET /links/{code}"),
		tyr.Summary("Get a link"), tyr.Errors(tyr.KindNotFound),
	), func(ctx context.Context, req GetLinkReq) (Link, error) {
		return Link{Code: req.Code, URL: "https://go.dev"}, nil
	})

	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("GET /openapi.json", rest.OpenAPI(api, tyr.Info{Title: "links", Version: "1.0.0"}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string         `json:"operationId"`
			Summary     string         `json:"summary"`
			Responses   map[string]any `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		fmt.Println(err)
	}
	for path, item := range doc.Paths {
		for method, op := range item {
			fmt.Println(method, path, op.OperationID, op.Summary, slices.Sorted(maps.Keys(op.Responses)))
		}
	}
	// Output:
	// get /links/{code} links.get Get a link [200 400 404 default]
}
