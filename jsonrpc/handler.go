package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonreq"
)

// handler is the http.Handler that Handler returns.
type handler struct {
	api *tyr.API                  // logs with its Logger
	ops map[string]*tyr.Operation // by name
	config
}

// call is a request object, checked.
type call struct {
	id     jsontext.Value // nil for a notification
	op     *tyr.Operation // nil if err is set
	params jsontext.Value // nil for a zero request
	err    *errorObject   // why the call can't be made, if it can't
}

// outcome is what the call of an operation returned, with the context it
// ran in, for the logs.
type outcome struct {
	ctx context.Context
	res any
	err error // nil or a *tyr.Error
}

// null is the id of a response to a request object whose id can't be told.
var null = jsontext.Value("null")

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.record(r, nil)
		w.Header().Set("Allow", http.MethodPost)
		writeProblem(w, http.StatusMethodNotAllowed, "")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.limit))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		h.record(r, nil)
		writeProblem(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is larger than %d bytes", h.limit))
		return
	}
	if len(body) > 0 && !jsonreq.IsJSON(r.Header.Get("Content-Type")) {
		h.record(r, nil)
		writeProblem(w, http.StatusUnsupportedMediaType, "request body must be JSON: application/json or a +json type")
		return
	}

	// A body that the client didn't send in full isn't valid JSON either.
	v := jsontext.Value(body)
	if err != nil || !v.IsValid() {
		h.record(r, nil)
		h.write(r.Context(), w, failure(parseError()))
		return
	}
	if v.Kind() != '[' {
		c := h.parse(v)
		h.record(r, c.op)
		var o outcome
		if c.op != nil {
			o = h.call(r.Context(), c)
		}
		if res := h.respond(c, o); res != nil {
			h.write(r.Context(), w, res)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.record(r, nil)
	var batch []jsontext.Value
	_ = json.Unmarshal(body, &batch) // it's a valid array
	switch {
	case len(batch) == 0:
		h.write(r.Context(), w, failure(invalidRequest()))
		return
	case len(batch) > h.maxBatch:
		e := invalidRequest()
		e.Message += fmt.Sprintf(": batch is larger than %d calls", h.maxBatch)
		h.write(r.Context(), w, failure(e))
		return
	}
	calls := make([]call, len(batch))
	for i, v := range batch {
		calls[i] = h.parse(v)
	}
	outcomes := h.callAll(r.Context(), calls)
	var responses []*response
	for i, c := range calls {
		if res := h.respond(c, outcomes[i]); res != nil {
			responses = append(responses, res)
		}
	}
	if len(responses) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.write(r.Context(), w, responses)
}

// record records the route of r and op in the RequestInfo of r, if there
// is one, for middleware above, such as an access log.
func (h *handler) record(r *http.Request, op *tyr.Operation) {
	if info, ok := tyr.RequestInfoFrom(r.Context()); ok {
		info.Record(r.Pattern, op)
	}
}

// parse checks v as a request object and finds its operation.
func (h *handler) parse(v jsontext.Value) call {
	var req struct {
		JSONRPC jsontext.Value `json:"jsonrpc"`
		Method  jsontext.Value `json:"method"`
		Params  jsontext.Value `json:"params"`
		ID      jsontext.Value `json:"id"`
	}
	// Any valid JSON object decodes: the members are raw values.
	if v.Kind() != '{' || json.Unmarshal(v, &req) != nil {
		return call{id: null, err: invalidRequest()}
	}
	var jsonrpc, method string
	ok := json.Unmarshal(req.JSONRPC, &jsonrpc) == nil && jsonrpc == version &&
		req.Method.Kind() == '"' && json.Unmarshal(req.Method, &method) == nil
	c := call{id: req.ID}
	switch req.ID.Kind() {
	case 0, '"', '0', 'n': // none, a string, a number or null
	default:
		c.id, ok = null, false
	}
	switch req.Params.Kind() {
	case 0, 'n': // none or null
	case '[':
		if !isEmptyArray(req.Params) {
			c.params = req.Params // by position: the operation's decode rejects it
		}
	case '{':
		c.params = req.Params
	default:
		ok = false
	}

	if !ok {
		if c.id == nil {
			c.id = null // an invalid request object is no notification
		}
		c.params, c.err = nil, invalidRequest()
		return c
	}
	if c.op = h.ops[method]; c.op == nil {
		c.params, c.err = nil, methodNotFound()
	}
	return c
}

// isEmptyArray reports whether v, a valid JSON array, has no elements.
func isEmptyArray(v jsontext.Value) bool {
	var elems []jsontext.Value
	return json.Unmarshal(v, &elems) == nil && len(elems) == 0
}

// call calls the operation of c with its params.
func (h *handler) call(ctx context.Context, c call) outcome {
	// The operation stays in the context after the call, for the logs.
	ctx = tyr.WithOperation(ctx, c.op)
	res, err := c.op.Call(ctx, func(dst any) error {
		if c.params == nil {
			return nil
		}
		return jsonreq.Unmarshal(c.params, dst)
	})
	return outcome{ctx: ctx, res: res, err: err}
}

// callAll calls the operations of calls, at most concurrency at a time,
// and returns their outcomes in the order of calls. Once ctx is done, as
// when the client goes away, the calls that haven't started fail as
// canceled instead.
//
// The calls are made by up to concurrency workers, each taking the next
// call, and the goroutine of the request is one of them: goroutines start
// as a batch has calls for them, not one per call. A goroutine started by
// sync.WaitGroup.Go must not panic, so a panic of a call, which only
// http.ErrAbortHandler can be (Call recovers the others), goes on in the
// goroutine of the request, once the calls are done.
func (h *handler) callAll(ctx context.Context, calls []call) []outcome {
	outcomes := make([]outcome, len(calls))
	var (
		next     atomic.Int64 // the index of the next call to make
		wg       sync.WaitGroup
		mu       sync.Mutex
		panicked any
	)
	work := func() {
		defer func() {
			if v := recover(); v != nil {
				mu.Lock()
				defer mu.Unlock()
				if panicked == nil {
					panicked = v
				}
			}
		}()
		for {
			i := int(next.Add(1)) - 1
			if i >= len(calls) {
				return
			}
			switch {
			case calls[i].op == nil:
			case ctx.Err() != nil:
				outcomes[i] = canceled(ctx)
			default:
				outcomes[i] = h.call(ctx, calls[i])
			}
		}
	}
	runnable := 0
	for _, c := range calls {
		if c.op != nil {
			runnable++
		}
	}
	for range min(concurrency, runnable) - 1 {
		wg.Go(work)
	}
	work()
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
	return outcomes
}

// canceled is the outcome of a call that didn't start because ctx is done.
func canceled(ctx context.Context) outcome {
	return outcome{ctx: ctx, err: tyr.Canceled("canceled").WithCause(ctx.Err())}
}
