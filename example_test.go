package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
	"github.com/tyr-go/tyr/rest"
)

func ExampleNewLogHandler() {
	// An application would pass the logger to slog.SetDefault. This one also
	// drops the time from records to keep the output stable.
	noTime := &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}
	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stdout, noTime)))

	get := tyr.New().Handle("links.get", func(ctx context.Context, req struct{}) (string, error) {
		logger.InfoContext(ctx, "link found", "code", "go")
		logger.WithGroup("db").InfoContext(ctx, "query", "rows", 1)
		return "https://go.dev", nil
	})
	_, _ = get.Call(tyr.WithRequestID(context.Background(), "0192f5e2"), nil)
	// Output:
	// {"level":"INFO","msg":"link found","request_id":"0192f5e2","operation":"links.get","code":"go"}
	// {"level":"INFO","msg":"query","request_id":"0192f5e2","operation":"links.get","db":{"rows":1}}
}

func ExampleDefine() {
	// A contract, which the server and its clients share; it would be a
	// package-level variable of a package of its own.
	type GetLinkReq struct {
		Code string `json:"code" path:"code" validate:"required"`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	getLink := tyr.Define[GetLinkReq, *Link]("links.get", rest.Route("GET /links/{code}"))

	// The server: Implement doesn't compile unless the handler fits.
	api := tyr.New()
	api.Implement(getLink, func(ctx context.Context, req GetLinkReq) (*Link, error) {
		return &Link{Code: req.Code, URL: "https://go.dev"}, nil
	})
	mux := http.NewServeMux()
	rest.Mount(mux, api) // at the route of the contract

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/links/go", nil))
	fmt.Println(getLink.Name(), rec.Code, rec.Body)
	// Output:
	// links.get 200 {"code":"go","url":"https://go.dev"}
}

func ExampleOperation_Call() {
	type GetLinkReq struct {
		Code string `json:"code"`
	}
	errNotFound := errors.New("store: not found") // a domain error

	api := tyr.New()
	api.MapError(func(err error) error {
		if errors.Is(err, errNotFound) {
			return tyr.NotFound("link not found").WithCause(err)
		}
		return err
	})
	get := api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		if req.Code != "go" {
			return "", errNotFound
		}
		return "https://go.dev", nil
	})

	// Transports pass a decode for their wire format; this one fills in the
	// request directly, as a test would.
	for _, code := range []string{"go", "rust"} {
		url, err := get.Call(context.Background(), func(dst any) error {
			dst.(*GetLinkReq).Code = code
			return nil
		})
		fmt.Println(url, err)
	}
	// Output:
	// https://go.dev <nil>
	// <nil> not_found: link not found: store: not found
}

func ExampleAPI_MapError() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code"`
	}
	errNotFound := errors.New("store: not found") // of a package that doesn't import tyr

	// The internal error goes to the log with its cause; this API drops it.
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
	api.MapError(func(err error) error {
		if errors.Is(err, errNotFound) {
			return tyr.NotFound("link not found").WithCause(err)
		}
		return err // unmapped: an internal error for the client
	})
	api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		if req.Code == "db" {
			return "", errors.New("db: connection refused")
		}
		return "", errNotFound
	}, rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	for _, target := range []string{"/links/rust", "/links/db"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 404 {"type":"/problems/not_found","title":"Not Found","status":404,"detail":"link not found","kind":"not_found"}
	// 500 {"type":"/problems/internal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}
}

func ExampleRequestInfo() {
	type GetLinkReq struct {
		Code string `json:"code" path:"code"`
	}
	api := tyr.New()
	api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		return "https://go.dev", nil
	}, rest.Route("GET /links/{code}"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	// metrics is middleware above the transports: it puts a RequestInfo in
	// the context of the request and reads it after next. A real one would
	// record the duration by route and operation.
	metrics := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, info := tyr.WithRequestInfo(r.Context())
			next.ServeHTTP(w, r.WithContext(ctx))
			name := "-"
			if op, ok := info.Operation(); ok {
				name = op.Name()
			}
			fmt.Println(info.Route(), name)
		})
	}
	handler := metrics(mux)

	call := `{"jsonrpc":"2.0","method":"links.get","params":{"code":"go"},"id":1}`
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/links/go", nil),
		httptest.NewRequest("POST", "/rpc", strings.NewReader(call)),
		httptest.NewRequest("POST", "/rpc", strings.NewReader("["+call+","+call+"]")),
	} {
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	// Output:
	// GET /links/{code} links.get
	// POST /rpc links.get
	// POST /rpc -
}

func ExampleAPI_Use() {
	type GetLinkReq struct {
		Code string `json:"code"`
	}

	// logCalls reports the outcome of every call; a real one would log it
	// or record a metric.
	logCalls := func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
		res, err := next(ctx, req)
		kind := "ok"
		if e, ok := errors.AsType[*tyr.Error](err); ok {
			kind = e.Kind.String()
		}
		fmt.Println(op.Name(), kind)
		return res, err
	}

	api := tyr.New()
	api.Use(logCalls) // the first interceptor is the outermost
	get := api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
		if req.Code != "go" {
			return "", tyr.NotFound("link not found")
		}
		return "https://go.dev", nil
	})
	api.Seal() // as a transport does when it mounts the API

	for _, code := range []string{"go", "rust"} {
		_, _ = get.Call(context.Background(), func(dst any) error {
			dst.(*GetLinkReq).Code = code
			return nil
		})
	}
	// Output:
	// links.get ok
	// links.get not_found
}

func ExampleError_WithCause() {
	errStore := errors.New("store: not found") // a domain error

	// E.g. in an error mapper: the client gets the kind and the message,
	// the logs get the cause too.
	err := tyr.NotFound("link not found").WithCause(errStore)

	fmt.Println(err.Kind, err.Message)
	fmt.Println(err)
	fmt.Println(errors.Is(err, errStore))
	// Output:
	// not_found link not found
	// not_found: link not found: store: not found
	// true
}

func ExampleError_Is() {
	// A shared error, e.g. a package-level variable.
	errExpired := tyr.FailedPrecondition("link expired")

	// Copies made by With* still match it, also when wrapped...
	err := fmt.Errorf("links.get: %w", errExpired.WithCause(errors.New("store: expired")))
	fmt.Println(errors.Is(err, errExpired))

	// ...but distinct errors don't, even with the same kind and message.
	fmt.Println(errors.Is(tyr.FailedPrecondition("link expired"), errExpired))
	// Output:
	// true
	// false
}
