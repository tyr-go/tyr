package tyr_test

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"testing"

	"github.com/tyr-go/tyr"
)

type getLinkReq struct {
	Code string `json:"code"`
}

type link struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

func getLink(ctx context.Context, req getLinkReq) (*link, error) {
	return &link{Code: req.Code, URL: "https://go.dev/" + req.Code}, nil
}

type linkService struct{}

func (linkService) Get(ctx context.Context, req getLinkReq) (*link, error) {
	return getLink(ctx, req)
}

func TestHandle(t *testing.T) {
	api := tyr.New()
	ops := []*tyr.Operation{
		api.Handle("links.get", getLink),
		api.Handle("links.method", linkService{}.Get),
		api.Handle("links.closure", func(ctx context.Context, req getLinkReq) (string, error) {
			return req.Code, nil
		}),
	}

	want := []struct {
		name     string
		req, res reflect.Type
	}{
		{"links.get", reflect.TypeFor[getLinkReq](), reflect.TypeFor[*link]()},
		{"links.method", reflect.TypeFor[getLinkReq](), reflect.TypeFor[*link]()},
		{"links.closure", reflect.TypeFor[getLinkReq](), reflect.TypeFor[string]()},
	}
	for i, op := range ops {
		if op.Name() != want[i].name || op.Req() != want[i].req || op.Res() != want[i].res {
			t.Errorf("operation %d = %s(%v) %v, want %s(%v) %v",
				i, op.Name(), op.Req(), op.Res(), want[i].name, want[i].req, want[i].res)
		}
	}
	if got := slices.Collect(api.Operations()); !slices.Equal(got, ops) {
		t.Errorf("Operations() = %v, want %v", names(got), names(ops))
	}
}

func TestHandleNames(t *testing.T) {
	const (
		invalid  = "name must be dot-separated segments of ASCII letters, digits, '_' and '-'"
		reserved = `the first segment "rpc" is reserved by JSON-RPC`
	)
	tests := []struct {
		name string
		want string // what's wrong with the name, or "" if it's valid
	}{
		{"links", ""},
		{"links.get", ""},
		{"v2.links.get_by-code", ""},
		{"rpcx.status", ""},
		{"RPC.status", ""}, // JSON-RPC reserves only the lowercase prefix
		{"", invalid},
		{".links", invalid},
		{"links.", invalid},
		{"links..get", invalid},
		{"links get", invalid},
		{"links/get", invalid},
		{"ссылки.get", invalid},
		{"rpc", reserved},
		{"rpc.discover", reserved},
	}
	for _, tt := range tests {
		var want any
		if tt.want != "" {
			want = fmt.Sprintf("tyr: Handle(%q): %s", tt.name, tt.want)
		}
		if got := panicValue(func() { tyr.New().Handle(tt.name, getLink) }); got != want {
			t.Errorf("Handle(%q) panicked with %v, want %v", tt.name, got, want)
		}
	}
}

func TestHandlePanics(t *testing.T) {
	tests := []struct {
		name     string
		register func(api *tyr.API)
		want     string
	}{
		{
			name: "duplicate name",
			register: func(api *tyr.API) {
				api.Handle("links.get", getLink)
				api.Group().Handle("links.get", getLink)
			},
			want: `tyr: Handle("links.get"): duplicate operation name`,
		},
		{
			name:     "nil handler",
			register: func(api *tyr.API) { api.Handle("links.get", tyr.Handler[getLinkReq, *link](nil)) },
			want:     `tyr: Handle("links.get"): nil handler`,
		},
		{
			name: "pointer request",
			register: func(api *tyr.API) {
				api.Handle("links.get", func(ctx context.Context, req *getLinkReq) (*link, error) { return nil, nil })
			},
			want: `tyr: Handle("links.get"): request type *tyr_test.getLinkReq is not a struct; use tyr_test.getLinkReq`,
		},
		{
			name: "non-struct request",
			register: func(api *tyr.API) {
				api.Handle("links.get", func(ctx context.Context, code string) (*link, error) { return nil, nil })
			},
			want: `tyr: Handle("links.get"): request type string is not a struct`,
		},
		{
			name:     "nil option",
			register: func(api *tyr.API) { api.Handle("links.get", getLink, nil) },
			want:     `tyr: Handle("links.get"): nil option`,
		},
		{
			name:     "nil group option",
			register: func(api *tyr.API) { api.Group(nil).Handle("links.get", getLink) },
			want:     `tyr: Handle("links.get"): nil option`,
		},
		{
			name:     "nil mapper",
			register: func(api *tyr.API) { api.MapError(nil) },
			want:     "tyr: MapError: nil mapper",
		},
		{
			name:     "nil interceptor",
			register: func(api *tyr.API) { api.Use(passThrough, nil) },
			want:     "tyr: Use: nil interceptor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(func() { tt.register(tyr.New()) }); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

func TestImplement(t *testing.T) {
	var applied []string
	option := func(tag string) tyr.OpOption {
		return func(op *tyr.Operation) { applied = append(applied, op.Name()+" "+tag) }
	}
	opts := []tyr.OpOption{option("contract")}
	get := tyr.Define[getLinkReq, *link]("links.get", opts...)
	opts[0] = option("changed") // the contract keeps its own copy

	// One contract, two APIs, as a server and a test of its clients have.
	api := tyr.New()
	op := api.Group(option("admin")).Implement(get, getLink)
	other := tyr.New().Implement(get, linkService{}.Get)

	for _, op := range []*tyr.Operation{op, other} {
		if op.Name() != "links.get" || op.Req() != reflect.TypeFor[getLinkReq]() || op.Res() != reflect.TypeFor[*link]() {
			t.Errorf("operation = %s(%v) %v, want links.get(%v) %v",
				op.Name(), op.Req(), op.Res(), reflect.TypeFor[getLinkReq](), reflect.TypeFor[*link]())
		}
	}
	// The group's options go first, as with Handle.
	want := []string{"links.get admin", "links.get contract", "links.get contract"}
	if !slices.Equal(applied, want) {
		t.Errorf("applied options:\n%q\nwant:\n%q", applied, want)
	}
	if got := slices.Collect(api.Operations()); !slices.Equal(got, []*tyr.Operation{op}) {
		t.Errorf("Operations() = %v, want [links.get]", names(got))
	}

	res, err := other.Call(t.Context(), func(dst any) error {
		dst.(*getLinkReq).Code = "go"
		return nil
	})
	if l, ok := res.(*link); !ok || err != nil || l.URL != "https://go.dev/go" {
		t.Errorf("Call() = %v, %v; want the link of go", res, err)
	}
}

func TestImplementPanics(t *testing.T) {
	get := tyr.Define[getLinkReq, *link]("links.get")
	tests := []struct {
		name      string
		implement func(api *tyr.API)
		want      string
	}{
		{
			name:      "zero Contract",
			implement: func(api *tyr.API) { api.Implement(tyr.Contract[getLinkReq, *link]{}, getLink) },
			want:      "tyr: Implement: zero Contract, make one with Define",
		},
		{
			name:      "zero Contract in a group",
			implement: func(api *tyr.API) { api.Group().Implement(tyr.Contract[getLinkReq, *link]{}, getLink) },
			want:      "tyr: Implement: zero Contract, make one with Define",
		},
		{
			name: "duplicate name",
			implement: func(api *tyr.API) {
				api.Handle("links.get", getLink)
				api.Implement(get, getLink)
			},
			want: `tyr: Implement("links.get"): duplicate operation name`,
		},
		{
			name:      "nil handler",
			implement: func(api *tyr.API) { api.Implement(get, nil) },
			want:      `tyr: Implement("links.get"): nil handler`,
		},
		{
			name: "bad validate tag",
			implement: func(api *tyr.API) {
				type badReq struct {
					Code string `json:"code" validate:"required,maxx=4"`
				}
				api.Implement(tyr.Define[badReq, *link]("links.get"), func(ctx context.Context, req badReq) (*link, error) {
					return nil, nil
				})
			},
			want: `tyr: Implement("links.get"): field Code: validate:"required,maxx=4": unknown rule "maxx"; ` +
				`check the tags with tyr.WithValidator(playground.New()) of the module validate/playground for more rules, or move the check to Validate()`,
		},
		{
			name:      "nil group option",
			implement: func(api *tyr.API) { api.Group(nil).Implement(get, getLink) },
			want:      `tyr: Implement("links.get"): nil option`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(func() { tt.implement(tyr.New()) }); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}

func TestGroup(t *testing.T) {
	var applied []string
	option := func(tag string) tyr.OpOption {
		return func(op *tyr.Operation) { applied = append(applied, op.Name()+" "+tag) }
	}

	api := tyr.New()
	opts := []tyr.OpOption{option("admin")}
	admin := api.Group(opts...)
	opts[0] = option("changed") // the group keeps its own copy
	admin.Group(option("audit")).Handle("links.purge", getLink, option("own"))
	admin.Group(option("beta")).Handle("links.stats", getLink)
	admin.Handle("links.list", getLink)
	api.Handle("links.get", getLink, option("own"))

	want := []string{
		"links.purge admin", "links.purge audit", "links.purge own",
		"links.stats admin", "links.stats beta",
		"links.list admin",
		"links.get own",
	}
	if !slices.Equal(applied, want) {
		t.Errorf("applied options:\n%q\nwant:\n%q", applied, want)
	}
	wantOps := []string{"links.purge", "links.stats", "links.list", "links.get"}
	if got := names(slices.Collect(api.Operations())); !slices.Equal(got, wantOps) {
		t.Errorf("Operations() = %v, want %v", got, wantOps)
	}
}

func TestSeal(t *testing.T) {
	api := tyr.New()
	get := api.Handle("links.get", getLink)

	// Operations is a plain read, so registering after it is fine.
	_ = slices.Collect(api.Operations())
	api.Handle("links.list", getLink)

	api.Seal()
	api.Seal() // sealing again does nothing

	const after = " after Seal: register everything before mounting the API"
	late := tyr.Define[getLinkReq, *link]("links.late")
	tests := []struct {
		name string
		call func()
		want string
	}{
		{"Handle", func() { api.Handle("links.late", getLink) }, `tyr: Handle("links.late")` + after},
		{"Group.Handle", func() { api.Group().Handle("links.late", getLink) }, `tyr: Handle("links.late")` + after},
		{"Implement", func() { api.Implement(late, getLink) }, `tyr: Implement("links.late")` + after},
		{"Group.Implement", func() { api.Group().Implement(late, getLink) }, `tyr: Implement("links.late")` + after},
		{"MapError", func() { api.MapError(func(err error) error { return err }) }, "tyr: MapError" + after},
		{"Use", func() { api.Use(passThrough) }, "tyr: Use" + after},
	}
	for _, tt := range tests {
		if got := panicValue(tt.call); got != tt.want {
			t.Errorf("%s on a sealed API panicked with %v, want %q", tt.name, got, tt.want)
		}
	}

	// A sealed API still lists and runs its operations.
	wantOps := []string{"links.get", "links.list"}
	if got := names(slices.Collect(api.Operations())); !slices.Equal(got, wantOps) {
		t.Errorf("Operations() = %v, want %v", got, wantOps)
	}
	if _, err := get.Call(t.Context(), nil); err != nil {
		t.Errorf("Call() on a sealed API = %v, want <nil>", err)
	}
}

// TestAPILogger replaces the global default logger, so it must not run in
// parallel with other tests.
func TestAPILogger(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if got := tyr.New(tyr.WithLogger(logger)).Logger(); got != logger {
		t.Errorf("Logger() = %p, want the logger of WithLogger, %p", got, logger)
	}

	// Without WithLogger, Logger returns slog.Default as it is at the time
	// of the call, so it sees a default set after the API was created.
	api := tyr.New()
	def := slog.New(slog.DiscardHandler)
	useDefaultLogger(t, def)
	if got := api.Logger(); got != def {
		t.Errorf("Logger() = %p, want the current default logger, %p", got, def)
	}
}

// names returns the names of ops.
func names(ops []*tyr.Operation) []string {
	var s []string
	for _, op := range ops {
		s = append(s, op.Name())
	}
	return s
}

// panicValue calls f and returns the value it panicked with, or nil.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}
