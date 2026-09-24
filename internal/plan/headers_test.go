package plan_test

import (
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyr-go/tyr/internal/plan"
)

// text formats itself for a header, with a pointer receiver.
type text struct{ s string }

func (t *text) MarshalText() ([]byte, error) {
	if t.s == "bad" {
		return nil, errors.New("bad text")
	}
	return []byte(t.s), nil
}

// Redirect is a result part that sets Location and has no JSON members.
type Redirect struct {
	Location string `json:"-" header:"Location"`
}

// withHeaders sets a header from a field of every type.
type withHeaders struct {
	Redirect
	ETag     string     `json:"etag" header:"ETag"`
	Count    int        `json:"count" header:"X-Count"`
	Ratio    float32    `json:"-" header:"X-Ratio"`
	OK       bool       `json:"-" header:"X-Ok"`
	Size     uint16     `json:"-" header:"X-Size"`
	Modified time.Time  `json:"-" header:"Last-Modified"`
	Expires  *time.Time `json:"-" header:"Expires"`
	Code     text       `json:"-" header:"X-Code"`
	Retry    *int       `json:"-" header:"Retry-After"`
	Plain    string     `json:"plain"`
}

func TestHeaders(t *testing.T) {
	h, err := plan.NewHeaders(reflect.TypeFor[withHeaders]())
	if err != nil {
		t.Fatalf("NewHeaders() error = %v", err)
	}
	tashkent := time.FixedZone("UZT", 5*60*60)
	res := withHeaders{
		Location: "/links/go",
		ETag:     "",
		Count:    0,
		Ratio:    0.5,
		OK:       true,
		Size:     7,
		Modified: time.Date(2026, 9, 23, 10, 0, 0, 0, tashkent),
		Code:     text{"go"},
		Plain:    "p",
	}
	// An empty string, a nil pointer and a zero time set no header; other
	// zero values do. A time is an HTTP date, in GMT.
	want := http.Header{
		"Location":      {"/links/go"},
		"X-Count":       {"0"},
		"X-Ratio":       {"0.5"},
		"X-Ok":          {"true"},
		"X-Size":        {"7"},
		"Last-Modified": {"Wed, 23 Sep 2026 05:00:00 GMT"},
		"X-Code":        {"go"},
	}
	for _, v := range []any{res, &res} {
		got, err := h.Of(reflect.ValueOf(v))
		if err != nil || !maps.EqualFunc(got, want, slices.Equal) {
			t.Errorf("Of(%T) = %v, %v; want %v", v, got, err, want)
		}
	}
	if got, err := h.Of(reflect.ValueOf((*withHeaders)(nil))); got != nil || err != nil {
		t.Errorf("Of(nil) = %v, %v; want no headers", got, err)
	}
	expires := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, err := h.Of(reflect.ValueOf(withHeaders{Expires: &expires, Retry: new(120)}))
	want = http.Header{
		"X-Count": {"0"}, "X-Ratio": {"0"}, "X-Ok": {"false"}, "X-Size": {"0"},
		"Expires": {"Thu, 01 Oct 2026 00:00:00 GMT"}, "Retry-After": {"120"},
	}
	if err != nil || !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Of() of zero values and pointers = %v, %v; want %v", got, err, want)
	}

	res.Code = text{"bad"}
	if _, err := h.Of(reflect.ValueOf(res)); err == nil || err.Error() != "field Code, header X-Code: bad text" {
		t.Errorf("Of() error = %v, want the error of MarshalText", err)
	}

	if !h.Has("location") || h.Has("Plain") {
		t.Errorf("Has(location) = %t, Has(Plain) = %t; want true, false", h.Has("location"), h.Has("Plain"))
	}
}

func TestHeadersEmbeddedPointer(t *testing.T) {
	type result struct {
		*Redirect
		Name string `json:"name"`
	}
	h, err := plan.NewHeaders(reflect.TypeFor[result]())
	if err != nil {
		t.Fatalf("NewHeaders() error = %v", err)
	}
	if got, err := h.Of(reflect.ValueOf(result{Name: "go"})); got != nil || err != nil {
		t.Errorf("Of() under a nil embedded pointer = %v, %v; want no headers", got, err)
	}
	got, err := h.Of(reflect.ValueOf(result{Redirect: &Redirect{Location: "/x"}}))
	if err != nil || got.Get("Location") != "/x" {
		t.Errorf("Of() = %v, %v; want Location /x", got, err)
	}
}

func TestHeadersOfRequestTags(t *testing.T) {
	// A type may be both a request and a result: path and query mean nothing
	// in a result, even on a field of a nested struct.
	type both struct {
		Code string `json:"code" path:"code"`
		Page struct {
			Limit int `json:"limit" query:"limit"`
		} `json:"page"`
		Match string `json:"-" header:"ETag"`
	}
	h, err := plan.NewHeaders(reflect.TypeFor[both]())
	if err != nil {
		t.Fatalf("NewHeaders() error = %v", err)
	}
	got, err := h.Of(reflect.ValueOf(both{Code: "go", Match: "v1"}))
	if err != nil || !maps.EqualFunc(got, http.Header{"Etag": {"v1"}}, slices.Equal) {
		t.Errorf("Of() = %v, %v; want only Etag", got, err)
	}
}

func TestHeadersOfOtherTypes(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[[]int](), reflect.TypeFor[*int]()} {
		h, err := plan.NewHeaders(typ)
		if err != nil {
			t.Fatalf("NewHeaders(%v) error = %v", typ, err)
		}
		if got, err := h.Of(reflect.ValueOf(reflect.New(typ).Elem().Interface())); got != nil || err != nil || h.Has("Location") {
			t.Errorf("headers of %v = %v, %v; want none", typ, got, err)
		}
	}
}

func TestNewHeadersErrors(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			"unexported field",
			reflect.TypeFor[struct {
				loc string `header:"Location"`
			}](),
			`field loc has header:"Location", but it is unexported`,
		},
		{
			"field of a nested struct",
			reflect.TypeFor[struct {
				Next Redirect `json:"next"`
			}](),
			`field Next.Location has header:"Location", but only fields of struct { Next plan_test.Redirect "json:\"next\"" } and of structs embedded in it can set headers`,
		},
		{
			"empty tag",
			reflect.TypeFor[struct {
				A string `header:""`
			}](),
			"field A has an empty header tag",
		},
		{
			"not a header name",
			reflect.TypeFor[struct {
				A string `header:"X Id"`
			}](),
			`field A has header:"X Id", which isn't a header name`,
		},
		{
			"header of the server",
			reflect.TypeFor[struct {
				Type string `header:"content-type"`
			}](),
			`field Type has header:"content-type", but the server sets Content-Type itself`,
		},
		{
			"repeated header in another case",
			reflect.TypeFor[struct {
				A string `header:"ETag"`
				B string `header:"etag"`
			}](),
			"fields A and B both set the header Etag",
		},
		{
			"unsupported type",
			reflect.TypeFor[struct {
				Links []string `header:"Link"`
			}](),
			`field Links has header:"Link", but its type []string can't set a header`,
		},
		{
			"pointer to a pointer",
			reflect.TypeFor[struct {
				P **string `header:"X-P"`
			}](),
			`field P has header:"X-P", but its type **string can't set a header`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := plan.NewHeaders(tt.typ); err == nil || err.Error() != tt.want {
				t.Errorf("NewHeaders() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// selfEncoding is a struct that encodes itself.
type selfEncoding struct{}

func (selfEncoding) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

func TestNoMembers(t *testing.T) {
	tests := []struct {
		typ  reflect.Type
		want bool
	}{
		{reflect.TypeFor[struct{}](), true},
		{reflect.TypeFor[Redirect](), true},
		{reflect.TypeFor[*Redirect](), true},
		{reflect.TypeFor[struct{ A string }](), false},
		{reflect.TypeFor[withHeaders](), false},
		{reflect.TypeFor[selfEncoding](), false},
		{reflect.TypeFor[time.Time](), false},
		{reflect.TypeFor[string](), false},
	}
	for _, tt := range tests {
		if got := plan.NoMembers(tt.typ); got != tt.want {
			t.Errorf("NoMembers(%v) = %t, want %t", tt.typ, got, tt.want)
		}
	}
}

func TestBindHeaderTime(t *testing.T) {
	type times struct {
		Since    time.Time  `json:"since" header:"If-Modified-Since"`
		SincePtr *time.Time `json:"since_ptr" header:"X-Since"`
		At       time.Time  `json:"at" query:"at"`
	}
	b, err := plan.NewBinding(reflect.TypeFor[times]())
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	bind := func(src plan.Source, value string) (times, []string) {
		var v times
		problems := b.Bind(reflect.ValueOf(&v).Elem(), func(s plan.Source, name string) ([]string, bool) {
			return []string{value}, s == src
		})
		var details []string
		for _, p := range problems {
			details = append(details, p.Field.GoName+": "+p.Detail)
		}
		return v, details
	}

	want := time.Date(1994, 11, 6, 8, 49, 37, 0, time.UTC)
	for _, value := range []string{
		"Sun, 06 Nov 1994 08:49:37 GMT",  // IMF-fixdate
		"Sunday, 06-Nov-94 08:49:37 GMT", // RFC 850
		"Sun Nov  6 08:49:37 1994",       // ANSI C asctime
	} {
		got, problems := bind(plan.Header, value)
		if problems != nil || got.Since != want || got.SincePtr == nil || *got.SincePtr != want {
			t.Errorf("header %q: bound %v, %v, %q; want %v in UTC", value, got.Since, got.SincePtr, problems, want)
		}
	}
	// A header of one's own may have an RFC 3339 time.
	got, problems := bind(plan.Header, "1994-11-06T13:49:37+05:00")
	if problems != nil || !got.Since.Equal(want) {
		t.Errorf("header in RFC 3339: bound %v, %q; want %v", got.Since, problems, want)
	}
	if _, problems := bind(plan.Header, "yesterday"); !slices.Equal(problems, []string{
		"Since: must be an HTTP date or an RFC 3339 time",
		"SincePtr: must be an HTTP date or an RFC 3339 time",
	}) {
		t.Errorf("header yesterday: problems %q", problems)
	}
	// The query has RFC 3339 times only.
	if _, problems := bind(plan.Query, "Sun, 06 Nov 1994 08:49:37 GMT"); len(problems) != 1 || !strings.HasSuffix(problems[0], "must be an RFC 3339 time") {
		t.Errorf("HTTP date in the query: problems %q, want one about RFC 3339", problems)
	}
}

func TestHeadersFields(t *testing.T) {
	type base struct {
		ETag string `header:"etag"`
	}
	type res struct {
		base
		Location *string   `json:"-" header:"Location"`
		Modified time.Time `json:"modified" header:"Last-Modified"`
		Plain    string    `json:"plain"`
	}
	h, err := plan.NewHeaders(reflect.TypeFor[*res]())
	if err != nil {
		t.Fatal(err)
	}
	want := []plan.HeaderField{
		{Name: "Etag", Index: []int{0, 0}, Type: reflect.TypeFor[string]()},
		{Name: "Location", Index: []int{1}, Type: reflect.TypeFor[*string]()},
		{Name: "Last-Modified", Index: []int{2}, Type: reflect.TypeFor[time.Time]()},
	}
	if got := h.Fields(); !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() = %+v, want %+v", got, want)
	}
}
