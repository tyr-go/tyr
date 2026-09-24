package rest_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

// The types of the API that TestOpenAPI documents.
type (
	docLink struct {
		Code      string    `json:"code" doc:"The code of the link."`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
	}
	docCreated struct {
		docLink
		Location string `json:"-" header:"Location" doc:"The path of the link."`
	}
	docCreateReq struct {
		URL    string `json:"url" validate:"required,http_url" doc:"Where the link leads."`
		Code   string `json:"code" validate:"omitempty,min=4,max=16"`
		Tenant string `json:"tenant" header:"X-Tenant"`
	}
	docGetReq struct {
		Code string `json:"code" path:"code" validate:"required"`
	}
	docListReq struct {
		Tags  []string  `json:"tags" query:"tag" validate:"max=3"`
		Limit int       `json:"limit" query:"limit" validate:"omitempty,min=1,max=100" doc:"How many links to return."`
		After *string   `json:"after" query:"after"`
		Since time.Time `json:"since" header:"If-Modified-Since"`
	}
	docFollowRes struct {
		URL string `json:"url" header:"Location"`
	}
	docImportReq struct {
		Links []docLink `json:"links" validate:"required,max=10"`
	}
	docImported struct {
		Imported int `json:"imported"`
	}
	docFileReq struct {
		Path string `json:"path" path:"path"`
	}
)

// docAPI returns an API with an operation of each shape that OpenAPI
// describes.
func docAPI() *tyr.API {
	api := newAPI()
	created := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	api.Implement(tyr.Define[docCreateReq, docCreated]("links.create",
		rest.Route("POST /links"), rest.Status(http.StatusCreated),
		tyr.Summary("Create a link"),
		tyr.Description("Without a code, the link gets a random one."),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindAlreadyExists, tyr.KindFailedPrecondition),
	).Example("with a code",
		docCreateReq{URL: "https://go.dev", Code: "go-home", Tenant: "acme"},
		docCreated{Code: "go-home", URL: "https://go.dev", CreatedAt: created, Location: "/links/go-home"},
	), func(ctx context.Context, req docCreateReq) (docCreated, error) { return docCreated{}, nil })

	api.Implement(tyr.Define[docGetReq, *docLink]("links.get",
		rest.Route("GET /links/{code}"), tyr.Tags("links"), tyr.Errors(tyr.KindNotFound),
	).Example("go-home", docGetReq{Code: "go-home"}, &docLink{Code: "go-home", URL: "https://go.dev", CreatedAt: created}),
		func(ctx context.Context, req docGetReq) (*docLink, error) { return nil, nil })
	api.Handle("links.list", func(ctx context.Context, req docListReq) ([]docLink, error) { return nil, nil },
		rest.Route("GET /links"), tyr.Tags("links"))
	api.Implement(tyr.Define[docGetReq, docFollowRes]("links.follow",
		rest.Route("GET /{code}"), rest.Status(http.StatusFound),
	).Example("go-home", docGetReq{Code: "go-home"}, docFollowRes{URL: "https://go.dev"}),
		func(ctx context.Context, req docGetReq) (docFollowRes, error) { return docFollowRes{}, nil })
	api.Group(tyr.Errors(tyr.KindUnauthenticated, tyr.KindPermissionDenied)).Handle("links.delete",
		func(ctx context.Context, req docGetReq) (struct{}, error) { return struct{}{}, nil },
		rest.Route("DELETE /links/{code}"), tyr.Deprecated())
	api.Handle("links.import", func(ctx context.Context, req docImportReq) (docImported, error) { return docImported{}, nil },
		rest.Route("POST /links/import"))
	api.Handle("files.get", func(ctx context.Context, req docFileReq) (string, error) { return "", nil },
		rest.Route("GET /files/{path...}"))
	api.Handle("home.get", func(ctx context.Context, req struct{}) (string, error) { return "", nil },
		rest.Route("GET /{$}"))
	api.Handle("status.get", func(ctx context.Context, req struct{}) (string, error) { return "", nil },
		rest.Route("GET status.example.com/status"))
	api.Handle("links.purge", func(ctx context.Context, req struct{}) (struct{}, error) { return struct{}{}, nil }) // JSON-RPC only
	return api
}

func TestOpenAPI(t *testing.T) {
	api := docAPI()
	h := rest.OpenAPI(api, tyr.Info{Title: "links", Version: "1.0.0", Description: "Short links."},
		rest.Challenge(`Bearer realm="links"`))

	rec := do(h, "GET", "/openapi.json", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("GET = %d %v, want 200 JSON", rec.Code, rec.Header())
	}
	golden(t, "openapi", rec.Body.Bytes())

	// It sealed the API: an operation left out would be a surprise.
	register := func() {
		api.Handle("links.late", func(ctx context.Context, req struct{}) (struct{}, error) { return struct{}{}, nil })
	}
	if got := panicValue(register); got == nil {
		t.Error("Handle() after OpenAPI didn't panic")
	}
}

func TestOpenAPIPanics(t *testing.T) {
	info := tyr.Info{Title: "links", Version: "1.0.0"}
	noop := func(ctx context.Context, req struct{}) (string, error) { return "", nil }
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"nil API", func() { rest.OpenAPI(nil, info) }, "rest: OpenAPI: nil API"},
		{"no title", func() { rest.OpenAPI(newAPI(), tyr.Info{Version: "1.0.0"}) }, "rest: OpenAPI: the Info needs a Title and a Version"},
		{"no version", func() { rest.OpenAPI(newAPI(), tyr.Info{Title: "links"}) }, "rest: OpenAPI: the Info needs a Title and a Version"},
		{"nil option", func() { rest.OpenAPI(newAPI(), info, nil) }, "rest: OpenAPI: nil option"},
		{"a route Mount rejects", func() {
			api := newAPI()
			api.Handle("links.get", noop, rest.Route("/links"))
			rest.OpenAPI(api, info)
		}, `rest: operation "links.get": pattern "/links" has no method, e.g. "GET /links"`},
		{"a method OpenAPI doesn't know", func() {
			api := newAPI()
			api.Handle("links.find", noop, rest.Route("PROPFIND /links"))
			rest.OpenAPI(api, info)
		}, `rest: operation "links.find": OpenAPI 3.1 has no method PROPFIND`},
		{"one path of two hosts", func() {
			api := newAPI()
			api.Handle("status.a", noop, rest.Route("GET a.example.com/status"))
			api.Handle("status.b", noop, rest.Route("GET b.example.com/status"))
			rest.OpenAPI(api, info)
		}, `rest: operation "status.b": operation "status.a" is at GET /status too`},
		{"a field JSON can't carry", func() {
			type waitReq struct {
				For time.Duration `json:"for"`
			}
			api := newAPI()
			api.Handle("jobs.wait", func(ctx context.Context, req waitReq) (string, error) { return "", nil }, rest.Route("POST /wait"))
			rest.OpenAPI(api, info)
		}, `rest: operation "jobs.wait": rest_test.waitReq.For: json/v2 has no representation of time.Duration`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := panicValue(tt.f).(string)
			if got != tt.want {
				t.Errorf("panicked with %q, want %q", got, tt.want)
			}
		})
	}
	if got, _ := panicValue(func() { rest.OpenAPI(nil, info) }).(string); !strings.HasPrefix(got, "rest: OpenAPI") {
		t.Errorf("panicked with %q", got)
	}
}
