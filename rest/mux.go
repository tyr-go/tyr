package rest

import (
	"context"
	"log/slog"
	"net/http"
)

// ProblemHandler returns a handler that serves mux, except that where mux
// itself replies 404 Not Found, for a path it has no pattern for, or 405
// Method Not Allowed, it writes that problem, as [WriteProblem] does,
// keeping the Allow header of 405. Any other reply of mux itself, such as a
// redirect to a clean path, goes through as it is, as do the responses of
// its routes. ProblemHandler panics if mux is nil.
func ProblemHandler(mux *http.ServeMux) http.Handler {
	if mux == nil {
		panic("rest: ProblemHandler: nil mux")
	}
	return problemHandler(mux)
}

// muxer is what ProblemHandler needs of a ServeMux. Tests fake it, since
// ServeMux replies differently in different versions of Go.
type muxer interface {
	Handler(r *http.Request) (h http.Handler, pattern string)
	http.Handler
}

// problemHandler implements ProblemHandler.
func problemHandler(mux muxer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the replies of the mux itself have an empty pattern.
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r) // so that the request gets its pattern and wildcards
			return
		}
		h.ServeHTTP(&problemWriter{ResponseWriter: w, ctx: r.Context()}, r)
	})
}

// problemWriter passes on a reply of a mux itself, but writes a 404 or 405
// as a problem, in place of the text of the mux.
type problemWriter struct {
	http.ResponseWriter
	ctx     context.Context
	sent    bool // the reply of the mux has started to go out
	problem bool // it was a 404 or 405, sent as a problem
}

func (p *problemWriter) WriteHeader(status int) {
	switch {
	case p.problem:
	case !p.sent && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed):
		p.problem = true
		writeProblem(p.ctx, slog.Default(), p.ResponseWriter, blank(status, ""))
	default:
		p.sent = true
		p.ResponseWriter.WriteHeader(status)
	}
}

func (p *problemWriter) Write(b []byte) (int, error) {
	if p.problem {
		return len(b), nil // the text of the mux, which the problem replaced
	}
	p.sent = true
	return p.ResponseWriter.Write(b)
}

// Unwrap returns the writer under p, for http.ResponseController.
func (p *problemWriter) Unwrap() http.ResponseWriter {
	return p.ResponseWriter
}
