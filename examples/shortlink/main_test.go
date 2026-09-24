package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/examples/shortlink/authz"
	"github.com/tyr-go/tyr/examples/shortlink/contract"
	"github.com/tyr-go/tyr/examples/shortlink/links"
	"github.com/tyr-go/tyr/examples/shortlink/store"
	"github.com/tyr-go/tyr/health"
	"github.com/tyr-go/tyr/inprocess"
	"github.com/tyr-go/tyr/jsonrpc"
)

// The tokens of the admin and of a user of the service under test.
const (
	adminToken = "secret"
	userToken  = "user-secret"
)

// app is the origin whose pages may call the service under test.
const app = "https://app.example.com"

// service is the service under test.
type service struct {
	handler http.Handler // of the server, middleware included
	client  *http.Client // of the test server
	logs    *logs
}

// start starts the service on an in-memory test server. It runs in a
// synctest bubble, whose clock dates links 2000-01-01.
func start(t *testing.T) *service {
	logs := &logs{}
	logger := slog.New(tyr.NewLogHandler(logs))
	api := newAPI(links.New(store.New()), logger)
	callers := map[string]authz.Caller{
		adminToken: {Name: "admin", Roles: []string{"admin"}},
		userToken:  {Name: "user"},
	}
	srv := newServer("", api, callers, []string{app}, health.NewReadiness(), logger)
	client := httptest.NewTestServer(t, srv.Handler).Client()
	// Show redirects to the test instead of following them: the client of
	// the test server sends requests to every host to the service.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &service{handler: srv.Handler, client: client, logs: logs}
}

// rpc returns a client of the service over JSON-RPC, through hc, that
// sends token as a bearer token, if there is one.
func rpc(hc *http.Client, token string) *jsonrpc.Client {
	if token != "" {
		authed := *hc
		authed.Transport = bearer{token: token, next: hc.Transport}
		hc = &authed
	}
	return jsonrpc.NewClient("http://shortlink.example/rpc", hc)
}

// bearer is an http.RoundTripper that sends requests through next with a
// bearer token, as a client of the service does.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// do sends a request, with a JSON body unless body is empty and with the
// header given as pairs of names and values, and returns the response and
// its body.
func (s *service) do(t *testing.T, method, target, body string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://shortlink.example"+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for kv := range slices.Chunk(header, 2) {
		req.Header.Set(kv[0], kv[1])
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the body: %v", method, target, err)
	}
	return resp, string(data)
}

func TestLinks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		const link = `{"code":"go-docs","url":"https://go.dev/doc/","created_at":"2000-01-01T00:00:00Z"}`

		resp, body := s.do(t, "POST", "/links", `{"url":"https://go.dev/doc/","code":"go-docs"}`)
		if resp.StatusCode != http.StatusCreated || body != link || resp.Header.Get("Location") != "/links/go-docs" || resp.Header.Get("X-Request-ID") == "" {
			t.Errorf("create = %d %v %s; want 201 %s with a Location and a request ID", resp.StatusCode, resp.Header, body, link)
		}
		resp, body = s.do(t, "GET", "/links/go-docs", "")
		if resp.StatusCode != http.StatusOK || body != link {
			t.Errorf("get = %d %s, want 200 %s", resp.StatusCode, body, link)
		}
		// The short link itself redirects.
		resp, body = s.do(t, "GET", "/go-docs", "")
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://go.dev/doc/" || body != "" {
			t.Errorf("follow = %d %v %q, want 302 to https://go.dev/doc/ without a body", resp.StatusCode, resp.Header, body)
		}

		// The errors of the store, translated by MapError.
		resp, body = s.do(t, "POST", "/links", `{"url":"https://go.dev","code":"go-docs"}`)
		const taken = `{"type":"/problems/already_exists","title":"Already Exists","status":409,"detail":"code is taken","kind":"already_exists"}`
		if resp.StatusCode != http.StatusConflict || body != taken {
			t.Errorf("create again = %d %s, want 409 %s", resp.StatusCode, body, taken)
		}
		resp, body = s.do(t, "GET", "/links/nope", "")
		const notFound = `{"type":"/problems/not_found","title":"Not Found","status":404,"detail":"link not found","kind":"not_found"}`
		if resp.StatusCode != http.StatusNotFound || body != notFound {
			t.Errorf("get a missing link = %d %s, want 404 %s", resp.StatusCode, body, notFound)
		}
	})
}

func TestRandomCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		resp, body := s.do(t, "POST", "/links", `{"url":"https://go.dev"}`)
		code := regexp.MustCompile(`^\{"code":"([a-z2-7]{7})","url":"https://go.dev",`).FindStringSubmatch(body)
		if resp.StatusCode != http.StatusCreated || code == nil {
			t.Fatalf("create = %d %s, want 201 and a random code", resp.StatusCode, body)
		}
		if resp, body := s.do(t, "GET", "/links/"+code[1], ""); resp.StatusCode != http.StatusOK {
			t.Errorf("get = %d %s, want 200", resp.StatusCode, body)
		}
	})
}

func TestCreateInvalid(t *testing.T) {
	violation := func(pointer, detail string) string {
		return `{"type":"/problems/invalid_argument","title":"Invalid Argument","status":400,"detail":"validation failed",` +
			`"kind":"invalid_argument","errors":[{"pointer":"` + pointer + `","detail":"` + detail + `"}]}`
	}
	tests := []struct {
		name   string
		body   string
		header []string
		status int
		want   string
	}{
		{
			name:   "no URL",
			body:   `{"code":"go-docs"}`,
			status: http.StatusBadRequest,
			want:   violation("/url", "is required"),
		},
		{
			name:   "not a URL",
			body:   `{"url":"go.dev"}`,
			status: http.StatusBadRequest,
			want:   violation("/url", "must be an http or https URL"),
		},
		{
			name:   "not an http URL",
			body:   `{"url":"javascript:alert(1)"}`,
			status: http.StatusBadRequest,
			want:   violation("/url", "must be an http or https URL"),
		},
		{
			name:   "short code",
			body:   `{"url":"https://go.dev","code":"go"}`,
			status: http.StatusBadRequest,
			want:   violation("/code", "must be at least 4 characters"),
		},
		{
			name:   "characters of the code",
			body:   `{"url":"https://go.dev","code":"Go_Dev"}`,
			status: http.StatusBadRequest,
			want:   violation("/code", "only a-z, 0-9 and '-'"),
		},
		{
			name:   "broken JSON",
			body:   `{"url":}`,
			status: http.StatusBadRequest,
			want:   violation("", "invalid JSON at byte offset 7: invalid character '}' at start of value"),
		},
		{
			name:   "not JSON",
			body:   `url=https://go.dev`,
			header: []string{"Content-Type", "application/x-www-form-urlencoded"},
			status: http.StatusUnsupportedMediaType,
			want:   `{"type":"about:blank","title":"Unsupported Media Type","status":415,"detail":"request body must be JSON: application/json or a +json type"}`,
		},
		{
			name:   "too large",
			body:   `{"url":"https://go.dev/?q=` + strings.Repeat("a", 1<<20) + `"}`,
			status: http.StatusRequestEntityTooLarge,
			want:   `{"type":"about:blank","title":"Request Entity Too Large","status":413,"detail":"request body is larger than 1048576 bytes"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resp, body := start(t).do(t, "POST", "/links", tt.body, tt.header...)
				if resp.StatusCode != tt.status || body != tt.want {
					t.Errorf("create = %d %s, want %d %s", resp.StatusCode, body, tt.status, tt.want)
				}
			})
		})
	}
}

func TestCrossOrigin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		// A page of the service itself posts to it.
		if resp, body := s.do(t, "POST", "/links", `{"url":"https://go.dev"}`, "Sec-Fetch-Site", "same-origin"); resp.StatusCode != http.StatusCreated {
			t.Errorf("same-origin create = %d %s, want 201", resp.StatusCode, body)
		}

		// A page of the app asks whether it may post JSON, over REST and
		// JSON-RPC: CORS answers, before the mux.
		for _, target := range []string{"/links", "/rpc"} {
			resp, body := s.do(t, "OPTIONS", target, "", "Origin", app,
				"Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type")
			h := resp.Header
			if resp.StatusCode != http.StatusNoContent || body != "" || h.Get("Access-Control-Allow-Origin") != app ||
				h.Get("Access-Control-Allow-Methods") != "POST" || h.Get("Access-Control-Allow-Headers") != "content-type" ||
				h.Get("Access-Control-Max-Age") != "3600" {
				t.Errorf("preflight of %s = %d %v %q, want 204 with the headers of CORS", target, resp.StatusCode, h, body)
			}
		}
		// Then it posts, from another site: CSRF protection trusts the same
		// origin, and the page may read the link and the request ID.
		resp, body := s.do(t, "POST", "/links", `{"url":"https://go.dev","code":"go-dev"}`, "Origin", app, "Sec-Fetch-Site", "cross-site")
		if h := resp.Header; resp.StatusCode != http.StatusCreated || h.Get("Access-Control-Allow-Origin") != app ||
			h.Get("Access-Control-Expose-Headers") != "Location, X-Request-ID" || h.Get("Vary") != "Origin" {
			t.Errorf("create from the app = %d %v %s, want 201 that the app may read", resp.StatusCode, h, body)
		}

		// A page of another site may do neither. CORS answers a preflight,
		// which no operation has, with a problem of HTTP; CSRF protection
		// denies a request of an operation with a kind, as the operations
		// do.
		const forbidden = `{"type":"about:blank","title":"Forbidden","status":403}`
		resp, body = s.do(t, "OPTIONS", "/links", "", "Origin", "https://evil.example", "Access-Control-Request-Method", "POST")
		if resp.StatusCode != http.StatusForbidden || body != forbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight from another site = %d %v %s, want 403 %s", resp.StatusCode, resp.Header, body, forbidden)
		}
		const denied = `{"type":"/problems/permission_denied","title":"Permission Denied","status":403,"detail":"cross-origin request","kind":"permission_denied"}`
		for _, target := range []string{"/links", "/rpc"} {
			resp, body = s.do(t, "POST", target, `{"url":"https://evil.example"}`, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
			if resp.StatusCode != http.StatusForbidden || body != denied || resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("POST %s from another site = %d %v %s, want 403 %s", target, resp.StatusCode, resp.Header, body, denied)
			}
		}
	})
}

func TestProbes(t *testing.T) {
	// The balancers probe the service past its middleware: the probes get
	// no request ID, and the access log has none of their records.
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		for _, path := range []string{"/health/live", "/health/ready"} {
			resp, body := s.do(t, "GET", path, "")
			if resp.StatusCode != http.StatusOK || body != `{"status":"ok"}` || resp.Header.Get("X-Request-ID") != "" {
				t.Errorf("GET %s = %d %v %s, want 200 {\"status\":\"ok\"} without a request ID", path, resp.StatusCode, resp.Header, body)
			}
		}
		synctest.Wait()
		if got := s.logs.get(); len(got) != 0 {
			t.Errorf("logged %+v, want nothing", got)
		}

		// Another method goes through the middleware to the mux, which has
		// no route for the path: a short link has one segment.
		resp, body := s.do(t, "POST", "/health/ready", "")
		const notFound = `{"type":"about:blank","title":"Not Found","status":404}`
		if resp.StatusCode != http.StatusNotFound || body != notFound || resp.Header.Get("X-Request-ID") == "" {
			t.Errorf("POST /health/ready = %d %v %s, want 404 %s with a request ID", resp.StatusCode, resp.Header, body, notFound)
		}
	})
}

func TestDelete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		if resp, body := s.do(t, "POST", "/links", `{"url":"https://go.dev","code":"golang"}`); resp.StatusCode != http.StatusCreated {
			t.Fatalf("create = %d %s", resp.StatusCode, body)
		}

		// Only the admin deletes links; a client without a token learns how
		// to authenticate.
		resp, body := s.do(t, "DELETE", "/links/golang", "")
		const unauthenticated = `{"type":"/problems/unauthenticated","title":"Unauthenticated","status":401,"detail":"a valid bearer token is required","kind":"unauthenticated"}`
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer realm="shortlink"` || body != unauthenticated {
			t.Errorf("delete without a token = %d %v %s, want 401 with a challenge", resp.StatusCode, resp.Header, body)
		}
		if resp, body := s.do(t, "DELETE", "/links/golang", "", "Authorization", "Bearer "+userToken); resp.StatusCode != http.StatusForbidden {
			t.Errorf("delete by a user = %d %s, want 403", resp.StatusCode, body)
		}
		if resp, body := s.do(t, "DELETE", "/links/golang", "", "Authorization", "Bearer "+adminToken); resp.StatusCode != http.StatusNoContent || body != "" {
			t.Errorf("delete by the admin = %d %q, want 204", resp.StatusCode, body)
		}
		if resp, _ := s.do(t, "GET", "/links/golang", ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("get a deleted link = %d, want 404", resp.StatusCode)
		}
		if resp, _ := s.do(t, "DELETE", "/links/golang", "", "Authorization", "Bearer "+adminToken); resp.StatusCode != http.StatusNotFound {
			t.Errorf("delete a deleted link = %d, want 404", resp.StatusCode)
		}
	})
}

func TestMuxProblems(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		// The mux has no route for the path, or none for the method.
		resp, body := s.do(t, "GET", "/links/go/stats", "")
		const notFound = `{"type":"about:blank","title":"Not Found","status":404}`
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/problem+json" || body != notFound {
			t.Errorf("unknown path = %d %v %s, want 404 %s", resp.StatusCode, resp.Header, body, notFound)
		}
		resp, body = s.do(t, "PUT", "/links/go", "")
		const notAllowed = `{"type":"about:blank","title":"Method Not Allowed","status":405}`
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "DELETE, GET, HEAD" || body != notAllowed {
			t.Errorf("PUT = %d %v %s, want 405 %s with Allow", resp.StatusCode, resp.Header, body, notAllowed)
		}
	})
}

// Not parallel: it replaces the default logger, which handlers log to.
func TestLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		defer slog.SetDefault(slog.Default())
		slog.SetDefault(slog.New(tyr.NewLogHandler(s.logs)))

		// With a token: then Authenticate, under Logger, passes on a request
		// with another context, and the access record still has the route
		// and the operation, which rest records.
		resp, _ := s.do(t, "POST", "/links", `{"url":"https://go.dev","code":"golang"}`, "Authorization", "Bearer "+adminToken)
		synctest.Wait()

		// The record of the handler and the access record have the ID of the
		// request, sent back to the client.
		id := resp.Header.Get("X-Request-ID")
		want := []record{
			{msg: "link created", attrs: map[string]string{"request_id": id, "operation": "links.create", "code": "golang"}},
			{msg: "middleware: request", attrs: map[string]string{
				"request_id": id, "method": "POST", "route": "POST /links", "operation": "links.create",
				"status": "201", "duration": "0s",
			}},
		}
		if got := s.logs.get(); !slices.EqualFunc(got, want, equalRecords) {
			t.Errorf("logged %+v, want %+v", got, want)
		}
	})
}

func TestRPC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		c := rpc(s.client, "")
		ctx := t.Context()
		const link = `{"code":"go-docs","url":"https://go.dev/doc/","created_at":"2000-01-01T00:00:00Z"}`

		// The operations of REST, by their contract, with the same store.
		// Location is a header of REST, which JSON-RPC doesn't send.
		created, err := c.Call(ctx, contract.CreateLink, contract.CreateReq{URL: "https://go.dev/doc/", Code: "go-docs"})
		day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err != nil || created.Code != "go-docs" || created.URL != "https://go.dev/doc/" || !created.CreatedAt.Equal(day) || created.Location != "" {
			t.Errorf("create = %+v, %v; want go-docs of %v without a Location", created, err, day)
		}
		// REST redirects to the link, JSON-RPC returns it.
		followed, err := c.Call(ctx, contract.FollowLink, contract.GetReq{Code: "go-docs"})
		if want := (contract.FollowRes{URL: "https://go.dev/doc/"}); followed != want || err != nil {
			t.Errorf("follow = %+v, %v; want %+v", followed, err, want)
		}
		// The errors of the store, translated by MapError, and of validation.
		_, err = c.Call(ctx, contract.GetLink, contract.GetReq{Code: "nope"})
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindNotFound || se.Message != "link not found" {
			t.Errorf("get a missing link = %v, want not_found: link not found", err)
		}
		_, err = c.Call(ctx, contract.CreateLink, contract.CreateReq{URL: "go.dev"})
		const violations = `[{"pointer":"/url","detail":"must be an http or https URL"}]`
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindInvalidArgument || string(se.Details) != violations {
			t.Errorf("create with an invalid URL = %v, want invalid_argument with %s", err, violations)
		}

		// A batch, which the client doesn't send.
		batch := `[{"jsonrpc":"2.0","method":"links.get","params":{"code":"go-docs"},"id":1},` +
			`{"jsonrpc":"2.0","method":"links.get","params":{"code":"nope"},"id":2}]`
		want := `[{"jsonrpc":"2.0","result":` + link + `,"id":1},` +
			`{"jsonrpc":"2.0","error":{"code":404,"message":"link not found","data":{"kind":"not_found"}},"id":2}]`
		if resp, body := s.do(t, "POST", "/rpc", batch); resp.StatusCode != http.StatusOK || body != want {
			t.Errorf("batch = %d %s, want 200 %s", resp.StatusCode, body, want)
		}
		if resp, body := s.do(t, "GET", "/links/go-docs", ""); resp.StatusCode != http.StatusOK || body != link {
			t.Errorf("get over REST = %d %s, want 200 %s", resp.StatusCode, body, link)
		}
	})
}

func TestRPCLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		// The client sends the request ID of its context on, and the
		// service takes it, as a service that calls it would have it.
		const id = "0192f5e2-7c3a-7b1e-9c4d-2f1a3b5c7d9e"
		_, err := rpc(s.client, "").Call(tyr.WithRequestID(t.Context(), id), contract.GetLink, contract.GetReq{Code: "nope"})
		if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindNotFound {
			t.Errorf("get a missing link = %v, want not_found", err)
		}
		synctest.Wait()

		// Every call has the route of the endpoint; the access record has
		// the operation too, which jsonrpc records.
		want := []record{{msg: "middleware: request", attrs: map[string]string{
			"request_id": id, "method": "POST", "route": "POST /rpc", "operation": "links.get",
			"status": "200", "duration": "0s",
		}}}
		if got := s.logs.get(); !slices.EqualFunc(got, want, equalRecords) {
			t.Errorf("logged %+v, want %+v", got, want)
		}
	})
}

func TestPurge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		for _, req := range []contract.CreateReq{
			{URL: "https://evil.example/a", Code: "evil-a"},
			{URL: "http://EVIL.example:8080/b", Code: "evil-b"},
			{URL: "https://go.dev", Code: "go-dev"},
		} {
			if _, err := rpc(s.client, "").Call(t.Context(), contract.CreateLink, req); err != nil {
				t.Fatalf("create %s: %v", req.Code, err)
			}
		}

		// Purge has no REST route: JSON-RPC serves it, and the interceptor
		// authorizes it as it would over REST.
		purge := contract.PurgeReq{Host: "evil.example"}
		tests := []struct {
			name    string
			token   string
			kind    tyr.Kind
			message string
		}{
			{"without a token", "", tyr.KindUnauthenticated, "a valid bearer token is required"},
			{"by a user", userToken, tyr.KindPermissionDenied, "links.purge requires the role admin"},
		}
		for _, tt := range tests {
			_, err := rpc(s.client, tt.token).Call(t.Context(), contract.PurgeLinks, purge)
			if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tt.kind || se.Message != tt.message {
				t.Errorf("purge %s = %v, want %v: %s", tt.name, err, tt.kind, tt.message)
			}
		}
		if res, err := rpc(s.client, adminToken).Call(t.Context(), contract.PurgeLinks, purge); res.Purged != 2 || err != nil {
			t.Errorf("purge by the admin = %+v, %v; want 2 purged", res, err)
		}

		for code, status := range map[string]int{"evil-a": http.StatusNotFound, "evil-b": http.StatusNotFound, "go-dev": http.StatusOK} {
			if resp, body := s.do(t, "GET", "/links/"+code, ""); resp.StatusCode != status {
				t.Errorf("get %s = %d %s, want %d", code, resp.StatusCode, body, status)
			}
		}
	})
}

func TestInProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		// The whole server, without a network: its middleware
		// authenticates the caller by the header, as it does over one, and
		// an admin in the context of the test isn't one of the server.
		hc := inprocess.Client(s.handler)
		purge := contract.PurgeReq{Host: "evil.example"}
		admin := authz.WithCaller(t.Context(), authz.Caller{Name: "admin", Roles: []string{"admin"}})
		for _, ctx := range []context.Context{t.Context(), admin} {
			_, err := rpc(hc, "").Call(ctx, contract.PurgeLinks, purge)
			if se, ok := errors.AsType[*jsonrpc.ServerError](err); !ok || se.Kind != tyr.KindUnauthenticated {
				t.Errorf("purge without a token = %v, want unauthenticated", err)
			}
		}
		if res, err := rpc(hc, adminToken).Call(t.Context(), contract.PurgeLinks, purge); res.Purged != 0 || err != nil {
			t.Errorf("purge by the admin = %+v, %v; want none purged", res, err)
		}
	})
}

func TestShutdown(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	ready := health.NewReadiness()
	srv := newServer("", newAPI(links.New(store.New()), logger), nil, nil, ready, logger)
	// The signal comes from the handler: net/http drops a request it has
	// read but not yet handled when Shutdown starts, so ConnState's
	// StateActive would be too early.
	handling := make(chan struct{})
	handler := srv.Handler
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handling)
		handler.ServeHTTP(w, r)
	})
	stopping := make(chan struct{})
	srv.RegisterOnShutdown(func() { close(stopping) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, ready, 0) }()

	// A request is in flight, half of its body sent, when the service is
	// told to stop.
	body, w := io.Pipe()
	req, err := http.NewRequest("POST", "http://"+ln.Addr().String()+"/links", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{}}
	responded := make(chan *http.Response, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("create: %v", err)
		}
		responded <- resp
	}()
	_, _ = io.WriteString(w, `{"url":`)
	<-handling
	stop()
	<-stopping

	// The service finishes the request before it stops.
	_, _ = io.WriteString(w, `"https://go.dev"}`)
	_ = w.Close()
	if resp := <-responded; resp == nil || resp.StatusCode != http.StatusCreated {
		t.Errorf("create = %v, want 201", resp)
	} else {
		_ = resp.Body.Close()
	}
	if err := <-served; err != nil {
		t.Errorf("serve() = %v, want <nil>", err)
	}
	if _, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		t.Error("the service still accepts connections")
	}
}

func TestDrain(t *testing.T) {
	// Told to stop, the service fails readiness first and serves on for the
	// drain, closing connections after their responses; only then it
	// stops accepting connections.
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.DiscardHandler)
		ready := health.NewReadiness()
		srv := newServer("", newAPI(links.New(store.New()), logger), nil, nil, ready, logger)
		ln := newPipeListener()
		ctx, stop := context.WithCancel(t.Context())
		served := make(chan error, 1)
		go func() { served <- serve(ctx, srv, ln, ready, 5*time.Second) }()

		client := &http.Client{Transport: &http.Transport{DialContext: ln.dial}}
		defer client.CloseIdleConnections()
		// send sends a request and returns its status and whether the
		// service closes the connection after it; 0 if it can't be sent.
		send := func(method, target, body string) (int, bool) {
			req, err := http.NewRequest(method, "http://shortlink.example"+target, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, false
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode, resp.Close
		}
		if status, closed := send("GET", "/health/ready", ""); status != http.StatusOK || closed {
			t.Errorf("readiness = %d, closed %v; want 200 on a connection kept alive", status, closed)
		}

		stop()
		synctest.Wait()
		start := time.Now()
		for _, at := range []time.Duration{0, 4 * time.Second} {
			time.Sleep(at - time.Since(start))
			if status, _ := send("GET", "/health/ready", ""); status != http.StatusServiceUnavailable {
				t.Errorf("readiness %v into the drain = %d, want 503", at, status)
			}
			if status, _ := send("GET", "/health/live", ""); status != http.StatusOK {
				t.Errorf("liveness %v into the drain = %d, want 200", at, status)
			}
			if status, closed := send("POST", "/links", `{"url":"https://go.dev"}`); status != http.StatusCreated || !closed {
				t.Errorf("create %v into the drain = %d, closed %v; want 201 on a connection closed after it", at, status, closed)
			}
		}

		if err := <-served; err != nil {
			t.Errorf("serve() = %v, want <nil>", err)
		}
		if took := time.Since(start); took != 5*time.Second {
			t.Errorf("serve() returned %v after the signal, want 5s", took)
		}
		if status, _ := send("GET", "/health/live", ""); status != 0 {
			t.Errorf("after the shutdown, liveness = %d, want no connection", status)
		}
	})
}

// pipeListener is a listener in memory, which a synctest bubble can serve
// on: dial makes a net.Pipe and hands one end to Accept.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	close  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.close.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return pipeAddr{}
}

// dial connects to the listener, as the DialContext of an http.Transport.
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		return nil, errors.New("connection refused")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pipeAddr is the address of a pipeListener.
type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// logs is a slog.Handler that keeps the records it gets.
type logs struct {
	mu      sync.Mutex
	records []record
}

// record is a record that logs got, with its attributes as text.
type record struct {
	msg   string
	attrs map[string]string
}

func (l *logs) Enabled(context.Context, slog.Level) bool {
	return true
}

func (l *logs) Handle(_ context.Context, r slog.Record) error {
	rec := record{msg: r.Message, attrs: make(map[string]string)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
	return nil
}

func (l *logs) WithAttrs([]slog.Attr) slog.Handler {
	return l
}

func (l *logs) WithGroup(string) slog.Handler {
	return l
}

// get returns the records l got so far.
func (l *logs) get() []record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.records)
}

// equalRecords reports whether the records a and b are equal.
func equalRecords(a, b record) bool {
	return a.msg == b.msg && maps.Equal(a.attrs, b.attrs)
}

var update = flag.Bool("update", false, "update the golden files in testdata")

// golden compares a document with the golden file testdata/name.json, or
// writes the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	got = append(bytes.Clone(got), '\n') // a file ends with a newline, a body doesn't
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s:\ngot  %s\nwant %s", path, got, want)
	}
}

func TestDocuments(t *testing.T) {
	// The service describes itself, to anyone: the documents need no token.
	synctest.Test(t, func(t *testing.T) {
		s := start(t)
		resp, body := s.do(t, "GET", "/openapi.json", "")
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("GET /openapi.json = %d %v, want 200 JSON", resp.StatusCode, resp.Header)
		}
		golden(t, "openapi", []byte(body))

		resp, body = s.do(t, "POST", "/rpc", `{"jsonrpc":"2.0","method":"rpc.discover","id":1}`)
		var res struct {
			Result jsontext.Value `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &res); err != nil || resp.StatusCode != http.StatusOK || res.Result == nil {
			t.Fatalf("rpc.discover = %d %s, want a result", resp.StatusCode, body)
		}
		if err := res.Result.Indent(jsontext.WithIndent("  ")); err != nil {
			t.Fatal(err)
		}
		golden(t, "openrpc", res.Result)
	})
}
