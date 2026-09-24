package authz_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tyr-go/tyr/examples/shortlink/authz"
)

func TestAuthenticate(t *testing.T) {
	admin := authz.Caller{Name: "admin", Roles: []string{"admin"}}
	h := authz.Authenticate(map[string]authz.Caller{"secret": admin})

	tests := []struct {
		header string // Authorization
		want   string // the name of the caller, "" if none
	}{
		{"Bearer secret", "admin"},
		{"bearer secret", "admin"},
		{"Bearer wrong", ""},
		{"Basic secret", ""},
		{"secret", ""},
		{"", ""},
	}
	for _, tt := range tests {
		var got string
		h(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _ := authz.CallerFrom(r.Context())
			got = c.Name
		})).ServeHTTP(httptest.NewRecorder(), func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", tt.header)
			return r
		}())
		if got != tt.want {
			t.Errorf("Authorization %q: caller %q, want %q", tt.header, got, tt.want)
		}
	}
}
