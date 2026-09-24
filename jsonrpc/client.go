package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonreq"
	"github.com/tyr-go/tyr/internal/reqid"
)

// Client calls operations over JSON-RPC 2.0, at an endpoint that
// [Handler] serves, by their contracts, which [tyr.Define] makes and the
// server shares:
//
//	c := jsonrpc.NewClient("http://links.internal/rpc", &http.Client{Timeout: 5 * time.Second})
//	link, err := c.Call(ctx, contract.GetLink, contract.GetReq{Code: "go"})
//	if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok && se.Kind == tyr.KindNotFound {
//		// ...
//	}
//
// A [*ServerError] from [Client.Call] is what the server answered; any
// other error means that no answer came that fits the call. Headers of
// one's own, such as Authorization, go through the Transport of the
// http.Client, as with golang.org/x/oauth2. A Client is safe for concurrent
// use.
//
// A generic method can't be in an interface, so code that calls a service
// declares the small interface it needs, over a Client, and its tests run
// the service in the same process, with a fake implementation of the
// contract and a client of [github.com/tyr-go/tyr/inprocess.Client]:
//
//	api := tyr.New()
//	api.Implement(contract.GetLink, fakeGetLink)
//	c := jsonrpc.NewClient("http://links/rpc", inprocess.Client(jsonrpc.Handler(api)))
//
// # Errors of another service
//
// Translate the errors of another service where you call it. A handler that
// returns the error of Call as it is fails with internal, which the API
// logs with the name of the operation and what the service answered, and
// its own clients learn nothing of the other service: neither its
// unauthenticated, which is about the credentials of the handler rather
// than theirs, nor its violations, which point into a request they didn't
// send. A kind that means something to them goes to them in the words of
// the handler:
//
//	link, err := links.Call(ctx, contract.GetLink, contract.GetReq{Code: code})
//	if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok && se.Kind == tyr.KindNotFound {
//		return nil, tyr.NotFound("no link %q", code).WithCause(err)
//	}
//	if err != nil {
//		return nil, err
//	}
//
// The other errors follow the rules of [tyr.Operation.Call]: the error of a
// context that is done becomes deadline_exceeded or canceled, and the rest
// internal, a failed connection and the 503 of the load balancer of the
// service while it is being deployed included. To report the failures of
// the service as unavailable, map them:
//
//	api.MapError(func(err error) error {
//		if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok {
//			if se.Kind == tyr.KindUnavailable || se.Kind == tyr.KindDeadlineExceeded {
//				return tyr.Unavailable("links are unavailable").WithCause(err)
//			}
//			return nil // whatever else it answered is internal to us
//		}
//		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
//			return nil // the rules of Call are right for these
//		}
//		// A failed exchange of Call has the Op "Post", as http.Client names
//		// the method; an error of url.Parse has "parse".
//		ue, isURL := errors.AsType[*url.Error](err)
//		he, isHTTP := errors.AsType[*jsonrpc.HTTPError](err)
//		if (isURL && ue.Op == "Post") || (isHTTP && he.StatusCode >= 502 && he.StatusCode <= 504) {
//			return tyr.Unavailable("links are unavailable").WithCause(err)
//		}
//		return nil
//	})
type Client struct {
	endpoint string
	hc       *http.Client  // a copy of that of NewClient, which doesn't follow redirects
	limit    int64         // of the body of a response
	ids      atomic.Uint64 // the last id of a call
}

// defaultMaxResponse is the size limit of responses without
// MaxResponseBytes.
const defaultMaxResponse = 4 << 20

// ClientOption configures a [Client].
type ClientOption func(*Client)

// MaxResponseBytes limits the body of a response to n bytes, 4 MiB by
// default, counted as the client reads it, after net/http decompresses it:
// a response compressed with gzip counts at its full size. A larger
// response fails the call with an error of its own, and the client reads no
// more of it. MaxResponseBytes panics if n isn't positive.
func MaxResponseBytes(n int64) ClientOption {
	if n <= 0 {
		panic(fmt.Sprintf("jsonrpc: MaxResponseBytes(%d): want a positive size", n))
	}
	return func(c *Client) { c.limit = n }
}

// NewClient returns a client that sends calls to endpoint, an absolute
// http or https URL, with a copy of hc, configured by opts. The copy
// doesn't follow redirects: a JSON-RPC endpoint answers every call with
// 200, so a redirect fails the call with an [*HTTPError] of its status,
// and the call goes nowhere else, nor do the headers that the Transport of
// hc adds, such as a token. Changes to hc after NewClient don't reach the
// client.
//
// NewClient panics if endpoint isn't such a URL, as "localhost:8080/rpc"
// isn't, or hc or an option is nil; hc has no default, since
// [http.DefaultClient] has no timeout.
func NewClient(endpoint string, hc *http.Client, opts ...ClientOption) *Client {
	u, err := url.Parse(endpoint)
	shown := endpoint
	if err == nil {
		shown = redacted(u) // a panic shouldn't print a password
	}
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		panic(fmt.Sprintf("jsonrpc: NewClient(%q): want an absolute http or https URL", shown))
	}
	if hc == nil {
		panic(fmt.Sprintf("jsonrpc: NewClient(%q): nil http.Client", shown))
	}
	c := &Client{endpoint: endpoint, hc: noRedirects(hc), limit: defaultMaxResponse}
	for _, opt := range opts {
		if opt == nil {
			panic(fmt.Sprintf("jsonrpc: NewClient(%q): nil option", shown))
		}
		opt(c)
	}
	return c
}

// noRedirects returns a copy of hc that doesn't follow redirects, but
// returns them as its response.
func noRedirects(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// Call calls the operation that contract defines with req and returns its
// result. It sends one request object, whose method is the name of the
// operation and whose params are req in JSON, and decodes the result into a
// Res. The request ID of ctx, see [tyr.RequestIDFrom], goes in the
// X-Request-ID header, if it is 1 to 128 characters of [A-Za-z0-9._:-], as
// the middleware.RequestID of the next service requires.
//
// An error that the server answered is a [*ServerError]. Its Kind is the
// kind in the data of the error, or else the kind whose errors get its
// code, as the package documentation lists them, with 409 as
// failed_precondition; other codes are [tyr.KindInternal]. Its Details are
// the details in the data, or nil. Violations can be decoded from them:
//
//	var v tyr.Violations
//	if se, ok := errors.AsType[*jsonrpc.ServerError](err); ok && se.Kind == tyr.KindInvalidArgument {
//		err := json.Unmarshal(se.Details, &v)
//		// ...
//	}
//
// Any other error means that no answer came that fits the call:
//
//   - a failed HTTP exchange, reading the response included, is a
//     [*url.Error] with the Op "Post", as [http.Client.Do] returns it;
//     once ctx is done, [errors.Is] finds the error of ctx in it
//   - a response with a status other than 200 OK, a redirect included, is
//     an [*HTTPError]
//   - a request that can't be encoded, a response that isn't the JSON-RPC
//     response to the call, one larger than the limit of
//     [MaxResponseBytes] and a result that doesn't decode into a Res are
//     errors of their own
//
// None of them is a [*tyr.Error]: see the documentation of [Client] for
// what a handler that returns them does. Call wraps them with the name of
// the operation; [errors.AsType] finds them in it. It also fails for the
// zero Contract, which has no name.
func (c *Client) Call[Req, Res any](ctx context.Context, contract tyr.Contract[Req, Res], req Req) (Res, error) {
	var res Res
	name := contract.Name()
	if name == "" {
		return res, errors.New("jsonrpc: Call: zero Contract, make one with tyr.Define")
	}
	id := c.ids.Add(1)
	body, err := json.Marshal(request[Req]{JSONRPC: version, Method: name, Params: req, ID: id})
	if err != nil {
		return res, fmt.Errorf("jsonrpc: %s: encoding the request: %w", name, err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return res, fmt.Errorf("jsonrpc: %s: %w", name, err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	if rid, ok := tyr.RequestIDFrom(ctx); ok && reqid.Valid(rid) {
		hreq.Header.Set(reqid.Header, rid)
	}

	resp, err := c.hc.Do(hreq)
	if err != nil {
		return res, fmt.Errorf("jsonrpc: %s: %w", name, exchangeError(ctx, hreq, err))
	}
	defer func() { _ = resp.Body.Close() }() // the call is done by then
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("jsonrpc: %s: %w", name, &HTTPError{StatusCode: resp.StatusCode})
	}
	if ct := resp.Header.Get("Content-Type"); !jsonreq.IsJSON(ct) {
		return res, fmt.Errorf("jsonrpc: %s: invalid response: Content-Type is %q, want JSON", name, ct)
	}
	if resp.ContentLength > c.limit {
		return res, c.tooLarge(name)
	}
	// One byte over the limit tells a body that is larger.
	data, err := io.ReadAll(io.LimitReader(resp.Body, min(c.limit, math.MaxInt64-1)+1))
	if err != nil {
		return res, fmt.Errorf("jsonrpc: %s: %w", name, exchangeError(ctx, hreq, err))
	}
	if int64(len(data)) > c.limit {
		return res, c.tooLarge(name)
	}

	result, se, err := parseReply(data, id)
	switch {
	case err != nil:
		return res, fmt.Errorf("jsonrpc: %s: invalid response: %w", name, err)
	case se != nil:
		return res, fmt.Errorf("jsonrpc: %s: %w", name, se)
	}
	if err := json.Unmarshal(result, &res); err != nil {
		return res, fmt.Errorf("jsonrpc: %s: decoding the result: %w", name, err)
	}
	return res, nil
}

// tooLarge returns the error of a call of the operation name whose response
// is larger than the limit of c.
func (c *Client) tooLarge(name string) error {
	return fmt.Errorf("jsonrpc: %s: invalid response: larger than %d bytes", name, c.limit)
}

// request is the request object of a call.
type request[P any] struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  P      `json:"params"`
	ID      uint64 `json:"id"`
}

// exchangeError returns err, which failed the HTTP exchange of req, as a
// *url.Error, as http.Client.Do returns it, reading the response included.
// If ctx is done, errors.Is finds the error of ctx in it, even when the
// transport reports a cause of ctx or an error of its own instead.
func exchangeError(ctx context.Context, req *http.Request, err error) error {
	if cerr := ctx.Err(); cerr != nil && !errors.Is(err, cerr) {
		err = fmt.Errorf("%w: %w", cerr, err)
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		// "Post", not "POST": http.Client names the method so.
		err = &url.Error{Op: "Post", URL: redacted(req.URL), Err: err}
	}
	return err
}

// redacted returns u with "***" for its password, as http.Client shows a
// URL in its errors.
func redacted(u *url.URL) string {
	if _, ok := u.User.Password(); ok {
		return strings.Replace(u.String(), u.User.String()+"@", u.User.Username()+":***@", 1)
	}
	return u.String()
}

// parseReply parses data as the response object to the call with the
// given id, and returns its result or its error.
func parseReply(data []byte, id uint64) (jsontext.Value, *ServerError, error) {
	var r struct {
		JSONRPC string         `json:"jsonrpc"`
		Result  jsontext.Value `json:"result"`
		Error   *struct {
			Code    int            `json:"code"`
			Message string         `json:"message"`
			Data    jsontext.Value `json:"data"`
		} `json:"error"`
		ID jsontext.Value `json:"id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, nil, err
	}
	switch {
	case r.JSONRPC != version:
		return nil, nil, fmt.Errorf("jsonrpc is %q, want %q", r.JSONRPC, version)
	case (r.Result == nil) == (r.Error == nil):
		return nil, nil, errors.New("want either a result or an error")
	}
	// An error may have a null id: the server can't tell the id of a
	// request it can't parse.
	want := strconv.AppendUint(nil, id, 10)
	switch {
	case r.ID == nil:
		return nil, nil, errors.New("no id")
	case !bytes.Equal(r.ID, want) && (r.Error == nil || r.ID.Kind() != 'n'):
		return nil, nil, fmt.Errorf("id is %s, want %s", r.ID, want)
	case r.Error != nil:
		return nil, errorOfReply(r.Error.Code, r.Error.Message, r.Error.Data), nil
	}
	return r.Result, nil, nil
}

// ServerError is the error that the server answered a call with; see
// [Client.Call]. Call returns it wrapped with the name of the operation,
// and [errors.AsType] finds it.
//
// It isn't a [*tyr.Error]: a handler that returns it as is fails with
// internal, which the API logs with it, so a kind of another service reaches
// the clients of the handler only when the handler translates it, as the
// documentation of [Client] shows.
type ServerError struct {
	Kind    tyr.Kind       // data.kind, or else the kind of Code; KindInternal for other codes
	Code    int            // the code of the error, such as 404 or -32602
	Message string         // as the server sent it
	Details jsontext.Value // data.details, or nil
}

// Error returns the kind, the code and the message of e, such as
// not_found (404): link "go" not found.
func (e *ServerError) Error() string {
	return fmt.Sprintf("%v (%d): %s", e.Kind, e.Code, e.Message)
}

// errorOfReply returns the error that a server answered, with the given
// code, message and data, as described at Client.Call.
func errorOfReply(code int, message string, data jsontext.Value) *ServerError {
	e := &ServerError{Kind: kindOfCode(code), Code: code, Message: message}
	var d struct {
		Kind    jsontext.Value `json:"kind"`
		Details jsontext.Value `json:"details"`
	}
	// Any valid JSON object decodes: the members are raw values.
	if data.Kind() != '{' || json.Unmarshal(data, &d) != nil {
		return e
	}
	var name string
	if d.Kind.Kind() == '"' && json.Unmarshal(d.Kind, &name) == nil {
		var k tyr.Kind
		if k.UnmarshalText([]byte(name)) == nil {
			e.Kind = k
		}
	}
	if d.Details != nil && d.Details.Kind() != 'n' {
		e.Details = d.Details
	}
	return e
}

// kindOfCode returns the kind of an error with code but without a kind:
// the kind whose errors get the code, as codeOf has it, or internal. Of
// already_exists and failed_precondition, which both get 409, it's the
// latter, the more general one.
func kindOfCode(code int) tyr.Kind {
	switch code {
	case codeInvalidParams:
		return tyr.KindInvalidArgument
	case http.StatusUnauthorized:
		return tyr.KindUnauthenticated
	case http.StatusForbidden:
		return tyr.KindPermissionDenied
	case http.StatusNotFound:
		return tyr.KindNotFound
	case http.StatusConflict:
		return tyr.KindFailedPrecondition
	case http.StatusTooManyRequests:
		return tyr.KindResourceExhausted
	case 499:
		return tyr.KindCanceled
	case http.StatusServiceUnavailable:
		return tyr.KindUnavailable
	case http.StatusGatewayTimeout:
		return tyr.KindDeadlineExceeded
	}
	return tyr.KindInternal
}

// HTTPError is the error of a call whose response has an HTTP status other
// than 200 OK. A JSON-RPC server answers every call with 200, errors
// included, so another status means that no answer came: a load balancer
// replies 502, 503 or 504 while the service is being deployed, a wrong
// endpoint gets 404 or a redirect, which the client doesn't follow, and a
// request that the server doesn't take as JSON-RPC gets 405, 413 or 415.
type HTTPError struct {
	StatusCode int // such as 502
}

// Error returns the status, such as "HTTP status 502 Bad Gateway".
func (e *HTTPError) Error() string {
	s := "HTTP status " + strconv.Itoa(e.StatusCode)
	if text := http.StatusText(e.StatusCode); text != "" {
		s += " " + text
	}
	return s
}
