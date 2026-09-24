package jsonrpc_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/plan/plantest"
	"github.com/tyr-go/tyr/jsonrpc"
)

// docVisibility is an enum of the API that TestDiscover documents.
type docVisibility string

func (docVisibility) EnumValues() []docVisibility { return []docVisibility{"public", "private"} }

// The types of the API that TestDiscover documents.
type (
	docLink struct {
		Code       string        `json:"code" doc:"The code of the link."`
		URL        string        `json:"url"`
		CreatedAt  time.Time     `json:"created_at"`
		Visibility docVisibility `json:"visibility" doc:"Who may follow the link."`
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
		docLink{Code: "go-home", URL: "https://go.dev", CreatedAt: created, Visibility: "public"},
	), func(ctx context.Context, req docCreateReq) (docLink, error) { return docLink{}, nil })
	api.Handle("links.get", func(ctx context.Context, req docGetReq) (*docLink, error) { return nil, nil },
		tyr.Tags("links"), tyr.Errors(tyr.KindNotFound))
	admin := api.Group(tyr.Tags("admin"), tyr.Errors(tyr.KindUnauthenticated, tyr.KindPermissionDenied))
	admin.Handle("links.delete", func(ctx context.Context, req docGetReq) (struct{}, error) { return struct{}{}, nil },
		tyr.Deprecated())
	admin.Implement(tyr.Define[docPurgeReq, docPurged]("links.purge", tyr.Summary("Purge the links to a host")).
		Example("a host", docPurgeReq{Host: "example.com"}, docPurged{Purged: 2}),
		func(ctx context.Context, req docPurgeReq) (docPurged, error) { return docPurged{}, nil })
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

	// A batch gets the document once, whatever it asks: a small request
	// can't ask for many copies of it. A notification doesn't count.
	rec = post(h, `[{"jsonrpc":"2.0","method":"rpc.discover"},`+
		`{"jsonrpc":"2.0","method":"rpc.discover","id":"a"},`+
		`{"jsonrpc":"2.0","method":"rpc.discover","id":"b"},`+
		`{"jsonrpc":"2.0","method":"links.purge","params":{"host":"go.dev"},"id":"c"},`+
		`{"jsonrpc":"2.0","method":"rpc.discover","id":"d"}]`)
	var answers []struct {
		Result jsontext.Value `json:"result"`
		Error  jsontext.Value `json:"error"`
		ID     string         `json:"id"`
	}
	const again = `{"code":-32600,"message":"Invalid Request: rpc.discover is answered once per batch"}`
	if err := json.Unmarshal(rec.Body.Bytes(), &answers); err != nil || len(answers) != 4 ||
		answers[0].ID != "a" || answers[0].Result.Kind() != '{' ||
		answers[1].ID != "b" || string(answers[1].Error) != again ||
		answers[2].ID != "c" || string(answers[2].Result) != `{"purged":0}` ||
		answers[3].ID != "d" || string(answers[3].Error) != again {
		t.Errorf("batch = %s, want the document once, the result of links.purge and %s twice", rec.Body, again)
	}
}

func TestOpenRPCJSON(t *testing.T) {
	// The document that rpc.discover answers with, indented.
	info := tyr.Info{Title: "links", Version: "1.0.0", Description: "Short links."}
	got := jsonrpc.OpenRPCJSON(docAPI(), info)
	if want := discover(t, jsonrpc.Handler(docAPI(), jsonrpc.Discover(info))); string(got) != string(want) {
		t.Errorf("OpenRPCJSON = %s, want what rpc.discover answers: %s", got, want)
	}

	// It seals the API: an operation registered later would be missing.
	api := docAPI()
	jsonrpc.OpenRPCJSON(api, info)
	want := `tyr: Handle("links.late") after Seal: register everything before mounting the API`
	if got := panicValue(func() {
		api.Handle("links.late", func(ctx context.Context, req struct{}) (string, error) { return "", nil })
	}); got != want {
		t.Errorf("Handle after OpenRPCJSON panicked with %v, want %q", got, want)
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
		{"two types of one schema name", func() {
			type Inner struct {
				Name string `json:"name"`
			}
			api := newAPI()
			api.Handle("inner.ours", func(ctx context.Context, req struct{}) (Inner, error) { return Inner{}, nil })
			api.Handle("inner.theirs", func(ctx context.Context, req struct{}) (plantest.Inner, error) { return plantest.Inner{}, nil })
			jsonrpc.Handler(api, jsonrpc.Discover(info))
		}, `jsonrpc: Discover: two types have the schema name "Inner": github.com/tyr-go/tyr/jsonrpc_test.Inner and ` +
			`github.com/tyr-go/tyr/internal/plan/plantest.Inner; give one of them a method SchemaName() string`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(tt.f); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenRPCJSONPanics(t *testing.T) {
	info := tyr.Info{Title: "links", Version: "1.0.0"}
	type waitReq struct {
		For time.Duration `json:"for"`
	}
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"nil API", func() { jsonrpc.OpenRPCJSON(nil, info) }, "jsonrpc: OpenRPCJSON: nil API"},
		{"no title", func() { jsonrpc.OpenRPCJSON(newAPI(), tyr.Info{Version: "1.0.0"}) }, "jsonrpc: OpenRPCJSON: the Info needs a Title and a Version"},
		{"no version", func() { jsonrpc.OpenRPCJSON(newAPI(), tyr.Info{Title: "links"}) }, "jsonrpc: OpenRPCJSON: the Info needs a Title and a Version"},
		{"a field JSON can't carry", func() {
			api := newAPI()
			api.Handle("jobs.wait", func(ctx context.Context, req waitReq) (string, error) { return "", nil })
			jsonrpc.OpenRPCJSON(api, info)
		}, `jsonrpc: OpenRPCJSON: operation "jobs.wait": jsonrpc_test.waitReq.For: json/v2 has no representation of time.Duration`},
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
		fmt.Printf("method %s: %s\n", m.Name, m.Summary)
		for _, p := range m.Params {
			fmt.Printf("param %s, required: %v\n", p.Name, p.Required)
		}
		for _, e := range m.Errors {
			fmt.Printf("error %d: %s\n", e.Code, e.Message)
		}
	}
	// Output:
	// OpenRPC 1.4.1
	// method links.get: Get a link
	// param code, required: true
	// error -32602: invalid argument
	// error 404: not found
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

func TestDiscoverWithValidator(t *testing.T) {
	// With a validator of its own, a param is required as a zero value
	// fails the validator, and only the rules of the core add keywords.
	api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)), tyr.WithValidator(phoneValidator{}))
	api.Handle("calls.start", func(ctx context.Context, req phoneReq) (struct{}, error) { return struct{}{}, nil })
	var doc struct {
		Methods []struct {
			Params []struct {
				Name     string         `json:"name"`
				Required bool           `json:"required"`
				Schema   jsontext.Value `json:"schema"`
			} `json:"params"`
		} `json:"methods"`
	}
	if err := json.Unmarshal(discover(t, jsonrpc.Handler(api, jsonrpc.Discover(tyr.Info{Title: "calls", Version: "1.0.0"}))), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range doc.Methods[0].Params {
		schema, _ := jsontext.AppendFormat(nil, p.Schema)
		got = append(got, fmt.Sprintf("%s %v %s", p.Name, p.Required, schema))
	}
	want := []string{`phone true {"type":"string"}`, `name false {"type":"string","minLength":2}`}
	if !slices.Equal(got, want) {
		t.Errorf("params = %q, want %q", got, want)
	}
}
