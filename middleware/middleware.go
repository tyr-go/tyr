// Package middleware provides HTTP middleware for services built on tyr:
// [RequestID], [Logger], [Recover] and [CORS]. A middleware is a
// func(http.Handler) http.Handler, so these mix with the middleware of the
// standard library and other packages, and [Chain] applies them, the first
// outermost:
//
//	handler := middleware.Chain(mux,
//		middleware.RequestID(),
//		middleware.Logger(slog.Default()),
//		middleware.Recover(slog.Default()),
//		http.NewCrossOriginProtection().Handler,
//	)
//
// Keep this order. RequestID goes first, so that every response carries the
// request ID, the 500 of Recover included, and every record below it has
// the ID, given a logger with a [tyr.NewLogHandler]. Logger goes above
// Recover to see that 500. Recover goes below both, over everything that
// may panic; [tyr.Operation.Call] recovers the panics of operations itself.
// Middleware of your own may go below all three even if it passes on
// another request, as authentication does to put the caller in the
// context: Logger gets the route and the operation of a request from the
// transport, see [tyr.RequestInfo].
//
// Authorization doesn't belong in middleware: an operation served both
// over REST and JSON-RPC would get past a check on its route. Check it in
// an interceptor, see [tyr.API.Use].
//
// # Cross-origin requests
//
// [CORS] lets the pages of other origins call the service from browsers.
// Its middleware goes between Logger and Recover, and the protection
// against cross-site requests trusts the same origins:
//
//	cors := middleware.CORS{Origins: []string{"https://app.example.com"}}
//	handler := middleware.Chain(mux,
//		middleware.RequestID(),
//		middleware.Logger(slog.Default()),
//		cors.Handler,
//		middleware.Recover(slog.Default()),
//		cors.CrossOriginProtection().Handler,
//	)
//
// Below Logger, the preflight requests that CORS answers itself are
// logged. Above Recover, its headers stay on the 500 of Recover, which
// keeps the headers of the middleware above it, so a page can read that
// error too.
//
// # Response writers
//
// Logger and Recover pass a wrapped [http.ResponseWriter] on. It has an
// Unwrap method, so [http.ResponseController] gets to the writer under it,
// and a Flush method for code that finds [http.Flusher] by a type
// assertion. If the writer under it implements [http.Hijacker], as the
// HTTP/1 one does, the wrapped writer does too; the HTTP/2 one doesn't.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
)

// Chain returns h wrapped in mws, the first outermost: Chain(h, a, b) is
// a(b(h)). A nil h, a nil middleware or a middleware that returns nil
// panics.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	if h == nil {
		panic("middleware: Chain: nil handler")
	}
	for i, mw := range mws {
		if mw == nil {
			panic(fmt.Sprintf("middleware: Chain: nil middleware at index %d", i))
		}
	}
	for i, mw := range slices.Backward(mws) {
		if h = mw(h); h == nil {
			panic(fmt.Sprintf("middleware: Chain: middleware at index %d returned nil", i))
		}
	}
	return h
}

// logger returns l, or slog.Default() as it is now if l is nil.
func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}
