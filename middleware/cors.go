package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// forbidden is the problem that CORS sends to a preflight request it
// doesn't allow.
const forbidden = `{"type":"about:blank","title":"Forbidden","status":403}`

// CORS is a policy of cross-origin requests from browsers, by the rules of
// the Fetch standard: the origins whose pages may call the service and
// read its responses. [CORS.Handler] is its middleware, and
// [CORS.CrossOriginProtection] makes a protection against cross-site
// requests that trusts the same origins, so that the two don't drift
// apart; see the package documentation for their place in a chain.
//
//	cors := middleware.CORS{
//		Origins: []string{"https://app.example.com"},
//		Expose:  []string{"Location", "X-Request-ID"},
//		MaxAge:  time.Hour,
//	}
//
// The origins are the boundary. A page of an allowed origin may use the
// methods and send the headers it asks for in a preflight request: which
// methods a route takes is for the routes to say, with 405, and who may do
// what is for interceptors.
type CORS struct {
	// Origins are the origins whose pages may call the service, each as
	// browsers send it in the Origin header: a scheme and a host, with a
	// port unless it's the default one, in lower case, such as
	// "https://app.example.com" or "http://localhost:5173". "*" allows any
	// origin; it goes alone and without Credentials. With no origins, no
	// page of another origin may call the service.
	Origins []string

	// Credentials lets the pages send cookies, HTTP authentication and TLS
	// client certificates with their requests. A token that a page puts in
	// the Authorization header needs no credentials.
	Credentials bool

	// Expose are the headers of responses that the pages may read beyond
	// the safelisted ones, such as Content-Type: Location and X-Request-ID,
	// for instance.
	Expose []string

	// MaxAge is how long browsers may keep the answer to a preflight
	// request, in whole seconds. Zero leaves it to them: 5 seconds, by the
	// standard. Chromium keeps it for 2 hours at most.
	MaxAge time.Duration
}

// Handler is the middleware of the policy. It panics if the policy is
// invalid: an origin that browsers don't send, as one with a path, a
// trailing slash, an upper-case letter or the default port of its scheme,
// "*" with other origins or with Credentials, an empty or invalid name in
// Expose or "*" there, or a negative MaxAge.
//
// Every response gets Vary: Origin, since whether it has the headers of
// CORS depends on the Origin of its request. A preflight request, an
// OPTIONS with the Origin and Access-Control-Request-Method headers, is
// answered here and goes no further, as the routes of a ServeMux would
// answer it with 405: from an allowed origin, with 204 and the headers of
// CORS, and from another with 403, as application/problem+json (RFC
// 9457), which fails it in the browser as a 204 without the headers would,
// and shows in the access log.
//
// Other requests go on to next, with the headers of CORS if their origin
// is allowed. A request of another origin, or of none, isn't rejected:
// pages send an Origin to their own origin too, and it's for
// [http.CrossOriginProtection] to stop the cross-site requests that change
// something.
func (c CORS) Handler(next http.Handler) http.Handler {
	p := c.policy("Handler")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPreflight(r) {
			w.Header().Add("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
			p.preflight(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && p.allows(origin) {
			p.allow(h, origin)
			if p.expose != "" {
				h.Set("Access-Control-Expose-Headers", p.expose)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// CrossOriginProtection returns a new [http.CrossOriginProtection] that
// trusts the origins of the policy. Without that, it would reject the
// requests that the policy lets their pages make, such as a POST with
// JSON: it rejects every cross-site request whose method isn't safe. Pages
// of other origins still get 403. Configure the rest of it as usual, such
// as its deny handler.
//
// It panics if the policy is invalid, as Handler does, or if its origins
// are "*": the protection can't trust every origin. A service open to any
// origin needs no protection unless it authenticates by cookies, and one
// that does decides for itself which requests bypass it.
func (c CORS) CrossOriginProtection() *http.CrossOriginProtection {
	p := c.policy("CrossOriginProtection")
	if p.any {
		panic(`middleware: CORS.CrossOriginProtection: origins "*": the protection can't trust every origin`)
	}
	protection := http.NewCrossOriginProtection()
	for _, origin := range c.Origins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			panic("middleware: CORS.CrossOriginProtection: " + err.Error()) // checkOrigin is stricter
		}
	}
	return protection
}

// policy is a CORS checked and ready for requests.
type policy struct {
	origins     map[string]bool
	any         bool // "*"
	credentials bool
	expose      string // of Access-Control-Expose-Headers; "" to leave it out
	maxAge      string // of Access-Control-Max-Age; "" to leave it out
}

// policy checks c and returns it ready for requests; method names the
// method of CORS in panics.
func (c CORS) policy(method string) *policy {
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("middleware: CORS.%s: ", method) + fmt.Sprintf(format, args...))
	}
	p := &policy{origins: make(map[string]bool), credentials: c.Credentials}
	for _, origin := range c.Origins {
		if origin == "*" {
			p.any = true
			continue
		}
		if err := checkOrigin(origin); err != nil {
			fail("origin %q: %v", origin, err)
		}
		p.origins[origin] = true
	}
	switch {
	case p.any && len(c.Origins) > 1:
		fail(`origins "*" and others: "*" allows any origin, so it goes alone`)
	case p.any && c.Credentials:
		fail(`origins "*" with Credentials: browsers send credentials only to origins that are named`)
	}
	for _, name := range c.Expose {
		if name == "*" || !isToken(name) {
			fail("Expose: %q isn't the name of a header", name)
		}
	}
	p.expose = strings.Join(c.Expose, ", ")
	if c.MaxAge < 0 {
		fail("MaxAge %v: want 0 or more", c.MaxAge)
	}
	if s := int64(c.MaxAge / time.Second); s > 0 {
		p.maxAge = strconv.FormatInt(s, 10)
	}
	return p
}

// checkOrigin reports what's wrong with origin as the value of an Origin
// header that browsers send, if anything.
func checkOrigin(origin string) error {
	for i := range len(origin) {
		if origin[i] >= 0x80 {
			return errors.New("want ASCII, as browsers send it: punycode for an internationalized host")
		}
	}
	if origin != strings.ToLower(origin) {
		return errors.New("want lower case, as browsers send it")
	}
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return err
	case u.Scheme == "" || u.Host == "" || u.Scheme+"://"+u.Host != origin || strings.HasSuffix(u.Host, ":"):
		return errors.New(`want a scheme and a host, with a port if it isn't the default one, as in "https://app.example.com"`)
	case u.Scheme == "https" && u.Port() == "443", u.Scheme == "http" && u.Port() == "80":
		return errors.New("browsers leave out the default port")
	}
	return nil
}

// preflight answers the preflight request r.
func (p *policy) preflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	origin := r.Header.Get("Origin")
	method := r.Header.Get("Access-Control-Request-Method")
	headers := r.Header.Get("Access-Control-Request-Headers")
	if !p.allows(origin) || !isToken(method) || !isTokenList(headers) {
		h.Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, forbidden)
		return
	}
	p.allow(h, origin)
	h.Set("Access-Control-Allow-Methods", method)
	if headers != "" {
		h.Set("Access-Control-Allow-Headers", headers)
	}
	if p.maxAge != "" {
		h.Set("Access-Control-Max-Age", p.maxAge)
	}
	w.WriteHeader(http.StatusNoContent)
}

// allows reports whether the pages of origin may call the service.
func (p *policy) allows(origin string) bool {
	return p.any || p.origins[origin]
}

// allow sets the headers of a response that the pages of origin may read.
func (p *policy) allow(h http.Header, origin string) {
	if p.any {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	if p.credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
}

// isPreflight reports whether r is a preflight request of CORS.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != ""
}

// isToken reports whether s is a token of RFC 9110, as the names of
// methods and headers are.
func isToken(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return s != ""
}

// isTokenList reports whether s is a list of tokens separated by commas,
// as the names of headers in Access-Control-Request-Headers are; an empty
// s is an empty list.
func isTokenList(s string) bool {
	if s == "" {
		return true
	}
	for name := range strings.SplitSeq(s, ",") {
		if !isToken(strings.Trim(name, " \t")) {
			return false
		}
	}
	return true
}
