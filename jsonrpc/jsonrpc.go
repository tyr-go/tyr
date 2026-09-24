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
//     an empty batch, one over the limit, and an rpc.discover of a batch
//     that has had one
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
// and the server shares with its clients, and returns the error of an
// operation as a [ServerError] with its kind, for the caller to translate:
//
//	c := jsonrpc.NewClient("http://links.internal/rpc", &http.Client{Timeout: 5 * time.Second})
//	link, err := c.Call(ctx, contract.GetLink, contract.GetLinkReq{Code: "go"})
//
// A client doesn't follow redirects, and it reads responses of up to 4 MiB,
// counted after decompression; see [MaxResponseBytes].
// [github.com/tyr-go/tyr/inprocess.Client] serves its calls with a handler
// in memory, with the whole lifecycle of a call, for tests and for calls
// within one program.
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

// HandlerOption configures a [Handler].
type HandlerOption func(*config)

// config is the configuration of a Handler.
type config struct {
	maxBatch int       // calls in a batch
	limit    int64     // of the request body
	discover *tyr.Info // with Discover: the API, as the OpenRPC document tells of it
}

// MaxBatch limits a batch to n calls, 50 by default: a larger batch gets a
// single error, -32600 Invalid Request, and none of its calls run.
// MaxBatch panics if n isn't positive.
func MaxBatch(n int) HandlerOption {
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
func MaxBodyBytes(n int64) HandlerOption {
	if n <= 0 {
		panic(fmt.Sprintf("jsonrpc: MaxBodyBytes(%d): want a positive size", n))
	}
	return func(c *config) { c.limit = n }
}

// Discover makes [Handler] answer the method rpc.discover with the OpenRPC
// 1.4 document of the operations it serves, with info on the API, as the
// OpenRPC specification has it:
//
//	mux.Handle("POST /rpc", jsonrpc.Handler(api, jsonrpc.Discover(tyr.Info{Title: "shortlink", Version: "1.0.0"})))
//
// An operation is a method of its name, with its documentation (see
// [tyr.Doc]): its params, by name, are the members of its request, its
// result is its result, and its errors are invalid_argument, which any call
// may get, and the kinds of [tyr.Errors], one per code. The schemas are JSON
// Schema Draft 7 and say what the server reads and writes, as json/v2 does;
// the validate tags of requests add their constraints, and doc tags
// describe fields. The schemas of struct types are in components, named
// after their types, such as Link for results and LinkInput for requests;
// see [tyr.SchemaNamer].
//
// The schemas fall short of the server in two ways. The schema of an
// element of a slice or a map is that of its type, with the constraints of
// its validate tags, which the server doesn't check without dive. And some
// JSON fits the schema of a request but doesn't decode, since JSON Schema
// can't tell how a value is written, such as an integer written as 1.0, a
// number beyond the range of its type, a time that time.Parse doesn't
// read, or a string that a type which parses itself rejects.
//
// The document is made once, by Handler, which panics on a type of a
// request or a result with a field that JSON can't carry, such as a
// time.Duration, and on two types of one schema name. rpc.discover isn't an operation: the interceptors don't
// see it, and its params are ignored. A batch gets the document once: a
// second rpc.discover in it gets -32600 Invalid Request, so that a small
// request can't ask for many copies of it. Without Discover, it is a method
// that doesn't exist, as any other name that starts with "rpc.". Discover
// panics if info has no Title or Version.
func Discover(info tyr.Info) HandlerOption {
	if info.Title == "" || info.Version == "" {
		panic("jsonrpc: Discover: the Info needs a Title and a Version")
	}
	return func(c *config) { c.discover = &info }
}

// Handler seals api and returns a handler that serves its operations over
// JSON-RPC 2.0, configured by opts. It serves the operations api has at
// the time: registering one after Handler panics, as for a sealed API.
// Handler panics if api or an option is nil.
func Handler(api *tyr.API, opts ...HandlerOption) http.Handler {
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
	if h.discover != nil {
		h.document = openRPCOf(api, *h.discover)
	}
	return h
}
