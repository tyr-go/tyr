package jsonrpc_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/inprocess"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/rest"
)

func ExampleClient() {
	// The contract, which the server and its clients share.
	type GetLinkReq struct {
		Code string `json:"code" validate:"required"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	getLink := tyr.Define[GetLinkReq, *Link]("links.get")

	api := tyr.New()
	api.Implement(getLink, func(ctx context.Context, req GetLinkReq) (*Link, error) {
		if req.Code != "go" {
			return nil, tyr.NotFound("link %q not found", req.Code)
		}
		return &Link{Code: "go", URL: "https://go.dev"}, nil
	})

	// In memory here; another program would pass an http.Client with a
	// timeout and the URL of the service.
	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(api)))
	for _, code := range []string{"go", "gone", ""} {
		link, err := c.Call(context.Background(), getLink, GetLinkReq{Code: code})
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok && se.Details != nil {
			fmt.Println(se.Kind, se.Message, se.Details) // what the server answered
		} else if ok {
			fmt.Println(se.Kind, se.Message)
		} else if err != nil {
			fmt.Println(err) // no answer that fits the call
		} else {
			fmt.Println(link.URL)
		}
	}
	// Output:
	// https://go.dev
	// not_found link "gone" not found
	// invalid_argument validation failed [{"pointer":"/code","detail":"is required"}]
}

func ExampleServerError() {
	type GetLinkReq struct {
		Code string `json:"code" validate:"required"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	type ShowReq struct {
		Page string `json:"page" path:"page"`
	}

	// Another service, which knows one link.
	getLink := tyr.Define[GetLinkReq, *Link]("links.get")
	links := tyr.New()
	links.Implement(getLink, func(ctx context.Context, req GetLinkReq) (*Link, error) {
		if req.Code != "go" {
			return nil, tyr.NotFound("link %q not found", req.Code)
		}
		return &Link{Code: "go", URL: "https://go.dev"}, nil
	})
	lc := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(links)))

	// A service that calls it translates what means something to its own
	// clients, and the rest is internal.
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
	api.Handle("pages.show", func(ctx context.Context, req ShowReq) (string, error) {
		link, err := lc.Call(ctx, getLink, GetLinkReq{Code: req.Page})
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok && se.Kind == tyr.KindNotFound {
			return "", tyr.NotFound("no page for %q", req.Page).WithCause(err)
		}
		if err != nil {
			return "", err // internal: the log gets what links answered
		}
		return "the page of " + link.URL, nil
	}, rest.Route("GET /pages/{page...}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	for _, target := range []string{"/pages/go", "/pages/gone", "/pages/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 200 "the page of https://go.dev"
	// 404 {"type":"/problems/not_found","title":"Not Found","status":404,"detail":"no page for \"gone\"","kind":"not_found"}
	// 500 {"type":"/problems/internal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}
}

func ExampleHandler() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code" validate:"required"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}

	api := tyr.New()
	api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (*Link, error) {
		if req.Code != "go" {
			return nil, tyr.NotFound("link %q not found", req.Code)
		}
		return &Link{Code: "go", URL: "https://go.dev"}, nil
	}, rest.Route("GET /links/{code}"))

	// The same operation over REST and over JSON-RPC.
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	for _, body := range []string{
		`{"jsonrpc": "2.0", "method": "links.get", "params": {"code": "go"}, "id": 1}`,
		`{"jsonrpc": "2.0", "method": "links.get", "params": {"code": "gone"}, "id": 2}`,
		`[{"jsonrpc": "2.0", "method": "links.get", "params": {}, "id": 3}, {"jsonrpc": "2.0", "method": "links.list", "id": 4}]`,
	} {
		req := httptest.NewRequest("POST", "/rpc", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 200 {"jsonrpc":"2.0","result":{"code":"go","url":"https://go.dev"},"id":1}
	// 200 {"jsonrpc":"2.0","error":{"code":404,"message":"link \"gone\" not found","data":{"kind":"not_found"}},"id":2}
	// 200 [{"jsonrpc":"2.0","error":{"code":-32602,"message":"validation failed","data":{"kind":"invalid_argument","details":[{"pointer":"/code","detail":"is required"}]}},"id":3},{"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found"},"id":4}]
}

// authorizationKey is where the service of ExampleHeaders keeps the
// Authorization of a request.
type authorizationKey struct{}

func ExampleHeaders() {
	// A service that answers who called it, by the Authorization of the
	// request, which its middleware puts into the context.
	api := tyr.New()
	whoami := tyr.Define[struct{}, string]("whoami")
	api.Implement(whoami, func(ctx context.Context, _ struct{}) (string, error) {
		auth, _ := ctx.Value(authorizationKey{}).(string)
		return auth, nil
	})
	rpc := jsonrpc.Handler(api)
	service := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), authorizationKey{}, r.Header.Get("Authorization"))
		rpc.ServeHTTP(w, r.WithContext(ctx))
	})

	// The client sends the token of its service with each call.
	c := jsonrpc.NewClient("http://whoami/rpc", inprocess.Client(service),
		jsonrpc.Headers(func(ctx context.Context, h http.Header) error {
			h.Set("Authorization", "Bearer service-token")
			return nil
		}))
	got, err := c.Call(context.Background(), whoami, struct{}{})
	fmt.Println(got, err)
	// Output:
	// Bearer service-token <nil>
}
