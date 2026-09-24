package middleware

import (
	"net/http"
	"uuid"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/reqid"
)

// RequestID returns a middleware that gives every request an ID. It keeps
// the X-Request-ID of the request if it is 1 to 128 characters of
// [A-Za-z0-9._:-], and makes a new UUIDv7, which sorts by time, otherwise:
// the header comes from the client, and the ID goes into every log record
// of the request. The ID goes into the X-Request-ID header of the
// response, before the handler runs, and into the request context with
// [tyr.WithRequestID], where [tyr.RequestIDFrom] and [tyr.NewLogHandler]
// find it.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(reqid.Header)
			if !reqid.Valid(id) {
				id = uuid.NewV7().String()
			}
			w.Header().Set(reqid.Header, id)
			next.ServeHTTP(w, r.WithContext(tyr.WithRequestID(r.Context(), id)))
		})
	}
}
