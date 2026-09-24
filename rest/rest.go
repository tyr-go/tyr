// Package rest serves the operations of a [tyr.API] over REST: [Mount]
// registers a handler on an [http.ServeMux] for every operation with a
// [Route].
//
//	api.Handle("links.get", links.Get, rest.Route("GET /links/{code}"))
//	rest.Mount(mux, api)
//
// # Requests
//
// A request becomes the operation's Req in two steps. First the JSON body,
// if there is one, is decoded into it. A body needs a JSON Content-Type,
// application/json or a +json type, or the response is 415 Unsupported
// Media Type; a body over the limit, 1 MiB by default (see [MaxBodyBytes]),
// gets 413. Then the fields tagged path, query or header get the value of
// that path wildcard, query parameter or header, over what the body set:
//
//	type ListReq struct {
//		Owner string   `json:"owner" path:"owner"`
//		Tags  []string `json:"tags" query:"tag"`
//	}
//
// Such fields may be strings, bools, integers and floats, or implement
// encoding.TextUnmarshaler, or be pointers to those, which get a value only
// when there is one; query fields may also be slices of those. Values must
// be valid UTF-8, as JSON strings are, and numbers are written as in JSON:
// in decimal, without NaN, infinities, a plus sign, leading zeros or
// underscores. A time.Time is an RFC 3339 time in the path and the query,
// and in a header an HTTP date, as RFC 9110 has them, or else an RFC 3339
// time, for headers of one's own. A value that doesn't fit fails the call
// with [tyr.KindInvalidArgument] and a [tyr.Violation]: its pointer is the
// JSON name of the field, and its detail names the source, e.g. query
// parameter "tag": must be an integer.
//
// A missing query parameter or header leaves the field as the body set it,
// so a client can set any bound field in JSON, as it can over JSON-RPC.
// Bind only what the client says: what the server trusts, such as the
// tenant or the user, comes through the context, from middleware or an
// interceptor.
//
// # Responses
//
// A result is sent as JSON with status 200 or the one set by [Status]. A
// field of the result tagged header sets that header of the response, of
// the same types as bound fields; a time.Time is an HTTP date. A nil
// pointer, an empty string and a zero time set none. The field stays in the
// JSON, which JSON-RPC sends too, unless it has json:"-":
//
//	type FollowRes struct {
//		URL string `json:"url" header:"Location"`
//	}
//	api.Handle("links.follow", Follow, rest.Route("GET /{code}"), rest.Status(http.StatusFound))
//
// A redirect, 301, 302, 303, 307 or 308, needs such a Location field and
// has no body. A result without JSON members, such as struct{}, has no body
// either, and its status is 204 by default.
//
// A type may be both a request and a result. Its path and query tags mean
// nothing in a result, but its header tags work both ways: a field is bound
// from the header of the request and sets the header of the response.
//
// An error is sent as application/problem+json (RFC 9457) with the status
// of its kind:
//
//   - invalid_argument: 400
//   - unauthenticated: 401
//   - permission_denied: 403
//   - not_found: 404
//   - already_exists, failed_precondition: 409
//   - resource_exhausted: 429
//   - canceled: 499, which nginx has for a client that went away
//   - unavailable: 503
//   - deadline_exceeded: 504
//   - internal and unknown kinds: 500
//
// The problem's detail is the error's message and its kind is the kind's
// name. [tyr.Violations] go in its errors member, other details in its
// details member. An internal error, and one of a kind rest doesn't know,
// has the detail "internal error", the kind internal and no details: its
// message and details are for the logs, where the API writes them. A 401
// carries the WWW-Authenticate challenges of [Challenge]. 413 and 415 have
// no kind: they are about the HTTP request, and the operation isn't called.
//
// A result or details that can't be encoded, or a redirect without a
// Location, are a bug of the server: they are logged with
// [tyr.API.Logger], with the request's context, which also carries the
// operation (see [tyr.WithOperation]), and the client gets an internal
// error or the problem without its details.
//
// For middleware above it, such as an access log, the handler of an
// operation records its route and the operation in the [tyr.RequestInfo]
// of the request, if there is one, before anything else.
//
// Outside operations, [WriteError] writes an error as an operation's, and
// [WriteProblem] a problem of the HTTP request, such as the 403 of
// [http.CrossOriginProtection]. [ProblemHandler] makes the 404 and 405 of
// the mux problems too, so that the API speaks one format of errors.
package rest

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tyr-go/tyr"
)

// The keys of the options; RouteOf reads the route.
var (
	routeKey  = tyr.NewMetaKey[string]("rest.route")
	statusKey = tyr.NewMetaKey[int]("rest.status")
	limitKey  = tyr.NewMetaKey[int64]("rest.max_body_bytes")
)

// defaultLimit is the size limit of request bodies without MaxBodyBytes.
const defaultLimit = 1 << 20

// Route serves an operation at pattern, in the syntax of [http.ServeMux]
// with a method: "GET /links/{code}". Every wildcard of the pattern needs a
// field of the request with a path tag of the same name, and every such
// field needs a wildcard. [Mount] checks that, and ServeMux checks the
// syntax.
func Route(pattern string) tyr.OpOption {
	return routeKey.Option(pattern)
}

// RouteOf returns the pattern that [Route] set for op and reports whether
// op has one.
func RouteOf(op *tyr.Operation) (pattern string, ok bool) {
	return routeKey.Get(op)
}

// Status sets the status of a successful response, which is 200 by default
// and 204 for a result without JSON members, such as struct{}. It may be a
// 2xx status or a redirect, 301, 302, 303, 307 or 308, which has no body
// and needs a field with header:"Location" in the result. Status panics on
// other codes, and [Mount] panics on a redirect without such a field or on
// 204 or 205 for a result with JSON members.
func Status(code int) tyr.OpOption {
	if (code < 200 || code > 299) && !isRedirect(code) {
		panic(fmt.Sprintf("rest: Status(%d): want a 2xx status or a redirect: 301, 302, 303, 307 or 308", code))
	}
	return statusKey.Option(code)
}

// isRedirect reports whether code is a status that Status allows for
// redirects.
func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// MaxBodyBytes limits the size of request bodies to n bytes, 1 MiB by
// default; a larger body gets 413 Request Entity Too Large. Give it to a
// group to set the limit of several operations. MaxBodyBytes panics if n
// isn't positive.
func MaxBodyBytes(n int64) tyr.OpOption {
	if n <= 0 {
		panic(fmt.Sprintf("rest: MaxBodyBytes(%d): want a positive size", n))
	}
	return limitKey.Option(n)
}

// MountOption configures [Mount].
type MountOption func(*mount)

// mount is the configuration of Mount.
type mount struct {
	challenges []string // of WWW-Authenticate
}

// Challenge makes the 401 Unauthorized responses of the operations that
// [Mount] serves carry the challenge in a WWW-Authenticate header, as RFC
// 9110 requires, e.g. `Bearer realm="shortlink"`; each Challenge adds one.
// Challenge panics if the challenge is empty or has a line break.
func Challenge(challenge string) MountOption {
	if challenge == "" || strings.ContainsAny(challenge, "\r\n") {
		panic(fmt.Sprintf("rest: Challenge(%q): want a challenge without line breaks", challenge))
	}
	return func(m *mount) {
		m.challenges = append(m.challenges, challenge)
	}
}

// Mount seals api and registers a handler on mux for every operation that
// has a [Route]; operations without one are left out.
//
// Mount panics if a pattern has no method, ServeMux rejects a pattern or
// finds that it conflicts with another, a wildcard has no path field or a
// path field has no wildcard, a bound field can't be bound, a query tag has
// a comma or a space or a header tag isn't a header name, a field of a
// result can't set its header, or [Status] sets a redirect for a result
// without a Location field or 204 or 205 for a result with JSON members.
// It also panics if mux, api or an option is nil.
func Mount(mux *http.ServeMux, api *tyr.API, opts ...MountOption) {
	if mux == nil || api == nil {
		panic("rest: Mount: nil mux or API")
	}
	var m mount
	for _, opt := range opts {
		if opt == nil {
			panic("rest: Mount: nil option")
		}
		opt(&m)
	}
	api.Seal()
	for op := range api.Operations() {
		pattern, ok := RouteOf(op)
		if !ok {
			continue
		}
		handle(mux, op, pattern, newHandler(api, op, pattern, &m))
	}
}
