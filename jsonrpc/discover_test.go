package jsonrpc_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/jsonrpc"
)

// The types of the API that TestDiscover documents.
type (
	docLink struct {
		Code      string    `json:"code" doc:"The code of the link."`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
	}
	docCreateReq struct {
		URL  string `json:"url" validate:"required,http_url" doc:"Where the link leads."`
		Code string `json:"code" validate:"omitempty,min=4,max=16"`
	}
	docGetReq struct {
		Code string `json:"code" validate:"required"`
	}
	docPurgeReq struct {
		Host  string    `json:"host" validate:"required,max=253"`
		Limit *int      `json:"limit" validate:"omitempty,min=1"`
		Links []docLink `json:"links"`
	}
	docPurged struct {
		Purged int `json:"purged"`
	}
)

// docAPI returns an API with an operation of each shape that OpenRPC
// describes.
func docAPI() *tyr.API {
	api := newAPI()
	created := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	api.Implement(tyr.Define[docCreateReq, docLink]("links.create",
		tyr.Summary("Create a link"),
		tyr.Description("Without a code, the link gets a random one."),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindAlreadyExists, tyr.KindFailedPrecondition),
	).Example("with a code",
		docCreateReq{URL: "https://go.dev", Code: "go-home"},
		docLink{Code: "go-home", URL: "https://go.dev", CreatedAt: created},
	), func(ctx context.Context, req docCreateReq) (docLink, error) { return docLink{}, nil })
	api.Handle("links.get", func(ctx context.Context, req docGetReq) (*docLink, error) { return nil, nil },
		tyr.Tags("links"), tyr.Errors(tyr.KindNotFound))
	admin := api.Group(tyr.Tags("admin"), tyr.Errors(tyr.KindUnauthenticated, tyr.KindPermissionDenied))
	admin.Handle("links.delete", func(ctx context.Context, req docGetReq) (struct{}, error) { return struct{}{}, nil },
		tyr.Deprecated())
	admin.Handle("links.purge", func(ctx context.Context, req docPurgeReq) (docPurged, error) { return docPurged{}, nil },
		tyr.Summary("Purge the links to a host"),
		tyr.Example("a host", docPurgeReq{Host: "example.com"}, docPurged{Purged: 2}))
	return api
}

// discover calls rpc.discover of h and returns the result, indented.
func discover(t *testing.T, h http.Handler) []byte {
	t.Helper()
	rec := post(h, `{"jsonrpc":"2.0","method":"rpc.discover","id":1}`)
	var res struct {
		Result jsontext.Value `json:"result"`
		Error  jsontext.Value `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || res.Result == nil {
		t.Fatalf("rpc.discover = %d %s, want a result", rec.Code, rec.Body)
	}
	if err := res.Result.Indent(jsontext.WithIndent("  ")); err != nil {
		t.Fatal(err)
	}
	return res.Result
}

func TestDiscover(t *testing.T) {
	h := jsonrpc.Handler(docAPI(), jsonrpc.Discover(tyr.Info{Title: "links", Version: "1.0.0", Description: "Short links."}))
	golden(t, "openrpc", discover(t, h))

	// In a batch, and with params, which it ignores.
	rec := post(h, `[{"jsonrpc":"2.0","method":"rpc.discover","params":{"x":1},"id":"a"},{"jsonrpc":"2.0","method":"links.purge","params":{"host":"go.dev"},"id":"b"}]`)
	var batch []struct {
		Result jsontext.Value `json:"result"`
		ID     string         `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil || len(batch) != 2 || batch[0].ID != "a" || batch[0].Result.Kind() != '{' || string(batch[1].Result) != `{"purged":0}` {
		t.Errorf("batch = %s, want the document and the result of links.purge", rec.Body)
	}

	// A notification gets nothing.
	if rec := post(h, `{"jsonrpc":"2.0","method":"rpc.discover"}`); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("notification = %d %s, want 204", rec.Code, rec.Body)
	}
}

func TestDiscoverOff(t *testing.T) {
	// Without Discover, rpc.discover is a method that doesn't exist.
	rec := post(jsonrpc.Handler(docAPI()), `{"jsonrpc":"2.0","method":"rpc.discover","id":1}`)
	want := `{"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found"},"id":1}`
	if rec.Body.String() != want {
		t.Errorf("rpc.discover = %s, want %s", rec.Body, want)
	}
}

func TestDiscoverPanics(t *testing.T) {
	info := tyr.Info{Title: "links", Version: "1.0.0"}
	type waitReq struct {
		For time.Duration `json:"for"`
	}
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"no title", func() { jsonrpc.Discover(tyr.Info{Version: "1.0.0"}) }, "jsonrpc: Discover: the Info needs a Title and a Version"},
		{"no version", func() { jsonrpc.Discover(tyr.Info{Title: "links"}) }, "jsonrpc: Discover: the Info needs a Title and a Version"},
		{"a field JSON can't carry", func() {
			api := newAPI()
			api.Handle("jobs.wait", func(ctx context.Context, req waitReq) (string, error) { return "", nil })
			jsonrpc.Handler(api, jsonrpc.Discover(info))
		}, `jsonrpc: Discover: operation "jobs.wait": jsonrpc_test.waitReq.For: json/v2 has no representation of time.Duration`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(tt.f); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

func ExampleDiscover() {
	type GetLinkReq struct {
		Code string `json:"code" validate:"required" doc:"The code of the link."`
	}
	type Link struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	api := tyr.New()
	api.Implement(tyr.Define[GetLinkReq, Link]("links.get", tyr.Summary("Get a link"), tyr.Errors(tyr.KindNotFound)),
		func(ctx context.Context, req GetLinkReq) (Link, error) {
			return Link{Code: req.Code, URL: "https://go.dev"}, nil
		})
	h := jsonrpc.Handler(api, jsonrpc.Discover(tyr.Info{Title: "links", Version: "1.0.0"}))

	rec := post(h, `{"jsonrpc":"2.0","method":"rpc.discover","id":1}`)
	var res struct {
		Result struct {
			OpenRPC string `json:"openrpc"`
			Methods []struct {
				Name    string `json:"name"`
				Summary string `json:"summary"`
				Params  []struct {
					Name     string `json:"name"`
					Required bool   `json:"required"`
				} `json:"params"`
				Errors []struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"errors"`
			} `json:"methods"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		fmt.Println(err)
	}
	fmt.Println("OpenRPC", res.Result.OpenRPC)
	for _, m := range res.Result.Methods {
		fmt.Println(m.Name, "-", m.Summary)
		for _, p := range m.Params {
			fmt.Println("  param", p.Name, "required:", p.Required)
		}
		for _, e := range m.Errors {
			fmt.Println("  error", e.Code, e.Message)
		}
	}
	// Output:
	// OpenRPC 1.4.1
	// links.get - Get a link
	//   param code required: true
	//   error -32602 invalid argument
	//   error 404 not found
}
