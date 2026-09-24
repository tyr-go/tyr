// Package tyr provides typed operations: a handler is written once as a
// plain func(ctx, Req) (Res, error), with no HTTP types, and transports
// serve it, such as REST, see [github.com/tyr-go/tyr/rest]. Whatever the
// transport, a call goes through [Operation.Call]: the request is decoded,
// passed through the interceptors (see [API.Use]) and validated, and an
// error of the handler becomes an [Error] of a [Kind].
//
// # Contracts
//
// An operation may be defined apart from its handler, as a contract that
// the server and its clients share, such as the typed client of
// [github.com/tyr-go/tyr/jsonrpc]:
//
//	var GetLink = tyr.Define[GetLinkReq, *Link]("links.get", rest.Route("GET /links/{code}"))
//
//	api.Implement(GetLink, links.Get)
//
// The compiler checks that the handler fits the contract, as it checks the
// calls of clients. [API.Handle] is short for Implement(Define(...)).
//
// # Documentation
//
// Options document an operation for the documents that transports make of
// an API, and change nothing at run time: [Summary], [Description], [Tags],
// [Deprecated] and [Errors], the kinds of the errors that it may return.
// A contract carries its documentation too, so the code and the documents
// share one source, and [Contract.Example] adds examples of calls, a request
// and the result it gets, whose types the compiler checks:
//
//	var GetLink = tyr.Define[GetLinkReq, *Link]("links.get", tyr.Summary("Get a link"), tyr.Errors(tyr.KindNotFound)).
//		Example("go", GetLinkReq{Code: "go"}, &Link{Code: "go", URL: "https://go.dev"})
//
// The schemas of the documents are named after their types, Link in
// results and LinkInput in requests, and a type may name its own; see
// [SchemaNamer].
//
// # Validation
//
// [Operation.Call] checks a request against the validate tags of its
// fields, then calls its Validate method if it's a [Validator]:
//
//	type CreateReq struct {
//		URL  string `json:"url" validate:"required,http_url"`
//		Code string `json:"code" validate:"omitempty,min=4,max=16"`
//	}
//
// The tags are a subset of those of go-playground/validator, with the same
// meaning as there with the option WithRequiredStructEnabled: required,
// omitempty, min, max, len, gt, gte, lt, lte, oneof, email, url, http_url
// and uuid. Strings are measured in runes, slices and maps by length,
// numbers by value. Nested and embedded structs are checked too, but not
// the elements of slices and maps.
//
// A field fails on its first failing rule, and each failing field adds one
// [Violation], with the JSON Pointer of the field; the violations come in
// the order of the fields. An unknown rule, a rule that doesn't apply to
// the type of its field, or a bad parameter makes [API.Handle] panic.
//
// # For transports
//
// A transport serves the operations of an API; handlers and interceptors
// don't need these. It seals the API with [API.Seal] when it mounts it,
// runs every request with [Operation.Call], keeps the operation in the
// context beyond the call with [WithOperation], and records what served a
// request with [RequestInfo.Record], which it finds with [RequestInfoFrom].
package tyr

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/tyr-go/tyr/internal/plan"
)

// Handler is the only shape business logic takes: a plain function of a
// context and a request, unaware of the transport that serves it.
type Handler[Req, Res any] = func(ctx context.Context, req Req) (Res, error)

// API is a set of operations. Create it with [New], register operations
// with [API.Handle] or [API.Implement] and mount it on transports, which
// [API.Seal] it.
//
// Configure an API from a single goroutine before serving it. Once sealed,
// it is read-only, and its operations may be called concurrently.
type API struct {
	ops          []*Operation
	names        map[string]bool
	mappers      []func(error) error
	interceptors []Interceptor
	log          *slog.Logger
	sealed       bool
}

// Option configures an [API] created by [New].
type Option func(*API)

// WithLogger sets the logger for failed calls that need attention.
//
// A panic is logged at the error level with its stack as soon as it is
// recovered, even if an interceptor then turns the call into a success.
// Other errors are logged once, for the call's final result: internal
// errors, and errors of kinds this package doesn't define, at the error
// level, exceeded deadlines and unavailable services at the warning level,
// unless the caller canceled the call. A record has the error, with its
// message and cause, and its details, if any: clients get neither the
// message nor the details of an internal error.
//
// Records are logged with the call's context, which carries the operation;
// see [OperationFrom]. By default, the API logs to [slog.Default] as it is
// at the time of logging.
func WithLogger(l *slog.Logger) Option {
	return func(a *API) { a.log = l }
}

// New returns an empty API configured by opts.
func New(opts ...Option) *API {
	a := &API{names: make(map[string]bool)}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// MapError adds a mapper that translates errors of other packages into an
// [Error], such as a store's ErrNotFound into [KindNotFound], so that those
// packages don't have to import tyr.
//
// A failed call's error reaches the mappers only if it doesn't contain an
// [Error] already. Each mapper gets that original error, in the order the
// mappers were added, and the first result that contains an [Error] is
// used; results without one, including nil, are ignored. See
// [Operation.Call] for what happens to errors no mapper translates.
//
// MapError panics if fn is nil or the API is sealed.
func (a *API) MapError(fn func(error) error) {
	a.checkOpen("MapError")
	if fn == nil {
		panic("tyr: MapError: nil mapper")
	}
	a.mappers = append(a.mappers, fn)
}

// Handle registers h as an operation with the given name and returns the
// operation; it is short for Implement(Define(name, opts...), h). The name
// is one or more dot-separated segments of ASCII letters, digits, '_' and
// '-', such as "links.get"; the first segment can't be "rpc", which
// JSON-RPC reserves. Req must be a struct type, and its validate tags must
// be valid; see the package documentation.
//
// opts configure the operation, e.g. with a route for a transport, and
// apply in order; examples of calls for its documentation need a contract,
// see [Contract.Example]. Handle panics if the name is invalid or already
// taken, h or an option is nil, Req isn't a struct or has an invalid
// validate tag, Req has no Validate method because those of structs it
// embeds conflict (see [Validator]), or the API is sealed.
func (a *API) Handle[Req, Res any](name string, h Handler[Req, Res], opts ...OpOption) *Operation {
	call := fmt.Sprintf("Handle(%q)", name)
	return register(a, call, define[Req, Res](call, name, opts), h, nil)
}

// Implement registers h as the handler of the operation that c defines,
// with the options of c, and returns the operation. The compiler checks
// that h fits c, so an implementation can't drift from the contract its
// clients call.
//
// Implement panics if c is the zero Contract, its name is already taken, h
// is nil, Req has an invalid validate tag or no Validate method because
// those of structs it embeds conflict (see [Validator]), an example of c
// doesn't fit (see [Contract.Example]), or the API is sealed. [Define] has
// checked the rest.
func (a *API) Implement[Req, Res any](c Contract[Req, Res], h Handler[Req, Res]) *Operation {
	return implement(a, c, h, nil)
}

// Group returns a group whose operations get opts before their own
// options, e.g. to require a role for all of them.
func (a *API) Group(opts ...OpOption) *Group {
	return &Group{api: a, opts: slices.Clone(opts)}
}

// Operations returns the registered operations in the order of
// registration.
func (a *API) Operations() iter.Seq[*Operation] {
	return slices.Values(a.ops)
}

// Use adds interceptors that run around the handler of every operation, in
// the order they are added: the first is the outermost, like the first
// middleware in middleware.Chain, and later ones go inside earlier ones.
// An interceptor meant for some operations only looks at their metadata.
// Use panics if an interceptor is nil or the API is sealed.
func (a *API) Use(ics ...Interceptor) {
	a.checkOpen("Use")
	for _, ic := range ics {
		if ic == nil {
			panic("tyr: Use: nil interceptor")
		}
	}
	a.interceptors = append(a.interceptors, ics...)
}

// Seal marks the API as complete: registering operations, interceptors or
// error mappers after it panics. Transports seal the API when they mount
// it, so that an operation registered too late fails at startup instead of
// being silently left out. Seal also builds the chain of interceptors of
// every operation once, instead of on every call. Sealing a sealed API does
// nothing.
func (a *API) Seal() {
	if a.sealed {
		return
	}
	a.sealed = true
	for _, op := range a.ops {
		op.invoke = a.chain(op)
	}
}

// checkOpen panics if a is sealed; call names the offending call.
func (a *API) checkOpen(call string) {
	if a.sealed {
		panic("tyr: " + call + " after Seal: register everything before mounting the API")
	}
}

// Logger returns the logger set by [WithLogger] or, without one,
// [slog.Default] as it is at the time of the call. The API and its
// transports log with it. Call Logger when logging rather than keeping its
// result, so that a default logger set later is used too.
func (a *API) Logger() *slog.Logger {
	if a.log != nil {
		return a.log
	}
	return slog.Default()
}

// Group registers operations in an [API] with shared options; see
// [API.Group].
type Group struct {
	api  *API
	opts []OpOption
}

// Handle is like [API.Handle] but applies the group's options before opts.
func (g *Group) Handle[Req, Res any](name string, h Handler[Req, Res], opts ...OpOption) *Operation {
	call := fmt.Sprintf("Handle(%q)", name)
	return register(g.api, call, define[Req, Res](call, name, opts), h, g.opts)
}

// Implement is like [API.Implement] but applies the group's options before
// those of c.
func (g *Group) Implement[Req, Res any](c Contract[Req, Res], h Handler[Req, Res]) *Operation {
	return implement(g.api, c, h, g.opts)
}

// Group returns a nested group, whose options go after g's.
func (g *Group) Group(opts ...OpOption) *Group {
	return &Group{api: g.api, opts: slices.Concat(g.opts, opts)}
}

// implement implements API.Implement and Group.Implement.
func implement[Req, Res any](a *API, def Contract[Req, Res], h Handler[Req, Res], groupOpts []OpOption) *Operation {
	if def.name == "" {
		panic("tyr: Implement: zero Contract, make one with Define")
	}
	return register(a, fmt.Sprintf("Implement(%q)", def.name), def, h, groupOpts)
}

// register registers h as the handler of def, a checked Contract, with
// groupOpts before the options of def; call names the call in panics.
func register[Req, Res any](a *API, call string, def Contract[Req, Res], h Handler[Req, Res], groupOpts []OpOption) *Operation {
	a.checkOpen(call)
	if a.names[def.name] {
		panic("tyr: " + call + ": duplicate operation name")
	}
	if h == nil {
		panic("tyr: " + call + ": nil handler")
	}
	t := reflect.TypeFor[Req]()
	validation, err := plan.NewValidation(t)
	if err != nil {
		panic("tyr: " + call + ": " + err.Error())
	}
	if conflict := conflictingValidate(t); conflict != "" {
		panic("tyr: " + call + ": " + conflict)
	}

	op := newOperation(a, def.name, h, validation)
	for _, opt := range slices.Concat(groupOpts, def.opts) {
		if opt == nil {
			panic("tyr: " + call + ": nil option") // of a group: Define checked its own
		}
		opt(op)
	}
	if err := checkExamples[Req](op, validation); err != nil {
		panic("tyr: " + call + ": " + err.Error())
	}
	op.registered = true
	a.names[def.name] = true
	a.ops = append(a.ops, op)
	return op
}

// checkName reports what's wrong with name as an operation name, or "" if
// nothing is.
func checkName(name string) string {
	if first, _, _ := strings.Cut(name, "."); first == "rpc" {
		return `the first segment "rpc" is reserved by JSON-RPC`
	}
	for seg := range strings.SplitSeq(name, ".") {
		if seg == "" || strings.ContainsFunc(seg, notNameRune) {
			return "name must be dot-separated segments of ASCII letters, digits, '_' and '-'"
		}
	}
	return ""
}

// notNameRune reports whether r can't appear in a segment of an operation
// name.
func notNameRune(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '_', r == '-':
		return false
	}
	return true
}
