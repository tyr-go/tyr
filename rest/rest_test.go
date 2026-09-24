package rest_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

type getLinkReq struct {
	Code string `json:"code" path:"code"`
}

type link struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

func getLink(ctx context.Context, req getLinkReq) (*link, error) {
	return &link{Code: req.Code, URL: "https://go.dev/" + req.Code}, nil
}

// newAPI returns an API that doesn't log, for tests.
func newAPI() *tyr.API {
	return tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
}

func TestMountPanics(t *testing.T) {
	tests := []struct {
		name     string
		register func(api *tyr.API)
		want     string // the panic message, or its start after "..."
	}{
		{
			name:     "no method",
			register: func(api *tyr.API) { api.Handle("links.get", getLink, rest.Route("/links/{code}")) },
			want:     `rest: operation "links.get": pattern "/links/{code}" has no method, e.g. "GET /links/{code}"`,
		},
		{
			name:     "syntax",
			register: func(api *tyr.API) { api.Handle("links.get", getLink, rest.Route("GET /links/{code")) },
			want:     `rest: operation "links.get": parsing "GET /links/{code": ...`,
		},
		{
			name:     "wildcard without a field",
			register: func(api *tyr.API) { api.Handle("links.get", getLink, rest.Route("GET /links/{id}")) },
			want:     `rest: operation "links.get": pattern "GET /links/{id}" has wildcard {id}, but rest_test.getLinkReq has no field with path:"id"`,
		},
		{
			name:     "field without a wildcard",
			register: func(api *tyr.API) { api.Handle("links.get", getLink, rest.Route("GET /links")) },
			want:     `rest: operation "links.get": field Code has path:"code", but pattern "GET /links" has no wildcard {code}`,
		},
		{
			name: "field that can't be bound",
			register: func(api *tyr.API) {
				api.Handle("links.list", func(ctx context.Context, req struct {
					Tags map[string]int `json:"tags" query:"tags"`
				}) (string, error) {
					return "", nil
				}, rest.Route("GET /links"))
			},
			want: `rest: operation "links.list": field Tags has query:"tags", but its type map[string]int can't be bound`,
		},
		{
			name: "option in a query tag",
			register: func(api *tyr.API) {
				api.Handle("links.list", func(ctx context.Context, req struct {
					Tag string `json:"tag" query:"tag,omitempty"`
				}) (string, error) {
					return "", nil
				}, rest.Route("GET /links"))
			},
			want: `rest: operation "links.list": field Tag has query:"tag,omitempty", which isn't a query parameter name: it has a comma or a space`,
		},
		{
			name: "header tag that isn't a header name",
			register: func(api *tyr.API) {
				api.Handle("links.list", func(ctx context.Context, req struct {
					Token string `json:"token" header:"X Token"`
				}) (string, error) {
					return "", nil
				}, rest.Route("GET /links"))
			},
			want: `rest: operation "links.list": field Token has header:"X Token", which isn't a header name`,
		},
		{
			name: "conflict",
			register: func(api *tyr.API) {
				api.Handle("links.get", getLink, rest.Route("GET /links/{code}"))
				api.Handle("links.find", func(ctx context.Context, req struct {
					ID string `json:"id" path:"id"`
				}) (string, error) {
					return "", nil
				}, rest.Route("GET /links/{id}"))
			},
			want: `rest: operation "links.find": pattern "GET /links/{id}" ...`,
		},
		{
			name: "204 for a result with a body",
			register: func(api *tyr.API) {
				api.Handle("links.get", getLink, rest.Route("GET /links/{code}"), rest.Status(http.StatusNoContent))
			},
			want: `rest: operation "links.get": Status(204) needs a result without JSON members, such as struct{}, not *rest_test.link`,
		},
		{
			name: "205 for a result with a body",
			register: func(api *tyr.API) {
				api.Handle("links.get", getLink, rest.Route("GET /links/{code}"), rest.Status(http.StatusResetContent))
			},
			want: `rest: operation "links.get": Status(205) needs a result without JSON members, such as struct{}, not *rest_test.link`,
		},
		{
			name: "redirect without a Location",
			register: func(api *tyr.API) {
				api.Handle("links.follow", getLink, rest.Route("GET /{code}"), rest.Status(http.StatusFound))
			},
			want: `rest: operation "links.follow": Status(302) is a redirect, but *rest_test.link has no field with header:"Location"`,
		},
		{
			name: "field that can't set a header",
			register: func(api *tyr.API) {
				api.Handle("links.list", func(ctx context.Context, req struct{}) (struct {
					Links []string `json:"-" header:"Link"`
				}, error) {
					return struct {
						Links []string `json:"-" header:"Link"`
					}{}, nil
				}, rest.Route("GET /links"))
			},
			want: `rest: operation "links.list": field Links has header:"Link", but its type []string can't set a header`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newAPI()
			tt.register(api)
			got, _ := panicValue(func() { rest.Mount(http.NewServeMux(), api) }).(string)
			if prefix, ok := strings.CutSuffix(tt.want, "..."); ok && !strings.HasPrefix(got, prefix) || !ok && got != tt.want {
				t.Errorf("Mount() panicked with %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		const want = "rest: Mount: nil mux or API"
		for _, mount := range []func(){
			func() { rest.Mount(nil, newAPI()) },
			func() { rest.Mount(http.NewServeMux(), nil) },
		} {
			if got := panicValue(mount); got != want {
				t.Errorf("Mount() panicked with %v, want %q", got, want)
			}
		}
		if got, want := panicValue(func() { rest.Mount(http.NewServeMux(), newAPI(), nil) }), "rest: Mount: nil option"; got != want {
			t.Errorf("Mount() panicked with %v, want %q", got, want)
		}
	})
}

func TestMountRoutes(t *testing.T) {
	tests := []struct {
		name     string
		register func(api *tyr.API)
	}{
		{
			name: "remainder wildcard",
			register: func(api *tyr.API) {
				api.Handle("files.get", func(ctx context.Context, req struct {
					Path string `json:"path" path:"path"`
				}) (string, error) {
					return req.Path, nil
				}, rest.Route("GET /files/{path...}"))
			},
		},
		{
			name: "end of path",
			register: func(api *tyr.API) {
				api.Handle("home", func(ctx context.Context, req struct{}) (string, error) {
					return "home", nil
				}, rest.Route("GET /{$}"))
			},
		},
		{
			name: "host and a tab",
			register: func(api *tyr.API) {
				api.Handle("links.get", getLink, rest.Route("GET\tgo.dev/links/{code}"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newAPI()
			tt.register(api)
			if got := panicValue(func() { rest.Mount(http.NewServeMux(), api) }); got != nil {
				t.Errorf("Mount() panicked with %v", got)
			}
		})
	}
}

func TestMount(t *testing.T) {
	api := newAPI()
	get := api.Handle("links.get", getLink, rest.Route("GET /links/{code}"))
	purge := api.Handle("links.purge", getLink) // no Route: left out
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	if got := panicValue(func() { api.Handle("links.late", getLink) }); got == nil {
		t.Error("Handle() after Mount didn't panic: Mount must seal the API")
	}
	if pattern, ok := rest.RouteOf(get); pattern != "GET /links/{code}" || !ok {
		t.Errorf("RouteOf(links.get) = %q, %t; want %q, true", pattern, ok, "GET /links/{code}")
	}
	if pattern, ok := rest.RouteOf(purge); pattern != "" || ok {
		t.Errorf("RouteOf(links.purge) = %q, %t; want \"\", false", pattern, ok)
	}
	if rec := do(mux, "GET", "/links/go", "", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /links/go = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestStatusPanics(t *testing.T) {
	for _, code := range []int{0, 199, 300, 304, 305, 306, 404} {
		if got, want := panicValue(func() { rest.Status(code) }), "rest: Status("; got == nil || !strings.HasPrefix(got.(string), want) {
			t.Errorf("Status(%d) panicked with %v, want a panic", code, got)
		}
	}
	for _, code := range []int{200, 201, 299, 301, 302, 303, 307, 308} {
		if got := panicValue(func() { rest.Status(code) }); got != nil {
			t.Errorf("Status(%d) panicked with %v", code, got)
		}
	}
	if got, want := panicValue(func() { rest.Status(404) }), "rest: Status(404): want a 2xx status or a redirect: 301, 302, 303, 307 or 308"; got != want {
		t.Errorf("Status(404) panicked with %v, want %q", got, want)
	}
}

func TestChallengePanics(t *testing.T) {
	for _, c := range []string{"", "Bearer\r\nSet-Cookie: x=1", "Bearer\n"} {
		want := fmt.Sprintf("rest: Challenge(%q): want a challenge without line breaks", c)
		if got := panicValue(func() { rest.Challenge(c) }); got != want {
			t.Errorf("Challenge(%q) panicked with %v, want %q", c, got, want)
		}
	}
}

func TestMaxBodyBytesPanics(t *testing.T) {
	for _, n := range []int64{0, -1} {
		if got := panicValue(func() { rest.MaxBodyBytes(n) }); got == nil {
			t.Errorf("MaxBodyBytes(%d) didn't panic", n)
		}
	}
	if got, want := panicValue(func() { rest.MaxBodyBytes(0) }), "rest: MaxBodyBytes(0): want a positive size"; got != want {
		t.Errorf("MaxBodyBytes(0) panicked with %v, want %q", got, want)
	}
}

// panicValue calls f and returns the value it panicked with, or nil.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}
