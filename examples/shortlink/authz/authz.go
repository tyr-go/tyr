// Package authz authenticates the callers of the service by bearer tokens
// and lets them call the operations whose role they have.
//
// Authentication is HTTP middleware: it reads a header. Authorization is a
// tyr interceptor, so that it guards an operation over every transport.
package authz

import (
	"context"
	"crypto/sha256"
	"net/http"
	"slices"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/ctxkey"
)

// Caller is an authenticated caller.
type Caller struct {
	Name  string
	Roles []string
}

var callerKey = ctxkey.New[Caller]("authz.caller")

// WithCaller returns a copy of ctx that carries c, as Authenticate makes.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return callerKey.Set(ctx, c)
}

// CallerFrom returns the caller that ctx carries.
func CallerFrom(ctx context.Context) (Caller, bool) {
	return callerKey.Get(ctx)
}

// Authenticate returns HTTP middleware that finds the caller of a request
// by the bearer token of its Authorization header in callers, a map from
// tokens, and puts it into the request context. A request without a token
// of callers goes on without a caller.
func Authenticate(callers map[string]Caller) func(http.Handler) http.Handler {
	// Look tokens up by their hashes, so that the time of a lookup tells
	// nothing about them.
	byHash := make(map[[sha256.Size]byte]Caller, len(callers))
	for token, c := range callers {
		byHash[sha256.Sum256([]byte(token))] = c
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			if c, ok := byHash[sha256.Sum256([]byte(token))]; ok && strings.EqualFold(scheme, "Bearer") {
				r = r.WithContext(WithCaller(r.Context(), c))
			}
			next.ServeHTTP(w, r)
		})
	}
}

var roleKey = tyr.NewMetaKey[string]("authz.role")

// Require returns an option for operations that only callers with the role
// may call. Interceptor enforces it.
func Require(role string) tyr.OpOption {
	return roleKey.Option(role)
}

// Interceptor rejects a call of an operation that requires a role unless
// the caller has it.
func Interceptor(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
	role, ok := roleKey.Get(op)
	if !ok {
		return next(ctx, req)
	}
	c, ok := CallerFrom(ctx)
	if !ok {
		return nil, tyr.Unauthenticated("a valid bearer token is required")
	}
	if !slices.Contains(c.Roles, role) {
		return nil, tyr.PermissionDenied("%s requires the role %s", op.Name(), role)
	}
	return next(ctx, req)
}
