package tyr_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"regexp"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestDoc(t *testing.T) {
	api := tyr.New()
	if got := api.Handle("links.get", getLink).Doc(); !reflect.DeepEqual(got, tyr.Doc{}) {
		t.Errorf("Doc() without options = %+v, want the zero Doc", got)
	}

	// Summary, Description and Deprecated: the last one wins. Tags, Errors
	// and Example add to those of the groups, without repeats.
	admin := api.Group(
		tyr.Summary("An admin operation"),
		tyr.Tags("admin"),
		tyr.Errors(tyr.KindUnauthenticated, tyr.KindPermissionDenied),
		tyr.Example("admin", getLinkReq{Code: "admin"}, &link{Code: "admin", URL: "https://go.dev/admin"}),
	)
	audited := admin.Group(tyr.Tags("audit", "admin"), tyr.Deprecated())
	op := audited.Handle("links.purge", getLink,
		tyr.Summary("Purge links"),
		tyr.Description("Deletes the links to a *host*."),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindNotFound, tyr.KindUnauthenticated),
		tyr.Example("go", getLinkReq{Code: "go"}, &link{Code: "go", URL: "https://go.dev/go"}),
	)
	want := tyr.Doc{
		Summary:     "Purge links",
		Description: "Deletes the links to a *host*.",
		Tags:        []string{"admin", "audit", "links"},
		Deprecated:  true,
		Errors:      []tyr.Kind{tyr.KindUnauthenticated, tyr.KindPermissionDenied, tyr.KindNotFound},
		Examples: []tyr.ExampleCall{
			{Name: "admin", Req: getLinkReq{Code: "admin"}, Res: &link{Code: "admin", URL: "https://go.dev/admin"}},
			{Name: "go", Req: getLinkReq{Code: "go"}, Res: &link{Code: "go", URL: "https://go.dev/go"}},
		},
	}
	if got := op.Doc(); !reflect.DeepEqual(got, want) {
		t.Errorf("Doc() = %+v\nwant %+v", got, want)
	}

	// The operations of a group don't share what they add.
	if got := admin.Handle("links.stats", getLink).Doc(); !reflect.DeepEqual(got.Tags, []string{"admin"}) || got.Deprecated || len(got.Examples) != 1 {
		t.Errorf("Doc() of another operation of the group = %+v, want only the group's", got)
	}
}

func TestOpExample(t *testing.T) {
	get := tyr.Define[getLinkReq, *link]("links.get", tyr.Summary("Get a link")).
		Example("go", getLinkReq{Code: "go"}, &link{Code: "go", URL: "https://go.dev/go"})
	more := get.Example("rust", getLinkReq{Code: "rust"}, nil) // a copy: get keeps one example

	op := tyr.New().Implement(get, getLink)
	other := tyr.New().Implement(more, getLink)
	if got := op.Doc(); got.Summary != "Get a link" || len(got.Examples) != 1 || got.Examples[0].Name != "go" {
		t.Errorf("Doc() = %+v, want the summary and the example go", got)
	}
	if got := other.Doc().Examples; len(got) != 2 || got[1].Name != "rust" || got[1].Res != (*link)(nil) {
		t.Errorf("Doc().Examples of the copy = %+v, want go and rust", got)
	}
}

// createReq has validate tags and a Validate method.
type createReq struct {
	URL  string `json:"url" validate:"required,http_url"`
	Code string `json:"code" validate:"omitempty,min=4"`
}

var codeChars = regexp.MustCompile(`^[a-z0-9-]*$`)

func (r createReq) Validate() error {
	var v tyr.Violations
	if !codeChars.MatchString(r.Code) {
		v.Add("code", "only a-z, 0-9 and '-'")
	}
	return v.Err()
}

// oddReq fails its Validate method with an error that has no violations.
type oddReq struct {
	N int `json:"n"`
}

func (r oddReq) Validate() error {
	if r.N%2 == 1 {
		return errors.New("n must be even")
	}
	return nil
}

func TestDocPanics(t *testing.T) {
	create := func(ctx context.Context, req createReq) (*link, error) { return nil, nil }
	_, nan := json.Marshal(math.NaN())
	tests := []struct {
		name string
		f    func(api *tyr.API)
		want string
	}{
		{
			name: "unknown kind",
			f:    func(api *tyr.API) { tyr.Errors(tyr.KindNotFound, tyr.Kind(42)) },
			want: "tyr: Errors: unknown kind Kind(42)",
		},
		{
			name: "empty tag",
			f:    func(api *tyr.API) { tyr.Tags("links", "") },
			want: "tyr: Tags: empty tag",
		},
		{
			name: "example without a name",
			f:    func(api *tyr.API) { tyr.Example("", getLinkReq{}, &link{}) },
			want: "tyr: Example: empty name",
		},
		{
			name: "contract example without a name",
			f:    func(api *tyr.API) { tyr.Define[getLinkReq, *link]("links.get").Example("", getLinkReq{}, nil) },
			want: "tyr: Example: empty name",
		},
		{
			name: "request of another type",
			f: func(api *tyr.API) {
				api.Handle("links.get", getLink, tyr.Example("go", createReq{URL: "https://go.dev"}, &link{}))
			},
			want: `tyr: Handle("links.get"): example "go": request is tyr_test.createReq, want tyr_test.getLinkReq`,
		},
		{
			name: "result of another type",
			f: func(api *tyr.API) {
				api.Handle("links.get", getLink, tyr.Example("go", getLinkReq{Code: "go"}, link{}))
			},
			want: `tyr: Handle("links.get"): example "go": result is tyr_test.link, want *tyr_test.link`,
		},
		{
			name: "name taken in a group",
			f: func(api *tyr.API) {
				api.Group(tyr.Example("go", getLinkReq{}, &link{})).Handle("links.get", getLink, tyr.Example("go", getLinkReq{}, &link{}))
			},
			want: `tyr: Handle("links.get"): example "go": another example has the name`,
		},
		{
			name: "request that fails a tag",
			f: func(api *tyr.API) {
				api.Implement(tyr.Define[createReq, *link]("links.create").Example("short", createReq{URL: "https://go.dev", Code: "go"}, nil), create)
			},
			want: `tyr: Implement("links.create"): example "short": the request fails validation: /code: must be at least 4 characters`,
		},
		{
			name: "request that fails Validate",
			f: func(api *tyr.API) {
				api.Implement(tyr.Define[createReq, *link]("links.create").Example("upper", createReq{URL: "https://go.dev", Code: "GO-DOCS"}, nil), create)
			},
			want: `tyr: Implement("links.create"): example "upper": the request fails validation: /code: only a-z, 0-9 and '-'`,
		},
		{
			name: "request that fails Validate without violations",
			f: func(api *tyr.API) {
				api.Handle("numbers.half", func(ctx context.Context, req oddReq) (int, error) { return req.N / 2, nil },
					tyr.Example("odd", oddReq{N: 3}, 1))
			},
			want: `tyr: Handle("numbers.half"): example "odd": the request fails validation: n must be even`,
		},
		{
			name: "result that can't be encoded",
			f: func(api *tyr.API) {
				api.Handle("numbers.sqrt", func(ctx context.Context, req oddReq) (float64, error) { return 0, nil },
					tyr.Example("nan", oddReq{N: -2}, math.NaN()))
			},
			want: `tyr: Handle("numbers.sqrt"): example "nan": encoding the result: ` + nan.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panicValue(func() { tt.f(tyr.New()) }); got != tt.want {
				t.Errorf("panicked with %v, want %q", got, tt.want)
			}
		})
	}
}
