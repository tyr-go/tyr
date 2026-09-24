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
// Its doc tags describe it in the OpenAPI document.
type problem struct {
	Type    string         `json:"type" doc:"The URI of the problem type: one per kind, or about:blank for a problem of the HTTP request itself."`
	Title   string         `json:"title" doc:"The title of the problem type."`
	Status  int            `json:"status" doc:"The HTTP status."`
	Detail  string         `json:"detail,omitempty" doc:"The message of the error."`
	Kind    string         `json:"kind,omitempty" doc:"The kind of the error. A problem of the HTTP request itself has none."`
	Errors  tyr.Violations `json:"errors,omitempty" doc:"The fields that failed validation, with invalid_argument."`
	Details any            `json:"details,omitempty" doc:"The details of an error of another kind."`
}

// blank returns the problem of an HTTP request itself, of status, with
// detail: its type is about:blank, which means nothing beyond the status,
// and its title is the reason phrase of the status.
func blank(status int, detail string) problem {
	return problem{Type: "about:blank", Title: statusText(status), Status: status, Detail: detail}
}

// WriteError writes err as REST writes the error of an operation: as
// application/problem+json with the type, the title and the status of its
// kind, for handlers outside operations. opts are those of [Mount]: given
// the same options, WriteError writes the types of [ProblemTypes] and the
// WWW-Authenticate challenges of [Challenge] as the operations do.
//
// An err that contains a [tyr.Error] is written as is. Otherwise, as
// [tyr.Operation.Call] does for errors no mapper translates, a
// [context.DeadlineExceeded] becomes [tyr.KindDeadlineExceeded], a
// [context.Canceled] becomes [tyr.KindCanceled] if the context of r is
// canceled too, and any other error, a nil *tyr.Error too,
// [tyr.KindInternal] with a generic message. An internal error, or one of
// a kind rest doesn't know, reaches the client as "internal error" only,
// and WriteError logs it to [slog.Default] with the context of r, with its
// message, cause and details. WriteError panics if err or an option is nil.
func WriteError(w http.ResponseWriter, r *http.Request, err error, opts ...MountOption) {
	if err == nil {
		panic("rest: WriteError: nil error")
	}
	m := newMount("WriteError", opts)
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
	writeError(r.Context(), slog.Default(), w, e, m)
}

// WriteProblem writes a problem of the HTTP request itself, as
// application/problem+json without a kind, like the 413 and 415 of
// [Mount]: {"type":"about:blank","title":"Forbidden","status":403}. Its
// type, about:blank, means nothing beyond the status. WriteProblem panics
// unless status is a 4xx or 5xx one.
func WriteProblem(w http.ResponseWriter, status int) {
	if status < 400 || status > 599 {
		panic(fmt.Sprintf("rest: WriteProblem(%d): want a 4xx or 5xx status", status))
	}
	writeProblem(context.Background(), slog.Default(), w, blank(status, ""))
}

// writeError sends e as the problem of its kind, or an internal error if e
// is nil, as m configures. An internal error, or one of a kind rest doesn't
// know, is sent without its message and details, which are for the logs. A
// 401 gets a WWW-Authenticate header per challenge of m. logger logs
// details that can't be encoded.
func writeError(ctx context.Context, logger *slog.Logger, w http.ResponseWriter, e *tyr.Error, m *mount) {
	k, detail := tyr.KindInternal, "internal error"
	if e != nil && !internal(e.Kind) {
		k, detail = e.Kind, e.Message
	}
	_, title := kindType(k)
	p := problem{Type: m.problemType(k), Title: title, Status: statusOf(k), Detail: detail, Kind: k.String()}
	if k != tyr.KindInternal {
		if v, ok := e.Details.(tyr.Violations); ok {
			p.Errors = v
		} else {
			p.Details = e.Details
		}
	}
	if p.Status == http.StatusUnauthorized {
		for _, c := range m.challenges {
			w.Header().Add("WWW-Authenticate", c)
		}
	}
	writeProblem(ctx, logger, w, p)
}

// defaultProblemTypes is the base of the types of problems without
// ProblemTypes: the documentation of package tyr, where the constant of
// every kind has an anchor.
const defaultProblemTypes = "https://pkg.go.dev/github.com/tyr-go/tyr#"

// problemType returns the type of the problems of kind k, which rest
// knows: the base of ProblemTypes with the name of k or, by default, the
// documentation of k.
func (m *mount) problemType(k tyr.Kind) string {
	if m.problemBase != "" {
		return m.problemBase + k.String()
	}
	constant, _ := kindType(k)
	return defaultProblemTypes + constant
}

// kindType returns the name of the constant of kind k in package tyr, whose
// documentation is the default type of the problems of k, and the title of
// that type. A kind rest doesn't know is internal.
func kindType(k tyr.Kind) (constant, title string) {
	switch k {
	case tyr.KindInvalidArgument:
		return "KindInvalidArgument", "Invalid Argument"
	case tyr.KindUnauthenticated:
		return "KindUnauthenticated", "Unauthenticated"
	case tyr.KindPermissionDenied:
		return "KindPermissionDenied", "Permission Denied"
	case tyr.KindNotFound:
		return "KindNotFound", "Not Found"
	case tyr.KindAlreadyExists:
		return "KindAlreadyExists", "Already Exists"
	case tyr.KindFailedPrecondition:
		return "KindFailedPrecondition", "Failed Precondition"
	case tyr.KindResourceExhausted:
		return "KindResourceExhausted", "Resource Exhausted"
	case tyr.KindCanceled:
		return "KindCanceled", "Canceled"
	case tyr.KindUnavailable:
		return "KindUnavailable", "Unavailable"
	case tyr.KindDeadlineExceeded:
		return "KindDeadlineExceeded", "Deadline Exceeded"
	}
	return "KindInternal", "Internal Error"
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

// writeProblem sends p as application/problem+json. Invalid UTF-8, which a
// message may carry, becomes U+FFFD. Details that can't be encoded are a
// bug of the server: they're logged to logger and left out.
func writeProblem(ctx context.Context, logger *slog.Logger, w http.ResponseWriter, p problem) {
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
			data = minimalProblem(p)
		}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_, _ = w.Write(data)
}

// minimalProblem returns p with only the members that every problem has,
// written without the JSON encoder.
func minimalProblem(p problem) []byte {
	b := []byte(`{"type":`)
	b, _ = jsontext.AppendQuote(b, p.Type) // invalid UTF-8 becomes U+FFFD, as above
	b = append(b, `,"title":`...)
	b, _ = jsontext.AppendQuote(b, p.Title)
	b = append(b, `,"status":`...)
	b = strconv.AppendInt(b, int64(p.Status), 10)
	return append(b, '}')
}
