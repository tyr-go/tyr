package tyr_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
	"github.com/tyr-go/tyr/rest"
)

// In an application, roles, requireRoles and authorize would live in an
// authz package, as authz.Require and authz.Interceptor.

var roles = tyr.NewMetaKey[[]string]("authz.roles")

// userRole carries the caller's role, as authentication middleware would
// set it.
var userRole = ctxkey.New[string]("user.role")

// requireRoles allows an operation only to callers with one of the roles.
func requireRoles(r ...string) tyr.OpOption {
	return roles.Option(r)
}

// authorize enforces the roles that operations require: a caller without
// a role isn't authenticated, and one with another role isn't allowed.
func authorize(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
	want, ok := roles.Get(op)
	if !ok {
		return next(ctx, req)
	}
	role, ok := userRole.Get(ctx)
	if !ok {
		return nil, tyr.Unauthenticated("log in first")
	}
	if !slices.Contains(want, role) {
		return nil, tyr.PermissionDenied("requires one of %v", want)
	}
	return next(ctx, req)
}

func ExampleMetaKey() {
	api := tyr.New()
	api.Use(authorize)
	admin := api.Group(requireRoles("admin"))
	purge := admin.Handle("links.purge", func(ctx context.Context, req struct{}) (string, error) {
		return "purged", nil
	})

	for _, role := range []string{"admin", "guest"} {
		res, err := purge.Call(userRole.Set(context.Background(), role), nil)
		fmt.Println(role, res, err)
	}
	// Output:
	// admin purged <nil>
	// guest <nil> permission_denied: requires one of [admin]
}

func ExampleInterceptor() {
	// authenticate is HTTP middleware: it finds the role of the caller by
	// the bearer token and puts it in the context.
	tokens := map[string]string{"s3cret": "admin", "p4ss": "user"}
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if role, ok := tokens[token]; ok {
				r = r.WithContext(userRole.Set(r.Context(), role))
			}
			next.ServeHTTP(w, r)
		})
	}

	api := tyr.New()
	api.Use(authorize) // over every transport, unlike middleware of a route
	admin := api.Group(roles.Option([]string{"admin"}))
	admin.Handle("links.purge", func(ctx context.Context, req struct{}) (string, error) {
		return "purged", nil
	}, rest.Route("POST /links/purge"))
	mux := http.NewServeMux()
	// RFC 9110 requires a challenge on every 401.
	rest.Mount(mux, api, rest.Challenge(`Bearer realm="links"`))
	handler := authenticate(mux)

	for _, token := range []string{"", "p4ss", "s3cret"} {
		req := httptest.NewRequest("POST", "/links/purge", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Body)
		if challenge := rec.Header().Get("WWW-Authenticate"); challenge != "" {
			fmt.Println("WWW-Authenticate:", challenge)
		}
	}
	// Output:
	// 401 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindUnauthenticated","title":"Unauthenticated","status":401,"detail":"log in first","kind":"unauthenticated"}
	// WWW-Authenticate: Bearer realm="links"
	// 403 {"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindPermissionDenied","title":"Permission Denied","status":403,"detail":"requires one of [admin]","kind":"permission_denied"}
	// 200 "purged"
}
