package jsonrpc_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
)

// thing is the request and the result of things.echo.
type thing struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// echoAPI returns an API with things.echo, which returns its request, and
// things.check, which requires a code.
func echoAPI() *tyr.API {
	api := newAPI()
	api.Handle("things.echo", func(ctx context.Context, req thing) (thing, error) {
		return req, nil
	})
	api.Handle("things.check", func(ctx context.Context, req struct {
		Code string `json:"code" validate:"required"`
	}) (struct{}, error) {
		return struct{}{}, nil
	})
	return api
}

// callOf returns a request object that calls method with params, "" for
// none, and id, "" for a notification.
func callOf(method, params, id string) string {
	s := `{"jsonrpc":"2.0","method":"` + method + `"`
	if params != "" {
		s += `,"params":` + params
	}
	if id != "" {
		s += `,"id":` + id
	}
	return s + "}"
}

func TestParams(t *testing.T) {
	h := jsonrpc.Handler(echoAPI())
	const (
		zero    = `{"jsonrpc":"2.0","result":{"code":"","count":0},"id":1}`
		invalid = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":1}`
	)
	tests := []struct {
		name   string
		method string // things.echo if empty
		params string // "" for none
		want   string
	}{
		{name: "none", want: zero},
		// Clients send these for methods without arguments.
		{name: "null", params: `null`, want: zero},
		{name: "empty array", params: `[]`, want: zero},
		{name: "empty array with spaces", params: `[ ]`, want: zero},
		{name: "empty object", params: `{}`, want: zero},
		{name: "by name", params: `{"code":"go","count":2}`, want: `{"jsonrpc":"2.0","result":{"code":"go","count":2},"id":1}`},
		{
			name: "by position", params: `["go",2]`,
			want: `{"jsonrpc":"2.0","error":{"code":-32602,"message":"validation failed","data":{"kind":"invalid_argument","details":[{"pointer":"","detail":"must be an object"}]}},"id":1}`,
		},
		{
			// As in the body of a REST request.
			name: "value of a wrong type", params: `{"count":"many"}`,
			want: `{"jsonrpc":"2.0","error":{"code":-32602,"message":"validation failed","data":{"kind":"invalid_argument","details":[{"pointer":"/count","detail":"must be an integer"}]}},"id":1}`,
		},
		{
			name: "validate tags", method: "things.check", params: `{}`,
			want: `{"jsonrpc":"2.0","error":{"code":-32602,"message":"validation failed","data":{"kind":"invalid_argument","details":[{"pointer":"/code","detail":"is required"}]}},"id":1}`,
		},
		{name: "string", params: `"go"`, want: invalid},
		{name: "number", params: `1`, want: invalid},
		{name: "boolean", params: `true`, want: invalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := "things.echo"
			if tt.method != "" {
				method = tt.method
			}
			if rec := post(h, callOf(method, tt.params, "1")); rec.Code != http.StatusOK || rec.Body.String() != tt.want {
				t.Errorf("response = %d %s, want 200 %s", rec.Code, rec.Body, tt.want)
			}
		})
	}
}

func TestIDs(t *testing.T) {
	h := jsonrpc.Handler(echoAPI())
	tests := []struct {
		id   string
		want string // the id of the response
	}{
		{`"abc"`, `"abc"`},
		{`7`, `7`},
		{`1.50`, `1.50`}, // as it is, not as a float64
		{`12345678901234567890123`, `12345678901234567890123`},
		{`null`, `null`},
		// Not a valid id: the request object is invalid, and its id can't
		// be told.
		{`true`, `null`},
		{`{"n":1}`, `null`},
		{`[1]`, `null`},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			var want string
			if strings.HasPrefix(tt.id, "t") || strings.HasPrefix(tt.id, "{") || strings.HasPrefix(tt.id, "[") {
				want = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":null}`
			} else {
				want = `{"jsonrpc":"2.0","result":{"code":"","count":0},"id":` + tt.want + `}`
			}
			if rec := post(h, callOf("things.echo", "", tt.id)); rec.Body.String() != want {
				t.Errorf("response = %s, want %s", rec.Body, want)
			}
		})
	}
}

func TestInvalidRequests(t *testing.T) {
	h := jsonrpc.Handler(echoAPI())
	const (
		invalid  = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":1}`
		notFound = `{"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found"},"id":1}`
	)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no jsonrpc", `{"method":"things.echo","id":1}`, invalid},
		{"another version", `{"jsonrpc":"1.0","method":"things.echo","id":1}`, invalid},
		{"version as a number", `{"jsonrpc":2.0,"method":"things.echo","id":1}`, invalid},
		{"no method", `{"jsonrpc":"2.0","id":1}`, invalid},
		{"null method", `{"jsonrpc":"2.0","method":null,"id":1}`, invalid},
		{"method as a number", `{"jsonrpc":"2.0","method":1,"id":1}`, invalid},
		// Names are case-sensitive: these members aren't jsonrpc and method.
		{"names in upper case", `{"JSONRPC":"2.0","METHOD":"things.echo","id":1}`, invalid},
		{"not an object", `"things.echo"`, `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":null}`},
		{"method in upper case", `{"jsonrpc":"2.0","method":"THINGS.ECHO","id":1}`, notFound},
		// Reserved by the specification; rpc.discover will be OpenRPC.
		{"rpc.discover", `{"jsonrpc":"2.0","method":"rpc.discover","id":1}`, notFound},
		// An invalid request object gets a response even without an id.
		{"invalid notification", `{"jsonrpc":"2.0","method":"things.echo","params":"go"}`, `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":null}`},
		// A duplicate name is invalid JSON to json/v2.
		{"duplicate name", `{"jsonrpc":"2.0","method":"things.echo","method":"things.check","id":1}`, `{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error"},"id":null}`},
		{"invalid UTF-8", "{\"jsonrpc\":\"2.0\",\"method\":\"things.\xff\",\"id\":1}", `{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error"},"id":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := post(h, tt.body); rec.Code != http.StatusOK || rec.Body.String() != tt.want {
				t.Errorf("response = %d %s, want 200 %s", rec.Code, rec.Body, tt.want)
			}
		})
	}
}

// expiry is a detail of an error that isn't Violations.
type expiry struct {
	ExpiredAt string `json:"expired_at"`
}

func TestErrors(t *testing.T) {
	var v tyr.Violations
	v.Add("code", "only a-z, 0-9 and '-'")
	tests := []struct {
		name string
		err  error
	}{
		{"invalid argument", tyr.InvalidArgument("code is too long")},
		{"violations", v.Err()},
		{"unauthenticated", tyr.Unauthenticated("log in first")},
		{"permission denied", tyr.PermissionDenied("admins only")},
		{"not found", tyr.NotFound("link %q not found", "go")},
		{"already exists", tyr.AlreadyExists("code is taken")},
		{"failed precondition", tyr.FailedPrecondition("link expired").WithDetails(expiry{ExpiredAt: "2026-01-01"})},
		{"resource exhausted", tyr.ResourceExhausted("too many links")},
		{"canceled", tyr.Canceled("stopped")},
		{"unavailable", tyr.Unavailable("try again later")},
		{"deadline exceeded", tyr.DeadlineExceeded("took too long")},
		// The client learns nothing of these: the API logs them.
		{"internal", tyr.Internal("storage is read-only").WithDetails(expiry{})},
		{"unmapped", errors.New("db: connection refused")},
		{"unknown kind", &tyr.Error{Kind: tyr.Kind(42), Message: "odd"}},
		{"invalid UTF-8 in the message", tyr.NotFound("link %s not found", "\xff")},
		{"invalid UTF-8 in details", tyr.NotFound("link not found").WithDetails("\xff")},
		{"unencodable details", tyr.Unavailable("try again later").WithDetails(make(chan int))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newAPI()
			api.Handle("links.get", func(ctx context.Context, req struct{}) (struct{}, error) {
				return struct{}{}, tt.err
			})
			rec := post(jsonrpc.Handler(api), callOf("links.get", "", "1"))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			golden(t, "error_"+tt.name, rec.Body.Bytes())
		})
	}
}

func TestEncodingErrorsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil)))))
	api.Handle("links.result", func(ctx context.Context, req struct{}) (chan int, error) {
		return make(chan int), nil
	})
	api.Handle("links.details", func(ctx context.Context, req struct{}) (struct{}, error) {
		return struct{}{}, tyr.Unavailable("try again later").WithDetails(make(chan int))
	})
	h := jsonrpc.Handler(api)

	tests := []struct {
		method  string
		want    string // the response
		wantMsg string // of the record
	}{
		{"links.result", `{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"},"id":1}`, "jsonrpc: encoding the result"},
		{"links.details", `{"jsonrpc":"2.0","error":{"code":503,"message":"try again later","data":{"kind":"unavailable"}},"id":1}`, "jsonrpc: encoding error details"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			buf.Reset()
			req := httptest.NewRequest("POST", "/rpc", strings.NewReader(callOf(tt.method, "", "1")))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(tyr.WithRequestID(req.Context(), "req-1"))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Body.String() != tt.want {
				t.Errorf("response = %s, want %s", rec.Body, tt.want)
			}

			// The API's logger gets the record, with the context of the
			// call, which carries the operation.
			var got map[string]any
			for line := range bytes.Lines(buf.Bytes()) {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatalf("unmarshal %q: %v", line, err)
				}
				if record["msg"] == tt.wantMsg {
					got = record
				}
			}
			if got["level"] != "ERROR" || got["request_id"] != "req-1" || got["operation"] != tt.method ||
				!strings.Contains(fmt.Sprint(got["err"]), "chan int") {
				t.Errorf("logged %v, want an error %q with request_id req-1 and operation %s", got, tt.wantMsg, tt.method)
			}
		})
	}
}

func TestHTTP(t *testing.T) {
	h := jsonrpc.Handler(echoAPI(), jsonrpc.MaxBodyBytes(64))
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantCode    int
		wantType    string
		golden      string // of the body, if any
	}{
		{name: "call", method: "POST", contentType: "application/json", body: callOf("things.echo", "", "1"), wantCode: http.StatusOK, wantType: "application/json"},
		{name: "+json type", method: "POST", contentType: "application/json-rpc+json; charset=utf-8", body: callOf("things.echo", "", "1"), wantCode: http.StatusOK, wantType: "application/json"},
		{name: "notification", method: "POST", contentType: "application/json", body: callOf("things.echo", "", ""), wantCode: http.StatusNoContent},
		{name: "empty body", method: "POST", wantCode: http.StatusOK, wantType: "application/json", golden: "http_empty_body"},
		{name: "GET", method: "GET", wantCode: http.StatusMethodNotAllowed, wantType: "application/problem+json", golden: "http_method_not_allowed"},
		{name: "not JSON", method: "POST", contentType: "text/plain", body: "{}", wantCode: http.StatusUnsupportedMediaType, wantType: "application/problem+json", golden: "http_unsupported_media_type"},
		{name: "no type", method: "POST", body: "{}", wantCode: http.StatusUnsupportedMediaType, wantType: "application/problem+json"},
		{
			// The limit is on the whole request, with all the calls of a
			// batch.
			name: "too large", method: "POST", contentType: "application/json",
			body:     "[" + callOf("things.echo", "", "1") + "," + callOf("things.echo", "", "2") + "]",
			wantCode: http.StatusRequestEntityTooLarge, wantType: "application/problem+json", golden: "http_too_large",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/rpc", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode || rec.Header().Get("Content-Type") != tt.wantType {
				t.Errorf("response = %d %s, want %d %s", rec.Code, rec.Header().Get("Content-Type"), tt.wantCode, tt.wantType)
			}
			if tt.wantCode == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "POST" {
				t.Errorf("Allow = %q, want POST", rec.Header().Get("Allow"))
			}
			if tt.wantCode == http.StatusNoContent && rec.Body.Len() != 0 {
				t.Errorf("body = %q, want none", rec.Body)
			}
			if tt.golden != "" {
				golden(t, tt.golden, rec.Body.Bytes())
			}
		})
	}
}

// batchOf returns a batch of n calls of method, with the ids 0 to n-1 and
// params {"count": id}.
func batchOf(method string, n int) string {
	calls := make([]string, n)
	for i := range n {
		calls[i] = callOf(method, fmt.Sprintf(`{"count":%d}`, i), fmt.Sprint(i))
	}
	return "[" + strings.Join(calls, ",") + "]"
}

func TestMaxBatch(t *testing.T) {
	var calls atomic.Int32
	api := newAPI()
	api.Handle("things.count", func(ctx context.Context, req thing) (int, error) {
		calls.Add(1)
		return req.Count, nil
	})
	h := jsonrpc.Handler(api, jsonrpc.MaxBatch(2))

	// Over the limit, no call runs.
	const tooLarge = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request: batch is larger than 2 calls"},"id":null}`
	if rec := post(h, batchOf("things.count", 3)); rec.Body.String() != tooLarge || calls.Load() != 0 {
		t.Errorf("a batch of 3 got %s after %d calls, want %s after none", rec.Body, calls.Load(), tooLarge)
	}
	const two = `[{"jsonrpc":"2.0","result":0,"id":0},{"jsonrpc":"2.0","result":1,"id":1}]`
	if rec := post(h, batchOf("things.count", 2)); rec.Body.String() != two {
		t.Errorf("a batch of 2 got %s, want %s", rec.Body, two)
	}
}

func TestBatchConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var running, peak atomic.Int32
		api := newAPI()
		api.Handle("things.slow", func(ctx context.Context, req thing) (int, error) {
			n := running.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(time.Second)
			running.Add(-1)
			return req.Count, nil
		})
		h := jsonrpc.Handler(api)

		start := time.Now()
		rec := post(h, batchOf("things.slow", 20))

		// 8 at a time: 20 calls take three rounds.
		if took := time.Since(start); took != 3*time.Second || peak.Load() != 8 {
			t.Errorf("20 calls took %v, at most %d at a time; want 3s, 8", took, peak.Load())
		}
		// The responses come in the order of the calls.
		var responses []struct {
			Result int `json:"result"`
			ID     int `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &responses); err != nil || len(responses) != 20 {
			t.Fatalf("responses = %s, %v; want 20", rec.Body, err)
		}
		for i, r := range responses {
			if r.ID != i || r.Result != i {
				t.Errorf("response %d = %+v, want id and result %d", i, r, i)
			}
		}
	})
}

// waitAPI returns an API with things.wait, which waits for its context to
// be done, and a channel that gets a value as each call starts.
func waitAPI() (*tyr.API, chan struct{}) {
	started := make(chan struct{}, 20)
	api := newAPI()
	api.Handle("things.wait", func(ctx context.Context, req thing) (struct{}, error) {
		started <- struct{}{}
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	return api, started
}

func TestBatchCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api, started := waitAPI()
		h := jsonrpc.Handler(api)
		done := make(chan struct{})
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(done)
			h.ServeHTTP(w, r)
		}))

		// The client of the test server sends requests to any host to it.
		client := srv.Client()
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, "POST", "http://example.com/rpc", strings.NewReader(batchOf("things.wait", 20)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		go func() {
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}()
		// The first 8 calls wait for their context, the others for their
		// turn.
		synctest.Wait()
		if n := len(started); n != 8 {
			t.Fatalf("%d calls started, want 8", n)
		}

		// The client goes away: the calls that run return, and the others
		// don't start.
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler didn't return after the client went away")
		}
		if n := len(started); n != 8 {
			t.Errorf("%d calls started, want 8", n)
		}
	})
}

func TestBatchCanceledLeaksNothing(t *testing.T) {
	api, started := waitAPI()
	h := jsonrpc.Handler(api)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for range 8 {
			<-started
		}
		cancel()
	}()
	req := httptest.NewRequestWithContext(ctx, "POST", "/rpc", strings.NewReader(batchOf("things.wait", 20)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Every call is canceled, those that didn't start too, and no call
	// started after the first 8.
	if n := strings.Count(rec.Body.String(), `"code":499`); n != 20 || len(started) != 0 {
		t.Errorf("%d canceled calls of 20, %d more started; want 20, none: %s", n, len(started), rec.Body)
	}
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutineleak").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile.String(), "jsonrpc") {
		t.Errorf("leaked goroutines:\n%s", profile.String())
	}
}

func TestBatchTimeout(t *testing.T) {
	// Each call has the timeout of its operation from the time it starts:
	// 16 calls on 8 workers time out in two rounds.
	synctest.Test(t, func(t *testing.T) {
		api := newAPI()
		var mu sync.Mutex
		var left []time.Duration // from the start of the batch to the deadline of a call
		start := time.Now()
		api.Handle("things.wait", func(ctx context.Context, req thing) (struct{}, error) {
			deadline, _ := ctx.Deadline()
			mu.Lock()
			left = append(left, deadline.Sub(start))
			mu.Unlock()
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		}, tyr.Timeout(time.Second))

		rec := post(jsonrpc.Handler(api), batchOf("things.wait", 16))
		if took := time.Since(start); took != 2*time.Second {
			t.Errorf("the batch took %v, want 2s", took)
		}
		const timedOut = `"error":{"code":504,"message":"deadline exceeded","data":{"kind":"deadline_exceeded"}}`
		if n := strings.Count(rec.Body.String(), timedOut); n != 16 {
			t.Errorf("%d calls timed out of 16: %s", n, rec.Body)
		}
		slices.Sort(left)
		want := slices.Concat(slices.Repeat([]time.Duration{time.Second}, 8), slices.Repeat([]time.Duration{2 * time.Second}, 8))
		if !slices.Equal(left, want) {
			t.Errorf("the calls had deadlines at %v, want %v", left, want)
		}
	})
}

func TestBatchDeadline(t *testing.T) {
	// A deadline of the request bounds the batch as a whole: the calls
	// that run get it, and those that haven't started by then fail with it
	// too, rather than as canceled by the client.
	synctest.Test(t, func(t *testing.T) {
		api, started := waitAPI()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, "POST", "/rpc", strings.NewReader(batchOf("things.wait", 20)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		jsonrpc.Handler(api).ServeHTTP(rec, req)

		const timedOut = `"error":{"code":504,"message":"deadline exceeded","data":{"kind":"deadline_exceeded"}}`
		if n := strings.Count(rec.Body.String(), timedOut); n != 20 || len(started) != 8 {
			t.Errorf("%d calls timed out of 20 after %d started, want 20 after 8: %s", n, len(started), rec.Body)
		}
	})
}

func TestTimeoutLeaksNothing(t *testing.T) {
	// The timeouts of the calls of a batch and the deadline of a request
	// leave no goroutine behind.
	api := newAPI()
	api.Handle("things.wait", func(ctx context.Context, req thing) (struct{}, error) {
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	}, tyr.Timeout(time.Millisecond))
	h := jsonrpc.Handler(api)
	if rec := post(h, batchOf("things.wait", 20)); strings.Count(rec.Body.String(), `"code":504`) != 20 {
		t.Errorf("timeouts: %s, want 20 calls timed out", rec.Body)
	}

	waiting, _ := waitAPI()
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", "/rpc", strings.NewReader(batchOf("things.wait", 20)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	jsonrpc.Handler(waiting).ServeHTTP(rec, req)
	if strings.Count(rec.Body.String(), `"code":504`) != 20 {
		t.Errorf("a deadline of the request: %s, want 20 calls timed out", rec.Body)
	}

	var profile bytes.Buffer
	if err := pprof.Lookup("goroutineleak").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(profile.String(), "jsonrpc") {
		t.Errorf("leaked goroutines:\n%s", profile.String())
	}
}

func TestBatchAbort(t *testing.T) {
	// A call that panics with http.ErrAbortHandler aborts the response,
	// from the goroutine of the request: a panic in the goroutine of a call
	// would end the program.
	api := newAPI()
	api.Handle("things.abort", func(ctx context.Context, req thing) (thing, error) {
		panic(http.ErrAbortHandler)
	})
	api.Handle("things.echo", func(ctx context.Context, req thing) (thing, error) {
		return req, nil
	})
	h := jsonrpc.Handler(api)
	rec := httptest.NewRecorder()
	body := "[" + callOf("things.echo", "", "1") + "," + callOf("things.abort", "", "2") + "]"
	req := httptest.NewRequest("POST", "/rpc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if got := panicValue(func() { h.ServeHTTP(rec, req) }); got != http.ErrAbortHandler {
		t.Errorf("ServeHTTP() panicked with %v, want http.ErrAbortHandler", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %s, want none", rec.Body)
	}
}

func TestBatchPanic(t *testing.T) {
	// Call recovers other panics: the call fails, the batch goes on.
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	api.Handle("things.boom", func(ctx context.Context, req thing) (thing, error) {
		panic("boom")
	})
	api.Handle("things.echo", func(ctx context.Context, req thing) (thing, error) {
		return req, nil
	})
	body := "[" + callOf("things.boom", "", "1") + "," + callOf("things.echo", `{"code":"go"}`, "2") + "]"
	const want = `[{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"},"id":1},` +
		`{"jsonrpc":"2.0","result":{"code":"go","count":0},"id":2}]`
	if rec := post(jsonrpc.Handler(api), body); rec.Body.String() != want {
		t.Errorf("response = %s, want %s", rec.Body, want)
	}
	if !strings.Contains(buf.String(), `"msg":"tyr: panic"`) {
		t.Errorf("logged %s, want the panic", buf.Bytes())
	}
}

func TestNotificationFailureIsLogged(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	api.Handle("things.fail", func(ctx context.Context, req thing) (thing, error) {
		return thing{}, errors.New("db: connection refused")
	})
	// No response, but the API logs the failure.
	if rec := post(jsonrpc.Handler(api), callOf("things.fail", "", "")); rec.Code != http.StatusNoContent ||
		!strings.Contains(buf.String(), `"msg":"tyr: operation failed"`) {
		t.Errorf("response = %d, logged %s; want 204 and the failure", rec.Code, buf.Bytes())
	}
}

func TestRequestInfo(t *testing.T) {
	api := echoAPI()
	mux := http.NewServeMux()
	mux.Handle("POST /rpc", jsonrpc.Handler(api))
	var echo *tyr.Operation
	for op := range api.Operations() {
		if op.Name() == "things.echo" {
			echo = op
		}
	}

	tests := []struct {
		name        string
		contentType string
		body        string
		want        *tyr.Operation
	}{
		{"call", "application/json", callOf("things.echo", "", "1"), echo},
		{"notification", "application/json", callOf("things.echo", "", ""), echo},
		{"batch", "application/json", "[" + callOf("things.echo", "", "1") + "]", nil},
		{"missing method", "application/json", callOf("things.nope", "", "1"), nil},
		{"invalid request object", "application/json", `{"jsonrpc":"2.0","method":1,"id":1}`, nil},
		{"invalid JSON", "application/json", `{`, nil},
		{"not JSON", "text/plain", "{}", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, info := tyr.WithRequestInfo(t.Context())
			req := httptest.NewRequestWithContext(ctx, "POST", "/rpc", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			mux.ServeHTTP(httptest.NewRecorder(), req)

			if op, _ := info.Operation(); info.Route() != "POST /rpc" || op != tt.want {
				t.Errorf("recorded %q and %v, want POST /rpc and %v", info.Route(), op, tt.want)
			}
		})
	}
}

func BenchmarkHandler(b *testing.B) {
	// The handler allocates nothing: what's measured is jsonrpc's own.
	res := thing{Code: "go", Count: 1}
	api := tyr.New()
	api.Handle("things.get", func(ctx context.Context, req thing) (thing, error) {
		return res, nil
	})
	api.Seal()
	h := jsonrpc.Handler(api)
	tests := []struct {
		name string
		body string
	}{
		{"call", callOf("things.get", `{"code":"go"}`, "1")},
		{"batch of 10", batchOf("things.get", 10)},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			req := httptest.NewRequest("POST", "/rpc", nil)
			req.Header.Set("Content-Type", "application/json")
			body := strings.NewReader(tt.body)
			req.Body = io.NopCloser(body)
			w := &discard{header: make(http.Header)}
			b.ReportAllocs()
			for b.Loop() {
				clear(w.header)
				body.Reset(tt.body)
				h.ServeHTTP(w, req)
			}
		})
	}
}

// discard is a ResponseWriter that drops the body.
type discard struct {
	header http.Header
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}
