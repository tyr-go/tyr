package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

type CreateLinkReq struct {
	URL  string `json:"url" validate:"required,http_url"`
	Code string `json:"code" validate:"omitempty,min=4,max=16"`
}

var codeRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Validate holds rules that struct tags can't express: cross-field checks
// and regular expressions.
func (r CreateLinkReq) Validate() error {
	var v tyr.Violations
	if r.Code != "" && !codeRe.MatchString(r.Code) {
		v.Add("code", "only a-z, 0-9 and '-'")
	}
	return v.Err()
}

func ExampleViolations() {
	fmt.Println(CreateLinkReq{URL: "https://go.dev", Code: "go"}.Validate())

	err := CreateLinkReq{URL: "https://go.dev", Code: "Go!"}.Validate()
	fmt.Println(err)
	if e, ok := errors.AsType[*tyr.Error](err); ok {
		for _, v := range e.Details.(tyr.Violations) {
			fmt.Println(v.Pointer, v.Detail)
		}
	}
	// Output:
	// <nil>
	// invalid_argument: validation failed
	// /code only a-z, 0-9 and '-'
}

func Example_validation() {
	api := tyr.New()
	api.Handle("links.create", func(ctx context.Context, req CreateLinkReq) (string, error) {
		return req.Code, nil
	}, rest.Route("POST /links"))
	mux := http.NewServeMux()
	rest.Mount(mux, api)

	for _, body := range []string{
		`{"url":"ftp://go.dev","code":"go"}`,     // fails the tags
		`{"url":"https://go.dev","code":"Go!!"}`, // fails Validate
		`{"url":"https://go.dev","code":"gopher"}`,
	} {
		req := httptest.NewRequest("POST", "/links", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Body)
	}
	// Output:
	// 400 {"type":"about:blank","title":"Bad Request","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/url","detail":"must be an http or https URL"},{"pointer":"/code","detail":"must be at least 4 characters"}]}
	// 400 {"type":"about:blank","title":"Bad Request","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/code","detail":"only a-z, 0-9 and '-'"}]}
	// 200 "gopher"
}
