# ᛏ tyr

[![Go Reference](https://pkg.go.dev/badge/github.com/tyr-go/tyr.svg)](https://pkg.go.dev/github.com/tyr-go/tyr)
[![CI](https://github.com/tyr-go/tyr/actions/workflows/ci.yml/badge.svg)](https://github.com/tyr-go/tyr/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/tag/tyr-go/tyr?sort=semver&label=release)](https://github.com/tyr-go/tyr/tags)
[![Go](https://img.shields.io/github/go-mod/go-version/tyr-go/tyr)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Typed operations for Go: write a handler once as a plain function, serve it over REST and JSON-RPC, and call it with a typed client.

## Installation

```sh
go mod init example.com/links   # if you have no module yet
go get github.com/tyr-go/tyr
```

tyr needs Go 1.27, for generic methods. With `GOTOOLCHAIN=auto`, the default, the go command downloads that toolchain itself. The module depends on the standard library only.

## Quickstart

```go
type GetReq struct {
	Code string `json:"code" path:"code" validate:"required,min=4"`
}

func Get(ctx context.Context, req GetReq) (*Link, error) { /* ... */ }

func main() {
	api := tyr.New()
	api.Handle("links.get", Get, rest.Route("GET /links/{code}"))

	mux := http.NewServeMux()
	rest.Mount(mux, api)
	mux.Handle("POST /rpc", jsonrpc.Handler(api))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

Here `Get` knows one link, `golang`. Each `# =>` is the status and the body of the response:

<!-- Output: tyr.Example_quickstart -->
```sh
curl localhost:8080/links/golang
# => 200 {"code":"golang","url":"https://go.dev"}

curl localhost:8080/links/x
# => 400 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindInvalidArgument","title":"Invalid Argument","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/code","detail":"must be at least 4 characters"}]}

curl localhost:8080/rpc -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"links.get","params":{"code":"golang"},"id":1}'
# => 200 {"jsonrpc":"2.0","result":{"code":"golang","url":"https://go.dev"},"id":1}
```

tyr decodes the request, from the JSON body and then the fields tagged `path`, `query` or `header`, validates it, calls the handler and encodes the result or the error. The operation has a name, `links.get`: REST serves it at its route, and JSON-RPC by that name.

## Features

- Handlers are plain functions, [`func(ctx, Req) (Res, error)`](https://pkg.go.dev/github.com/tyr-go/tyr#Handler), with no HTTP types
- One operation over [REST](https://pkg.go.dev/github.com/tyr-go/tyr/rest) and [JSON-RPC 2.0](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc), by its name
- [Contracts](https://pkg.go.dev/github.com/tyr-go/tyr#Define) that the server and a typed [JSON-RPC client](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#Client) share, checked by the compiler, without codegen, and an [in-process client](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#InProcess) for tests
- [OpenAPI 3.1](https://pkg.go.dev/github.com/tyr-go/tyr/rest#OpenAPI) and [OpenRPC](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#Discover) documents made of the same types and contracts: JSON Schemas of what the server reads and writes, with the constraints of the `validate` tags, and the summaries, errors and examples of the contracts
- [Binding](https://pkg.go.dev/github.com/tyr-go/tyr/rest#hdr-Requests) from the JSON body, the path, the query and headers
- [Validation](https://pkg.go.dev/github.com/tyr-go/tyr#hdr-Validation) by tags in the syntax of go-playground/validator and by a `Validate` method
- [Errors of kinds](https://pkg.go.dev/github.com/tyr-go/tyr#Kind): RFC 9457 problems over REST, error codes over JSON-RPC
- [Interceptors](https://pkg.go.dev/github.com/tyr-go/tyr#Interceptor) with typed [metadata](https://pkg.go.dev/github.com/tyr-go/tyr#MetaKey) of operations, for authorization, metrics and tracing
- [Middleware](https://pkg.go.dev/github.com/tyr-go/tyr/middleware): request IDs, access logs, recovery from panics
- [Logs](https://pkg.go.dev/github.com/tyr-go/tyr#NewLogHandler) with the request ID and the operation, through `log/slog`
- The [route and the operation](https://pkg.go.dev/github.com/tyr-go/tyr#RequestInfo) of a request, for access logs and metrics
- No dependencies but the standard library; routing by `http.ServeMux`

## Philosophy

Business logic is a plain function: a context and a request in, a result or an error out. It knows nothing of HTTP, so it stays the same over any transport, and a test calls it as it is. An operation has a name, `links.get`. Transports bind it, REST to a route and JSON-RPC to a method, and only decode requests and encode results and errors; the core owns the rest of a call: interceptors, validation and the mapping of errors.

tyr is a thin layer over `net/http`: routing is `http.ServeMux`, middleware is `func(http.Handler) http.Handler`, and logs go through `log/slog`. Everything beyond the Quickstart is optional, and nothing registers itself behind your back: no global registries, no `init`.

Whether you come from Go or from NestJS and Hono, each piece should look familiar:

| tyr | NestJS / Hono | What a Gopher already knows |
|---|---|---|
| `func(ctx, Req) (Res, error)` | controller method returning a value | gRPC service method, same shape |
| `validate:"required,min=4"` | DTO + class-validator | go-playground/validator, Gin's `binding` |
| `Interceptor` + `MetaKey` | Interceptor, Guard + decorator | gRPC interceptor, context key |
| `MapError` | ExceptionFilter, `app.onError` | Echo's `HTTPErrorHandler` |
| `Define` + client | tRPC, Hono RPC, Eden | gRPC proto contract, without codegen |

Týr, the Norse god of law and oaths, put his hand in Fenrir's jaws as the pledge of a fair deal; tyr keeps contracts the compiler checks. ᛏ is his rune.

## Limitations

- Go 1.27 or later: the API uses generic methods.
- The API may change until v1.
- JSON only, with `encoding/json/v2`. Forms, multipart and files go to plain handlers on the same mux.
- Request and response only. Streaming, server-sent events and WebSockets go to plain handlers too, and consumers of event streams are out of scope.
- JSON-RPC takes params by name only.
- Validation implements a subset of the tags of go-playground/validator, with the semantics of v10.30.5: `required`, `omitempty`, `min`, `max`, `len`, `gt`, `gte`, `lt`, `lte`, `oneof`, `email`, `url`, `http_url` and `uuid`, without `dive` and `|`. An unknown rule panics at startup. Rules between fields go in a `Validate` method; an adapter for all of go-playground is planned.
- The typed client speaks JSON-RPC and sends one call per request; a REST client is planned.
- The schemas of requests say what the `validate` tags demand as far as JSON Schema can: `email` and `url` become formats, which the rules of go-playground don't match exactly, and a `Validate` method doesn't show. The schemas of results have no constraints: the server doesn't check what it writes.
- Authorization belongs in interceptors, not in the middleware of a route: over JSON-RPC, an operation has no route of its own.

## Examples

### [Return an error](https://pkg.go.dev/github.com/tyr-go/tyr/rest#example-Mount)

A handler returns a `*tyr.Error` of a kind, and REST sends it as RFC 9457 `application/problem+json` with the type, the title and the status of the kind. By default, the type is the documentation of the kind; [`rest.ProblemTypes`](https://pkg.go.dev/github.com/tyr-go/tyr/rest#ProblemTypes) gives the kinds URIs of the service's own:

<!-- Output: rest.ExampleMount -->
```go
api.Handle("links.get", func(ctx context.Context, req GetLinkReq) (string, error) {
	if req.Code != "go" {
		return "", tyr.NotFound("link %q not found", req.Code)
	}
	return "https://go.dev", nil
}, rest.Route("GET /links/{code}"))

// GET /links/go
// => 200 "https://go.dev"
// GET /links/rust
// => 404 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindNotFound","title":"Not Found","status":404,"detail":"link \"rust\" not found","kind":"not_found"}
```

### [Translate the errors of other packages](https://pkg.go.dev/github.com/tyr-go/tyr#example-API.MapError)

A store needn't import tyr: `MapError` translates its errors. The client learns no more of an error left untranslated than `internal error`, and the log gets its message and cause:

<!-- Output: tyr.ExampleAPI_MapError -->
```go
api.MapError(func(err error) error {
	if errors.Is(err, errNotFound) {
		return tyr.NotFound("link not found").WithCause(err)
	}
	return err // unmapped: an internal error for the client
})

// GET /links/rust, whose handler returns errNotFound
// => 404 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindNotFound","title":"Not Found","status":404,"detail":"link not found","kind":"not_found"}
// GET /links/db, whose handler returns errors.New("db: connection refused")
// => 500 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindInternal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}
```

### [Validate a request](https://pkg.go.dev/github.com/tyr-go/tyr#example-package-Validation)

Tags check each field; a `Validate` method, the rules that tags can't express. A failed check is a 400 with a JSON pointer per field:

<!-- Output: tyr.Example_validation -->
```go
type CreateLinkReq struct {
	URL  string `json:"url" validate:"required,http_url"`
	Code string `json:"code" validate:"omitempty,min=4,max=16"`
}

func (r CreateLinkReq) Validate() error {
	var v tyr.Violations
	if r.Code != "" && !codeRe.MatchString(r.Code) {
		v.Add("code", "only a-z, 0-9 and '-'")
	}
	return v.Err()
}

// POST /links {"url":"ftp://go.dev","code":"go"}
// => 400 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindInvalidArgument","title":"Invalid Argument","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/url","detail":"must be an http or https URL"},{"pointer":"/code","detail":"must be at least 4 characters"}]}
// POST /links {"url":"https://go.dev","code":"Go!!"}
// => 400 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindInvalidArgument","title":"Invalid Argument","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/code","detail":"only a-z, 0-9 and '-'"}]}
```

Validation runs after the interceptors, so a client that isn't allowed to call an operation learns that, not what's wrong with its request.

### [Authorize with an interceptor](https://pkg.go.dev/github.com/tyr-go/tyr#example-Interceptor)

Interceptors run around every operation, over every transport, so authorization belongs there. A `MetaKey` attaches typed metadata to operations, here the roles they require, and a group gives it to several. The role of the caller comes from HTTP middleware that reads the token and puts the role in the context, under a key of `ctxkey`. A caller without a role isn't authenticated, and one with another role isn't allowed:

<!-- Output: tyr.ExampleInterceptor -->
```go
var roles = tyr.NewMetaKey[[]string]("authz.roles")

func authorize(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
	want, ok := roles.Get(op)
	if !ok {
		return next(ctx, req)
	}
	role, ok := userRole.Get(ctx)
	if !ok {
		return nil, tyr.Unauthenticated("log in first")
	}
	if !slices.Contains(want, role) {
		return nil, tyr.PermissionDenied("requires one of %v", want)
	}
	return next(ctx, req)
}

api.Use(authorize)
admin := api.Group(roles.Option([]string{"admin"}))
admin.Handle("links.purge", Purge, rest.Route("POST /links/purge"))

// RFC 9110 requires a challenge on every 401.
rest.Mount(mux, api, rest.Challenge(`Bearer realm="links"`))

// POST /links/purge without a token
// => 401 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindUnauthenticated","title":"Unauthenticated","status":401,"detail":"log in first","kind":"unauthenticated"}
// => WWW-Authenticate: Bearer realm="links"
// POST /links/purge with a user's token
// => 403 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindPermissionDenied","title":"Permission Denied","status":403,"detail":"requires one of [admin]","kind":"permission_denied"}
// POST /links/purge with the admin's token
// => 200 "purged"
```

### [Set headers and redirect](https://pkg.go.dev/github.com/tyr-go/tyr/rest#example-Status)

Fields of a result tagged `header` set headers of the response, and a redirect is a status and a `Location`:

<!-- Output: rest.ExampleStatus -->
```go
type Created struct {
	Code     string `json:"code"`
	URL      string `json:"url"`
	Location string `json:"-" header:"Location"` // a header, not a member
}

type FollowRes struct {
	URL string `json:"url" header:"Location"` // JSON-RPC gets it as a member
}

api.Handle("links.create", Create, rest.Route("POST /links"), rest.Status(http.StatusCreated))
api.Handle("links.follow", Follow, rest.Route("GET /{code}"), rest.Status(http.StatusFound))

// POST /links {"url":"https://go.dev","code":"golang"}
// => 201 Location: /links/golang
// => {"code":"golang","url":"https://go.dev"}
// GET /golang
// => 302 Location: https://go.dev
```

### [Serve the same operation over JSON-RPC](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#example-Handler)

The method is the name of the operation, and params are its request, by name. The code of an error is the HTTP status that REST sends for its kind, but two kinds get codes of JSON-RPC itself: -32602 for `invalid_argument`, with the violations that REST sends, and -32603 for `internal`, which tells the client no more than `internal error`. A batch runs up to 8 calls at a time, and a notification, a call without an id, gets no response:

<!-- Output: jsonrpc.ExampleHandler -->
```go
mux.Handle("POST /rpc", jsonrpc.Handler(api))

// POST /rpc {"jsonrpc": "2.0", "method": "links.get", "params": {"code": "go"}, "id": 1}
// => 200 {"jsonrpc":"2.0","result":{"code":"go","url":"https://go.dev"},"id":1}
// POST /rpc {"jsonrpc": "2.0", "method": "links.get", "params": {"code": "gone"}, "id": 2}
// => 200 {"jsonrpc":"2.0","error":{"code":404,"message":"link \"gone\" not found","data":{"kind":"not_found"}},"id":2}
```

### [Call an operation with a typed client](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#example-Client)

A contract, made by `tyr.Define`, is a value that the server and its clients share. `Implement` doesn't compile unless the handler fits it, and `Call` takes its request type and returns its result type. An error of the server comes back as a `*tyr.Error` of its kind. Here `InProcess` serves the calls in memory, as in a test; another program passes an `http.Client` with a timeout and the URL of the service:

<!-- Output: jsonrpc.ExampleClient -->
```go
getLink := tyr.Define[GetLinkReq, *Link]("links.get") // shared with the clients

api.Implement(getLink, func(ctx context.Context, req GetLinkReq) (*Link, error) {
	if req.Code != "go" {
		return nil, tyr.NotFound("link %q not found", req.Code)
	}
	return &Link{Code: "go", URL: "https://go.dev"}, nil
})

c := jsonrpc.NewClient("http://links/rpc", jsonrpc.InProcess(jsonrpc.Handler(api)))
link, err := c.Call(ctx, getLink, GetLinkReq{Code: code})
if e, ok := errors.AsType[*tyr.Error](err); ok {
	fmt.Println(e.Kind, e.Message, e.Details)
} else if err != nil {
	fmt.Println(err) // no answer that fits the call
} else {
	fmt.Println(link.URL)
}

// code "go"
// => https://go.dev
// code "gone"
// => not_found link "gone" not found <nil>
// code "", which GetLinkReq requires
// => invalid_argument validation failed [{"pointer":"/code","detail":"is required"}]
```

Any other error of `Call`, such as a failed connection or a 503 of a load balancer, isn't a `*tyr.Error`: the documentation of [`Client`](https://pkg.go.dev/github.com/tyr-go/tyr/jsonrpc#Client) shows how to report those as `unavailable`.

### [Document an API](https://pkg.go.dev/github.com/tyr-go/tyr/rest#example-OpenAPI)

The contract documents its operation too, so the code and the documents share one source: `Summary`, `Description`, `Tags`, `Errors` and examples by `Contract.Example`, whose types the compiler checks; `doc` tags describe fields. REST serves an OpenAPI 3.1 document of the operations it serves, with the same options as `Mount`:

<!-- Output: rest.ExampleOpenAPI -->
```go
type GetLinkReq struct {
	Code string `json:"code" path:"code" validate:"required" doc:"The code of the link."`
}

getLink := tyr.Define[GetLinkReq, Link]("links.get", rest.Route("GET /links/{code}"),
	tyr.Summary("Get a link"), tyr.Errors(tyr.KindNotFound))
api.Implement(getLink, get)

mux.Handle("GET /openapi.json", rest.OpenAPI(api, tyr.Info{Title: "links", Version: "1.0.0"}))

// GET /openapi.json: its method, path, operationId, summary and responses
// => get /links/{code} links.get Get a link [200 400 404 default]
```

Every operation may fail with `invalid_argument`, so 400 is always there; `default` stands for the rest. JSON-RPC answers `rpc.discover` with the OpenRPC document of the same operations:

<!-- Output: jsonrpc.ExampleDiscover -->
```go
mux.Handle("POST /rpc", jsonrpc.Handler(api, jsonrpc.Discover(tyr.Info{Title: "links", Version: "1.0.0"})))

// POST /rpc {"jsonrpc": "2.0", "method": "rpc.discover", "id": 1}
// => OpenRPC 1.4.1
// => method links.get: Get a link
// => param code, required: true
// => error -32602: invalid argument
// => error 404: not found
```

The schemas say what the server reads and writes, as `encoding/json/v2` does. A member of a request is required if the server rejects the request without it, and a type whose schemas of requests and results differ gets two, such as `Link` and `LinkInput`.

### [Log with the request ID and the operation](https://pkg.go.dev/github.com/tyr-go/tyr#example-NewLogHandler)

There is no logger in the context: code logs with the context, and `tyr.NewLogHandler` adds the request ID and the operation of the context to every record, at its top level, even in a group:

<!-- Output: tyr.ExampleNewLogHandler -->
```go
logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil)))

logger.InfoContext(ctx, "link found", "code", "go")
// => {"level":"INFO","msg":"link found","request_id":"0192f5e2","operation":"links.get","code":"go"}
logger.WithGroup("db").InfoContext(ctx, "query", "rows", 1)
// => {"level":"INFO","msg":"query","request_id":"0192f5e2","operation":"links.get","db":{"rows":1}}
```

The records above leave out the time.

### [Chain middleware](https://pkg.go.dev/github.com/tyr-go/tyr/middleware#example-Chain)

<!-- Output: middleware.ExampleChain -->
```go
handler := middleware.Chain(mux, // first = outermost
	middleware.RequestID(),
	middleware.Logger(logger),
	middleware.Recover(logger),
)

// GET /links/go, with X-Request-ID: req-go
// => {"level":"INFO","msg":"middleware: request","request_id":"req-go","method":"GET","route":"GET /links/{code}","status":200}
// GET /links/bug, whose handler panics, with X-Request-ID: req-bug
// => {"level":"ERROR","msg":"middleware: panic","request_id":"req-bug","panic":"index out of range"}
// the status, the X-Request-ID and the body of the response
// => 500 req-bug {"type":"about:blank","title":"Internal Server Error","status":500}
```

The records above leave out the time and the duration.

### [Measure requests by route and operation](https://pkg.go.dev/github.com/tyr-go/tyr#example-RequestInfo)

Middleware above the transports gets the route and the operation of a request from a `tyr.RequestInfo`, which the transport fills in, even if middleware in between passes on another request. A batch has no single operation:

<!-- Output: tyr.ExampleRequestInfo -->
```go
func metrics(next http.Handler) http.Handler {
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

// GET /links/go
// => GET /links/{code} links.get
// POST /rpc, a call of links.get
// => POST /rpc links.get
// POST /rpc, a batch
// => POST /rpc -
```

A whole service, [`examples/shortlink`](examples/shortlink), is a URL shortener built on tyr the way a user would build it: an in-memory store, REST and JSON-RPC, a contract that documents it and that its tests call with the typed client, OpenAPI and OpenRPC documents, validation, `MapError`, authorization with an interceptor, middleware, graceful shutdown and end-to-end tests.

## Middleware

| Middleware | What it does | Where it goes |
|---|---|---|
| [`middleware.RequestID`](https://pkg.go.dev/github.com/tyr-go/tyr/middleware#RequestID) | Keeps a valid `X-Request-ID` or makes a UUIDv7, and puts it in the response and the context | first |
| [`middleware.Logger`](https://pkg.go.dev/github.com/tyr-go/tyr/middleware#Logger) | Writes a record per request: the method, the route, the operation, the status and the duration | under RequestID |
| [`middleware.Recover`](https://pkg.go.dev/github.com/tyr-go/tyr/middleware#Recover) | Turns a panic into a 500 problem and logs it with the stack | under Logger |
| [`http.CrossOriginProtection`](https://pkg.go.dev/net/http#CrossOriginProtection) | Rejects unsafe cross-origin requests, against CSRF; from the standard library | under Recover |
| [`rest.ProblemHandler`](https://pkg.go.dev/github.com/tyr-go/tyr/rest#ProblemHandler) | Makes the 404 and 405 of the mux problems, as the errors of operations are | around the mux |

`middleware.Chain` applies them, the first outermost. Any `func(http.Handler) http.Handler` goes in the chain, those of the standard library too, and middleware of your own, such as authentication, may go under all of them.

## Roadmap

- [x] v0.1: REST
- [x] v0.2: JSON-RPC 2.0, the `canceled` kind, `RequestInfo` for access logs and metrics
- [x] v0.3: contracts (`Define`), a typed JSON-RPC client, an in-process client for tests
- [x] v0.4: OpenAPI 3.1 and OpenRPC documents, with JSON Schemas of the same types; problem types of kinds
- [ ] OpenTelemetry, timeouts, CORS and an adapter for all of go-playground/validator
- [ ] Later: a REST client, a TypeScript client, NATS and MCP

The API may change until v1.

## Development

```sh
gofmt -l .
go vet ./...
go test -race ./...
golangci-lint run
(cd internal/playgroundtest && go test ./...)
(cd internal/schematest && go test ./...)
go test ./rest ./jsonrpc ./internal/plan ./examples/shortlink -update
go test -run '^$' -bench . -benchmem ./...
```

- golangci-lint v2.13.2 must be built with Go 1.27, for generic methods: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2`.
- `internal/playgroundtest` is a module of its own, so that the root module has no dependencies: it checks that the `validate` tags of the core fail the same fields as go-playground/validator v10.30.5.
- `internal/schematest` is one too: with a JSON Schema validator, it checks the schemas against the JSON that `encoding/json/v2` writes and reads, in Draft 7 and 2020-12, and every OpenAPI and OpenRPC document of the repository against the official schemas of those formats.
- `-update` rewrites the golden files, the documents among them.
- `TestREADME` checks the results in this README against the output of the examples they come from, named by a comment such as `<!-- Output: rest.ExampleMount -->` before the block.
- CI runs these on every push and pull request. Commits follow [Conventional Commits](https://www.conventionalcommits.org).

## License

[MIT](LICENSE)
