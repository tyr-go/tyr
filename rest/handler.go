package rest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonreq"
	"github.com/tyr-go/tyr/internal/plan"
)

// handler serves one operation.
type handler struct {
	api        *tyr.API // logs with its Logger
	op         *tyr.Operation
	pattern    string // that the handler is mounted at
	binding    *plan.Binding
	headers    *plan.Headers // that results set
	status     int           // of a successful response
	noBody     bool          // for a redirect or a result without JSON members
	limit      int64         // of the request body
	challenges []string      // of the WWW-Authenticate of 401
}

// newHandler returns the handler of op of api at pattern, mounted with m. It
// panics if they don't fit together, as described at Mount.
func newHandler(api *tyr.API, op *tyr.Operation, pattern string, m *mount) *handler {
	if !strings.ContainsAny(pattern, " \t") {
		panicf(op, "pattern %q has no method, e.g. %q", pattern, "GET "+pattern)
	}
	// Let ServeMux check the syntax before the wildcards are looked at.
	handle(http.NewServeMux(), op, pattern, http.NotFoundHandler())

	b, err := plan.NewBinding(op.Req())
	if err != nil {
		panicf(op, "%v", err)
	}
	wildcards := wildcardsOf(pattern)
	for _, name := range wildcards {
		if !slices.ContainsFunc(b.Fields, func(f plan.Field) bool { return f.Source == plan.Path && f.Name == name }) {
			panicf(op, "pattern %q has wildcard {%s}, but %v has no field with path:%q", pattern, name, op.Req(), name)
		}
	}
	for _, f := range b.Fields {
		if f.Source == plan.Path && !slices.Contains(wildcards, f.Name) {
			panicf(op, "field %s has path:%q, but pattern %q has no wildcard {%s}", f.GoName, f.Name, pattern, f.Name)
		}
	}

	res := op.Res()
	headers, err := plan.NewHeaders(res)
	if err != nil {
		panicf(op, "%v", err)
	}
	h := &handler{
		api: api, op: op, pattern: pattern, binding: b, headers: headers,
		status: http.StatusOK, limit: defaultLimit, challenges: m.challenges,
	}
	if h.noBody = plan.NoMembers(res); h.noBody {
		h.status = http.StatusNoContent
	}
	if status, ok := statusKey.Get(op); ok {
		switch {
		case isRedirect(status) && !headers.Has("Location"):
			panicf(op, "Status(%d) is a redirect, but %v has no field with header:%q", status, res, "Location")
		case isRedirect(status):
			h.noBody = true
		case (status == http.StatusNoContent || status == http.StatusResetContent) && !h.noBody:
			panicf(op, "Status(%d) needs a result without JSON members, such as struct{}, not %v", status, res)
		}
		h.status = status
	}
	if limit, ok := limitKey.Get(op); ok {
		h.limit = limit
	}
	return h
}

// wildcardsOf returns the names of the wildcards in a valid pattern, such as
// "code" for "GET /links/{code}"; {$} isn't one.
func wildcardsOf(pattern string) []string {
	var names []string
	for seg := range strings.SplitSeq(pattern[strings.IndexByte(pattern, '/'):], "/") {
		if name, ok := strings.CutPrefix(seg, "{"); ok {
			if name = strings.TrimSuffix(strings.TrimSuffix(name, "}"), "..."); name != "$" {
				names = append(names, name)
			}
		}
	}
	return names
}

// handle registers h on mux. It adds op to a panic of ServeMux, such as one
// over a conflict, since ServeMux points to this package as the place of
// registration.
func handle(mux *http.ServeMux, op *tyr.Operation, pattern string, h http.Handler) {
	defer func() {
		if v := recover(); v != nil {
			panicf(op, "%v", v)
		}
	}()
	mux.Handle(pattern, h)
}

// panicf panics with a message about op.
func panicf(op *tyr.Operation, format string, args ...any) {
	panic(fmt.Sprintf("rest: operation %q: ", op.Name()) + fmt.Sprintf(format, args...))
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// For middleware above, such as an access log, before anything can
	// fail.
	if info, ok := tyr.RequestInfoFrom(r.Context()); ok {
		info.Record(h.pattern, h.op)
	}
	// The operation stays in the context after the call, for the logs.
	ctx := tyr.WithOperation(r.Context(), h.op)
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, h.limit))
	if _, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
		writeProblem(ctx, h.api.Logger(), w, problem{
			Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("request body is larger than %d bytes", h.limit),
		})
		return
	}
	if len(body) > 0 && !jsonreq.IsJSON(r.Header.Get("Content-Type")) {
		writeProblem(ctx, h.api.Logger(), w, problem{
			Status: http.StatusUnsupportedMediaType,
			Detail: "request body must be JSON: application/json or a +json type",
		})
		return
	}

	res, err := h.op.Call(ctx, func(dst any) error {
		if readErr != nil {
			return readErr
		}
		return h.decode(dst, body, r)
	})
	if err != nil {
		e, _ := err.(*tyr.Error) // Call returns only those
		writeError(ctx, h.api.Logger(), w, e, h.challenges)
		return
	}
	h.writeResult(ctx, w, res)
}

// decode fills in dst, a *Req, from the body and then from the path, the
// query and the headers.
func (h *handler) decode(dst any, body []byte, r *http.Request) error {
	if len(body) > 0 {
		if err := jsonreq.Unmarshal(body, dst); err != nil {
			return err
		}
	}

	var query url.Values // parsed on first use
	problems := h.binding.Bind(reflect.ValueOf(dst).Elem(), func(src plan.Source, name string) ([]string, bool) {
		switch src {
		case plan.Path:
			return []string{r.PathValue(name)}, true
		case plan.Query:
			if query == nil {
				query = r.URL.Query()
			}
			values := query[name]
			return values, len(values) > 0
		}
		values := r.Header.Values(name)
		return values, len(values) > 0
	})
	var v tyr.Violations
	for _, p := range problems {
		v.Add(p.Field.JSON, fmt.Sprintf("%s %q: %s", p.Field.Source, p.Field.Name, p.Detail))
	}
	return v.Err()
}

// writeResult sends a successful result: the headers its fields set and,
// unless the response has none, the body. A result that can't be encoded,
// or a redirect without a Location, is a bug of the server: it's logged,
// and the client gets an internal error.
func (h *handler) writeResult(ctx context.Context, w http.ResponseWriter, res any) {
	fail := func(msg string, args ...any) {
		h.api.Logger().ErrorContext(ctx, msg, args...)
		writeError(ctx, h.api.Logger(), w, tyr.Internal("internal error"), nil)
	}
	header, err := h.headers.Of(reflect.ValueOf(res))
	if err != nil {
		fail("rest: encoding a header of the result", "err", err)
		return
	}
	if isRedirect(h.status) && header.Get("Location") == "" {
		fail("rest: a redirect without a Location", "status", h.status)
		return
	}
	var data []byte
	if !h.noBody {
		if data, err = json.Marshal(res); err != nil {
			fail("rest: encoding the result", "err", err)
			return
		}
	}

	maps.Copy(w.Header(), header)
	if !h.noBody {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(h.status)
	_, _ = w.Write(data)
}
