package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"

	"github.com/tyr-go/tyr"
)

// The error codes that the JSON-RPC 2.0 specification defines.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// version is the version of JSON-RPC that requests and responses have.
const version = "2.0"

// response is a response object of JSON-RPC 2.0: a result or an error.
type response struct {
	JSONRPC string         `json:"jsonrpc"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *errorObject   `json:"error,omitzero"`
	ID      jsontext.Value `json:"id"`
}

// failure returns the response with e to a request whose id can't be told.
func failure(e *errorObject) *response {
	return &response{JSONRPC: version, Error: e, ID: null}
}

// errorObject is the error of a response.
type errorObject struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitzero"`
}

// errorData is the data of the error of an operation.
type errorData struct {
	Kind    string         `json:"kind"`
	Details jsontext.Value `json:"details,omitzero"`
}

// parseError, invalidRequest and methodNotFound return the errors of the
// protocol, with the messages of the specification.
func parseError() *errorObject {
	return &errorObject{Code: codeParseError, Message: "Parse error"}
}

func invalidRequest() *errorObject {
	return &errorObject{Code: codeInvalidRequest, Message: "Invalid Request"}
}

func methodNotFound() *errorObject {
	return &errorObject{Code: codeMethodNotFound, Message: "Method not found"}
}

// internalError returns the error of an internal error, which tells the
// client nothing more.
func internalError() *errorObject {
	return &errorObject{Code: codeInternalError, Message: "internal error"}
}

// respond returns the response to c, whose operation returned o, or nil if
// c is a notification.
func (h *handler) respond(c call, o outcome) *response {
	if c.id == nil {
		return nil // a notification: no response, not even an error
	}
	r := &response{JSONRPC: version, ID: c.id}
	switch {
	case c.err != nil:
		r.Error = c.err
	case o.err != nil:
		r.Error = h.errorOf(o.ctx, o.err)
	default:
		data, err := json.Marshal(o.res)
		if err != nil {
			h.api.Logger().ErrorContext(o.ctx, "jsonrpc: encoding the result", "err", err)
			r.Error = internalError()
			break
		}
		r.Result = data
	}
	return r
}

// errorOf returns the error object of err, the error of a call, which is a
// *tyr.Error. An internal error, or one of a kind jsonrpc doesn't know, is
// sent without its message and details, which are for the logs. Details
// that can't be encoded are logged, with ctx, and left out.
func (h *handler) errorOf(ctx context.Context, err error) *errorObject {
	e, _ := err.(*tyr.Error)
	if e == nil || codeOf(e.Kind) == codeInternalError {
		return internalError()
	}
	d := &errorData{Kind: e.Kind.String()}
	if e.Details != nil {
		// As rest has it, invalid UTF-8 from data becomes U+FFFD.
		details, err := json.Marshal(e.Details, jsontext.AllowInvalidUTF8(true))
		if err != nil {
			h.api.Logger().ErrorContext(ctx, "jsonrpc: encoding error details", "err", err)
		} else {
			d.Details = details
		}
	}
	return &errorObject{Code: codeOf(e.Kind), Message: e.Message, Data: d}
}

// codeOf returns the error code of errors of kind k: the HTTP status of
// rest outside the range of codes that JSON-RPC reserves.
func codeOf(k tyr.Kind) int {
	switch k {
	case tyr.KindInvalidArgument:
		return codeInvalidParams
	case tyr.KindUnauthenticated:
		return http.StatusUnauthorized
	case tyr.KindPermissionDenied:
		return http.StatusForbidden
	case tyr.KindNotFound:
		return http.StatusNotFound
	case tyr.KindAlreadyExists, tyr.KindFailedPrecondition:
		return http.StatusConflict
	case tyr.KindResourceExhausted:
		return http.StatusTooManyRequests
	case tyr.KindCanceled:
		return 499 // nginx's Client Closed Request, as in rest
	case tyr.KindUnavailable:
		return http.StatusServiceUnavailable
	case tyr.KindDeadlineExceeded:
		return http.StatusGatewayTimeout
	}
	return codeInternalError
}

// write sends v, a response or a batch of them, as JSON with status 200.
// Invalid UTF-8, which a message may carry, becomes U+FFFD.
func (h *handler) write(ctx context.Context, w http.ResponseWriter, v any) {
	data, err := json.Marshal(v, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		// Unreachable: results and details are encoded already, and the
		// rest is strings and numbers, which AllowInvalidUTF8 lets
		// through. The line stays so that no later change to response can
		// send a truncated body.
		h.api.Logger().ErrorContext(ctx, "jsonrpc: encoding the response", "err", err)
		data = []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"},"id":null}`)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// problem is a problem details object of RFC 9457, as rest writes those of
// HTTP requests: without a kind.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// writeProblem sends a problem of the HTTP request that isn't a JSON-RPC
// one, as application/problem+json.
func writeProblem(w http.ResponseWriter, status int, detail string) {
	// Strings and a number, and detail is ours: it can't fail.
	data, _ := json.Marshal(problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail})
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
