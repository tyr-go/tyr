package inprocess_test

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/inprocess"
	"github.com/tyr-go/tyr/rest"
)

func ExampleClient() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code"`
	}

	api := tyr.New()
	api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		return "https://go.dev/" + req.Code, nil
	}, rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	// The service in memory, as over a network: any HTTP request, such as
	// one of REST, and the calls of the typed client of jsonrpc.
	hc := inprocess.Client(mux)
	resp, err := hc.Get("http://links/links/go")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(resp.StatusCode, string(body))
	// Output:
	// 200 "https://go.dev/go"
}
