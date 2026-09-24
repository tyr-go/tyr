// Package jsonrpc serves the operations of a [tyr.API] over JSON-RPC 2.0,
// at a single HTTP endpoint, and calls them with a typed [Client]. The
// method of a call is the name of an operation, and its params are the
// operation's request, by name:
//
//	mux.Handle("POST /rpc", jsonrpc.Handler(api))
//
//	--> {"jsonrpc": "2.0", "method": "links.get", "params": {"code": "go"}, "id": 1}
//	<-- {"jsonrpc": "2.0", "result": {"code": "go", "url": "https://go.dev"}, "id": 1}
//
// Every call goes through [tyr.Operation.Call], so an operation has the
// same interceptors, validation and errors as over REST.
//
// # Requests
//
// A request is a POST with a JSON body, application/json or a +json type,
// or it gets 415 Unsupported Media Type; a body over the limit, 1 MiB by
// default (see [MaxBodyBytes]), gets 413, and another method 405. Those
// three are problems of the HTTP request, sent as application/problem+json
// (RFC 9457), as rest sends them.
//
// The body is a request object or a batch, an array of them. params is an
// object whose members are those of the operation's request, and it's
// decoded as a REST body is, so a value that doesn't fit gets the same
// violation. Absent params, null and an empty array, which clients send
// for methods without arguments, leave the request zero. Params by
// position, a non-empty array, fail with -32602 and the violation "must be
// an object"; params of another type make the request object invalid.
//
// A request object without an id is a notification: its operation runs,
// but no response is sent, not even an error. A request of notifications
// only gets 204 No Content.
//
// A batch may have up to 50 calls (see [MaxBatch]). They run concurrently,
// at most 8 at a time, and the responses come in the order of the calls.
// Once the client goes away, the calls that haven't started don't.
//
// # Errors
//
// Every JSON-RPC response has HTTP status 200, errors included. The errors
// of the protocol have the codes and the messages of its specification:
//
//   - -32700 Parse error: the body isn't valid JSON, as encoding/json/v2
//     has it: duplicate names and invalid UTF-8 count too
//   - -32600 Invalid Request: a value that isn't a valid request object,
//     an empty batch or one over the limit
//   - -32601 Method not found: no operation has the name; those that
//     start with "rpc." are reserved
//
// The error of an operation has the code of its kind, the message of the
// [tyr.Error], and data with the kind and the details, if there are any:
// {"kind": "not_found", "details": ...}. The codes of kinds are the HTTP
// statuses that rest sends for them, outside the range that JSON-RPC
// reserves, except for two that the specification defines:
//
//   - invalid_argument: -32602, [tyr.Violations] in details
//   - unauthenticated: 401
//   - permission_denied: 403
//   - not_found: 404
//   - already_exists, failed_precondition: 409
//   - resource_exhausted: 429
//   - canceled: 499
//   - unavailable: 503
//   - deadline_exceeded: 504
//   - internal and unknown kinds: -32603
//
// An internal error has the message "internal error" and no data: its
// message and details are for the logs, where the API writes them. A
// result or details that can't be encoded are a bug of the server: they
// are logged with [tyr.API.Logger], with the context of the call, and the
// client gets an internal error or the error without its details.
//
// For middleware above it, such as an access log, the handler records the
// route and the operation of a single call in the [tyr.RequestInfo] of the
// request, if there is one; a batch records no operation.
//
// # Client
//
// [Client] calls operations by their contracts, which [tyr.Define] makes
// and the server shares with its clients, and turns the error of an
// operation back into a [tyr.Error] of its kind:
//
//	c := jsonrpc.NewClient("http://links.internal/rpc", &http.Client{Timeout: 5 * time.Second})
//	link, err := c.Call(ctx, contract.GetLink, contract.GetLinkReq{Code: "go"})
//
// [InProcess] serves the calls of a client with a handler in memory, with
// the whole lifecycle of a call, for tests and for calls within one
// program.
package jsonrpc

import (
	"fmt"
	"net/http"

	"github.com/tyr-go/tyr"
)

// The defaults of the options.
const (
	defaultMaxBatch = 50
	defaultLimit    = 1 << 20
)

// concurrency is the number of the calls of a batch that run at a time.
const concurrency = 8

// Option configures a [Handler].
type Option func(*config)

// config is the configuration of a Handler.
type config struct {
	maxBatch int   // calls in a batch
	limit    int64 // of the request body
}

// MaxBatch limits a batch to n calls, 50 by default: a larger batch gets a
// single error, -32600 Invalid Request, and none of its calls run.
// MaxBatch panics if n isn't positive.
func MaxBatch(n int) Option {
	if n <= 0 {
		panic(fmt.Sprintf("jsonrpc: MaxBatch(%d): want a positive number of calls", n))
	}
	return func(c *config) { c.maxBatch = n }
}

// MaxBodyBytes limits the size of request bodies to n bytes, 1 MiB by
// default; a larger body gets 413 Request Entity Too Large. The limit is on
// the whole request, all the calls of a batch together, unlike that of
// [github.com/tyr-go/tyr/rest.MaxBodyBytes], which is set per operation.
// MaxBodyBytes panics if n isn't positive.
func MaxBodyBytes(n int64) Option {
	if n <= 0 {
		panic(fmt.Sprintf("jsonrpc: MaxBodyBytes(%d): want a positive size", n))
	}
	return func(c *config) { c.limit = n }
}

// Handler seals api and returns a handler that serves its operations over
// JSON-RPC 2.0, configured by opts. It serves the operations api has at
// the time: registering one after Handler panics, as for a sealed API.
// Handler panics if api or an option is nil.
func Handler(api *tyr.API, opts ...Option) http.Handler {
	if api == nil {
		panic("jsonrpc: Handler: nil API")
	}
	h := &handler{api: api, ops: make(map[string]*tyr.Operation), maxBatch: defaultMaxBatch, limit: defaultLimit}
	for _, opt := range opts {
		if opt == nil {
			panic("jsonrpc: Handler: nil option")
		}
		opt(&h.config)
	}
	api.Seal()
	for op := range api.Operations() {
		h.ops[op.Name()] = op
	}
	return h
}
