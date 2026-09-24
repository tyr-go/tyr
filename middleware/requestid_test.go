package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/middleware"
)

func TestRequestID(t *testing.T) {
	allowed := "ABCXYZabcxyz0189._:-"
	long := strings.Repeat(allowed, 7)[:128]

	tests := []struct {
		name   string
		header []string // the X-Request-ID values of the request
		keep   bool     // whether the request keeps the first one
	}{
		{name: "allowed characters", header: []string{allowed}, keep: true},
		{name: "one character", header: []string{"a"}, keep: true},
		{name: "128 characters", header: []string{long}, keep: true},
		{name: "first of two", header: []string{"first", "second"}, keep: true},
		{name: "none"},
		{name: "empty", header: []string{""}},
		{name: "129 characters", header: []string{long + "a"}},
		{name: "space", header: []string{"a b"}},
		{name: "merged by a proxy", header: []string{"a, b"}},
		{name: "slash", header: []string{"a/b"}},
		{name: "quote", header: []string{`a"b`}},
		{name: "not ASCII", header: []string{"é"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id, early string
			h := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, _ = tyr.RequestIDFrom(r.Context())
				early = w.Header().Get("X-Request-ID")
			}))
			req := httptest.NewRequest("GET", "/", nil)
			for _, v := range tt.header {
				req.Header.Add("X-Request-ID", v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// The response has the ID of the context, from before the handler.
			if got := rec.Header().Get("X-Request-ID"); got != id || early != id {
				t.Errorf("X-Request-ID = %q, %q when the handler ran; the context has %q", got, early, id)
			}
			if tt.keep {
				if id != tt.header[0] {
					t.Errorf("ID = %q, want %q", id, tt.header[0])
				}
				return
			}
			if u, err := uuid.Parse(id); err != nil || u.String() != id || id[14] != '7' {
				t.Errorf("ID = %q, want a new UUIDv7", id)
			}
		})
	}
}

func TestRequestIDGenerated(t *testing.T) {
	var ids []string
	h := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := tyr.RequestIDFrom(r.Context())
		ids = append(ids, id)
	}))
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}
	// A client that sends a generated ID on, to another service, keeps it.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", ids[0])
	h.ServeHTTP(httptest.NewRecorder(), req)

	if ids[0] >= ids[1] {
		t.Errorf("IDs %q and %q don't sort by time", ids[0], ids[1])
	}
	if ids[2] != ids[0] {
		t.Errorf("sent %q, got %q", ids[0], ids[2])
	}
}
