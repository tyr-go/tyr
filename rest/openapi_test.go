package rest_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/plan/plantest"
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
	routes := rest.Mount(http.NewServeMux(), docAPI(), rest.Challenge(`Bearer realm="links"`))
	h := routes.OpenAPI(tyr.Info{Title: "links", Version: "1.0.0", Description: "Short links."})

	rec := do(h, "GET", "/openapi.json", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("GET = %d %v, want 200 JSON", rec.Code, rec.Header())
	}
	golden(t, "openapi", rec.Body.Bytes())
}

// Types that the stable names of schemas are checked with.
type (
	stableAddress struct {
		City string `json:"city"`
	}
	stableSignUpReq struct {
		Name    string        `json:"name" validate:"required"`
		Address stableAddress `json:"address"`
	}
	// stableTag has the same schemas in both directions: its member is
	// optional both ways.
	stableTag struct {
		Name string `json:"name,omitempty"`
	}
	stableTagsReq struct {
		Tags []stableTag `json:"tags"`
	}
)

func TestOpenAPIStableNames(t *testing.T) {
	// A schema is named after its type and direction only: operations
	// added to an API rename none of those it had, and requests get the
	// suffix Input, whether their schemas differ from those of results or
	// not, and whether the type is in results at all.
	names := func(api *tyr.API) []string {
		rec := do(rest.Mount(http.NewServeMux(), api).OpenAPI(tyr.Info{Title: "links", Version: "1.0.0"}), "GET", "/openapi.json", "", "")
		var doc struct {
			Components struct {
				Schemas map[string]jsontext.Value `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(doc.Components.Schemas))
	}
	build := func(more bool) *tyr.API {
		api := newAPI()
		api.Handle("links.get", func(ctx context.Context, req docGetReq) (docLink, error) { return docLink{}, nil },
			rest.Route("GET /links/{code}"))
		if more {
			api.Handle("links.import", func(ctx context.Context, req docImportReq) (docImported, error) { return docImported{}, nil },
				rest.Route("POST /links/import"))
			api.Handle("users.signup", func(ctx context.Context, req stableSignUpReq) (struct{}, error) { return struct{}{}, nil },
				rest.Route("POST /users"))
			api.Handle("tags.set", func(ctx context.Context, req stableTagsReq) ([]stableTag, error) { return nil, nil },
				rest.Route("PUT /tags"))
		}
		return api
	}

	few, many := names(build(false)), names(build(true))
	if want := []string{"Problem", "Violation", "docLink"}; !slices.Equal(few, want) {
		t.Errorf("schemas of one operation = %q, want %q", few, want)
	}
	want := []string{"Problem", "Violation", "docImported", "docLink", "docLinkInput", "stableAddressInput", "stableTag", "stableTagInput"}
	if !slices.Equal(many, want) {
		t.Errorf("schemas of more operations = %q, want %q", many, want)
	}
}

func TestOpenAPIPanics(t *testing.T) {
	info := tyr.Info{Title: "links", Version: "1.0.0"}
	noop := func(ctx context.Context, req struct{}) (string, error) { return "", nil }
	openAPI := func(api *tyr.API, info tyr.Info) func() {
		return func() { rest.Mount(http.NewServeMux(), api).OpenAPI(info) }
	}
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"no title", openAPI(newAPI(), tyr.Info{Version: "1.0.0"}), "rest: OpenAPI: the Info needs a Title and a Version"},
		{"no version", openAPI(newAPI(), tyr.Info{Title: "links"}), "rest: OpenAPI: the Info needs a Title and a Version"},
		{"a method OpenAPI doesn't know", func() {
			api := newAPI()
			api.Handle("links.find", noop, rest.Route("PROPFIND /links"))
			openAPI(api, info)()
		}, `rest: operation "links.find": OpenAPI 3.1 has no method PROPFIND`},
		{"one path of two hosts", func() {
			api := newAPI()
			api.Handle("status.a", noop, rest.Route("GET a.example.com/status"))
			api.Handle("status.b", noop, rest.Route("GET b.example.com/status"))
			openAPI(api, info)()
		}, `rest: operation "status.b": operation "status.a" is at GET /status too`},
		{"a field JSON can't carry", func() {
			type waitReq struct {
				For time.Duration `json:"for"`
			}
			api := newAPI()
			api.Handle("jobs.wait", func(ctx context.Context, req waitReq) (string, error) { return "", nil }, rest.Route("POST /wait"))
			openAPI(api, info)()
		}, `rest: operation "jobs.wait": rest_test.waitReq.For: json/v2 has no representation of time.Duration`},
		{"two types of one schema name", func() {
			type Inner struct {
				Name string `json:"name"`
			}
			api := newAPI()
			api.Handle("inner.ours", func(ctx context.Context, req struct{}) (Inner, error) { return Inner{}, nil }, rest.Route("GET /ours"))
			api.Handle("inner.theirs", func(ctx context.Context, req struct{}) (plantest.Inner, error) { return plantest.Inner{}, nil }, rest.Route("GET /theirs"))
			openAPI(api, info)()
		}, `rest: OpenAPI: two types have the schema name "Inner": github.com/tyr-go/tyr/rest_test.Inner and ` +
			`github.com/tyr-go/tyr/internal/plan/plantest.Inner; give one of them a method SchemaName() string`},
		{"the name of the problems", func() {
			type Problem struct {
				Title string `json:"title"`
			}
			api := newAPI()
			api.Handle("problems.get", func(ctx context.Context, req struct{}) (Problem, error) { return Problem{}, nil }, rest.Route("GET /problem"))
			openAPI(api, info)()
		}, `rest: OpenAPI: two types have the schema name "Problem": github.com/tyr-go/tyr/rest.problem and ` +
			`github.com/tyr-go/tyr/rest_test.Problem; give one of them a method SchemaName() string`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := panicValue(tt.f).(string)
			if got != tt.want {
				t.Errorf("panicked with %q, want %q", got, tt.want)
			}
		})
	}
}

// phoneReq has a rule that the core doesn't know, for a validator of its
// own.
type phoneReq struct {
	Phone string `json:"phone" validate:"e164"`
	Name  string `json:"name" validate:"omitempty,min=2,alphaunicode"`
}

// phoneValidator is a TagValidator by which a phoneReq without a phone
// fails at it.
type phoneValidator struct{}

func (phoneValidator) Plan(t reflect.Type) (func(req any) []tyr.FailedRule, error) {
	return func(req any) []tyr.FailedRule {
		if r, ok := req.(*phoneReq); ok && r.Phone == "" {
			return []tyr.FailedRule{{Path: []string{"Phone"}, Rule: "e164"}}
		}
		return nil
	}, nil
}

func TestOpenAPIWithValidator(t *testing.T) {
	// With a validator of its own, a member is required as a zero value
	// fails the validator, and only the rules of the core add keywords.
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)), tyr.WithValidator(phoneValidator{}))
	api.Handle("calls.start", func(ctx context.Context, req phoneReq) (struct{}, error) { return struct{}{}, nil },
		rest.Route("POST /calls"))
	rec := do(rest.Mount(http.NewServeMux(), api).OpenAPI(tyr.Info{Title: "calls", Version: "1.0.0"}), "GET", "/openapi.json", "", "")
	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema jsontext.Value `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	got := string(doc.Paths["/calls"]["post"].RequestBody.Content["application/json"].Schema)
	const want = `{"type":"object","properties":{"phone":{"type":"string"},"name":{"type":"string","minLength":2}},"required":["phone"]}`
	if compacted, err := jsontext.AppendFormat(nil, []byte(got)); err != nil || string(compacted) != want {
		t.Errorf("the body = %s, want %s", compacted, want)
	}
}

func TestOpenAPITypedErrors(t *testing.T) {
	// The schema of an error of a status has its kinds and their types, as
	// constants or enums, with the types of ProblemTypes; default has none.
	api := newAPI()
	api.Handle("links.create", func(ctx context.Context, req docCreateReq) (struct{}, error) { return struct{}{}, nil },
		rest.Route("POST /links"), tyr.Errors(tyr.KindAlreadyExists, tyr.KindFailedPrecondition, tyr.KindUnauthenticated))
	routes := rest.Mount(http.NewServeMux(), api, rest.ProblemTypes("https://example.com/problems/"), rest.Challenge(`Bearer realm="links"`))
	rec := do(routes.OpenAPI(tyr.Info{Title: "links", Version: "1.0.0"}), "GET", "/openapi.json", "", "")
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Headers map[string]jsontext.Value `json:"headers"`
				Content map[string]struct {
					Schema jsontext.Value `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	responses := doc.Paths["/links"]["post"].Responses
	schema := func(status string) string {
		s, _ := jsontext.AppendFormat(nil, responses[status].Content["application/problem+json"].Schema)
		return string(s)
	}
	typed := func(typ, kind string) string {
		return `{"allOf":[{"$ref":"#/components/schemas/Problem"},{"type":"object","properties":{"type":` + typ +
			`,"kind":` + kind + `},"required":["type","kind"]}]}`
	}
	tests := []struct{ status, want string }{
		{"400", typed(`{"const":"https://example.com/problems/invalid_argument"}`, `{"const":"invalid_argument"}`)},
		{"401", typed(`{"const":"https://example.com/problems/unauthenticated"}`, `{"const":"unauthenticated"}`)},
		{"409", typed(`{"enum":["https://example.com/problems/already_exists","https://example.com/problems/failed_precondition"]}`,
			`{"enum":["already_exists","failed_precondition"]}`)},
		{"default", `{"$ref":"#/components/schemas/Problem"}`},
	}
	for _, tt := range tests {
		if got := schema(tt.status); got != tt.want {
			t.Errorf("%s:\n%s\nwant\n%s", tt.status, got, tt.want)
		}
	}
	if _, ok := responses["401"].Headers["WWW-Authenticate"]; !ok {
		t.Error("401 lost its WWW-Authenticate")
	}
}
