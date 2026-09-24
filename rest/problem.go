package rest

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/tyr-go/tyr"
)

// problem is a problem details object of RFC 9457 with the members of tyr.
type problem struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Detail  string         `json:"detail,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Errors  tyr.Violations `json:"errors,omitempty"`
	Details any            `json:"details,omitempty"`
}

// WriteError writes err as REST writes the error of an operation: as
// application/problem+json with the status of its kind, for handlers
// outside operations. An err that contains a [tyr.Error] is written as is.
// Otherwise, as [tyr.Operation.Call] does for errors no mapper translates,
// a [context.DeadlineExceeded] becomes [tyr.KindDeadlineExceeded], a
// [context.Canceled] becomes [tyr.KindCanceled] if the context of r is
// canceled too, and any other error, a nil *tyr.Error too,
// [tyr.KindInternal] with a generic message. An internal error, or one of
// a kind rest doesn't know, reaches the client as "internal error" only,
// and WriteError logs it to [slog.Default] with the context of r, with its
// message, cause and details. WriteError doesn't add the WWW-Authenticate
// of [Challenge] to a 401. It panics if err is nil.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		panic("rest: WriteError: nil error")
	}
	e, ok := errors.AsType[*tyr.Error](err)
	switch {
	case ok && e == nil: // a nil *tyr.Error, returned as an error by mistake
		e = tyr.Internal("internal error").WithCause(errors.New("rest: a nil *tyr.Error was written as an error"))
	case !ok && errors.Is(err, context.DeadlineExceeded):
		e = tyr.DeadlineExceeded("deadline exceeded").WithCause(err)
	case !ok && errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled):
		e = tyr.Canceled("canceled").WithCause(err)
	case !ok:
		e = tyr.Internal("internal error").WithCause(err)
	}
	if internal(e.Kind) {
		args := []any{"err", e}
		if e.Details != nil {
			args = append(args, "details", e.Details)
		}
		slog.Default().ErrorContext(r.Context(), "rest: internal error", args...)
	}
	writeError(r.Context(), slog.Default(), w, e, nil)
}

// WriteProblem writes a problem of the HTTP request itself, as
// application/problem+json without a kind, like the 413 and 415 of
// [Mount]: {"type":"about:blank","title":"Forbidden","status":403}.
// It panics unless status is a 4xx or 5xx one.
func WriteProblem(w http.ResponseWriter, status int) {
	if status < 400 || status > 599 {
		panic(fmt.Sprintf("rest: WriteProblem(%d): want a 4xx or 5xx status", status))
	}
	writeProblem(context.Background(), slog.Default(), w, problem{Status: status})
}

// writeError sends e as a problem, or an internal error if e is nil. An
// internal error, or one of a kind rest doesn't know, is sent without its
// message and details, which are for the logs. A 401 gets a
// WWW-Authenticate header per challenge. logger logs details that can't be
// encoded.
func writeError(ctx context.Context, logger *slog.Logger, w http.ResponseWriter, e *tyr.Error, challenges []string) {
	p := problem{Status: http.StatusInternalServerError, Detail: "internal error", Kind: tyr.KindInternal.String()}
	if e != nil && !internal(e.Kind) {
		p.Status, p.Detail, p.Kind = statusOf(e.Kind), e.Message, e.Kind.String()
		if v, ok := e.Details.(tyr.Violations); ok {
			p.Errors = v
		} else {
			p.Details = e.Details
		}
	}
	if p.Status == http.StatusUnauthorized {
		for _, c := range challenges {
			w.Header().Add("WWW-Authenticate", c)
		}
	}
	writeProblem(ctx, logger, w, p)
}

// statusOf returns the HTTP status of errors of kind k.
func statusOf(k tyr.Kind) int {
	switch k {
	case tyr.KindInvalidArgument:
		return http.StatusBadRequest
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
		return statusClientClosedRequest
	case tyr.KindUnavailable:
		return http.StatusServiceUnavailable
	case tyr.KindDeadlineExceeded:
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

// internal reports whether errors of kind k are internal to the clients of
// REST: those of tyr.KindInternal and of kinds rest doesn't know, which get
// 500 Internal Server Error.
func internal(k tyr.Kind) bool {
	return statusOf(k) == http.StatusInternalServerError
}

// statusClientClosedRequest is the status of calls that the client
// canceled, as nginx logs them; net/http has no name for it.
const statusClientClosedRequest = 499

// statusText returns the title of a problem of status: the reason phrase
// of the status, or the one nginx has for 499, which net/http doesn't know.
func statusText(status int) string {
	if status == statusClientClosedRequest {
		return "Client Closed Request"
	}
	return http.StatusText(status)
}

// writeProblem sends p as application/problem+json, with the type and the
// title filled in. Invalid UTF-8, which a message may carry, becomes
// U+FFFD. Details that can't be encoded are a bug of the server: they're
// logged to logger and left out.
func writeProblem(ctx context.Context, logger *slog.Logger, w http.ResponseWriter, p problem) {
	p.Type, p.Title = "about:blank", statusText(p.Status)
	data, err := json.Marshal(p, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		// Only details can fail to encode: send the problem without them.
		logger.ErrorContext(ctx, "rest: encoding error details", "err", err)
		p.Errors, p.Details = nil, nil
		if data, err = json.Marshal(p, jsontext.AllowInvalidUTF8(true)); err != nil {
			// Unreachable: without details, p is strings and a number,
			// and with AllowInvalidUTF8 every string encodes. The line
			// stays so that no later change to problem can bring back a
			// truncated body.
			data = minimalProblem(p.Status)
		}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_, _ = w.Write(data)
}

// minimalProblem returns the problem of status with only the members that
// every problem has, written without the JSON encoder.
func minimalProblem(status int) []byte {
	return []byte(`{"type":"about:blank","title":"` + statusText(status) + `","status":` + strconv.Itoa(status) + `}`)
}
