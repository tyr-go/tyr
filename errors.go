package tyr

import (
	"encoding/json/jsontext"
	"fmt"
	"strconv"
	"strings"
)

// Kind classifies an [Error] independently of any transport. The kinds
// follow the meaning of the gRPC status codes of the same names, and
// transports map them to codes of their own, such as HTTP statuses or
// JSON-RPC error codes. The HTTP statuses of [github.com/tyr-go/tyr/rest]
// are its own choice rather than the HTTP mapping of gRPC: there,
// [KindFailedPrecondition] is 409 Conflict, not 400, on purpose, as RFC 9110
// has 409 for a request that conflicts with the state of its resource.
type Kind uint8

const (
	// KindInternal is an unexpected failure on the server. It is the zero
	// Kind: errors that tyr can't classify are reported with it.
	KindInternal Kind = iota
	// KindInvalidArgument means the request is malformed or fails
	// validation, whatever the state of the system.
	KindInvalidArgument
	// KindUnauthenticated means the request lacks valid credentials.
	KindUnauthenticated
	// KindPermissionDenied means the caller is known but isn't allowed to
	// perform the operation.
	KindPermissionDenied
	// KindNotFound means a requested entity doesn't exist.
	KindNotFound
	// KindAlreadyExists means an entity the caller tried to create already
	// exists.
	KindAlreadyExists
	// KindFailedPrecondition means the system isn't in the state the
	// operation requires, e.g. a shipped order can't be cancelled.
	KindFailedPrecondition
	// KindResourceExhausted means a quota or a rate limit is exhausted.
	KindResourceExhausted
	// KindDeadlineExceeded means the operation didn't finish before its
	// deadline.
	KindDeadlineExceeded
	// KindUnavailable means the service can't handle the request right now;
	// the caller may retry.
	KindUnavailable
	// KindCanceled means the caller canceled the call, e.g. the client went
	// away before the response. It is the CANCELLED code of gRPC, spelled
	// as in [context.Canceled].
	KindCanceled

	numKinds // the number of the kinds above; keep it last
)

// String returns the name transports send to clients, such as "not_found".
// Unknown kinds are formatted as "Kind(42)".
func (k Kind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindInvalidArgument:
		return "invalid_argument"
	case KindUnauthenticated:
		return "unauthenticated"
	case KindPermissionDenied:
		return "permission_denied"
	case KindNotFound:
		return "not_found"
	case KindAlreadyExists:
		return "already_exists"
	case KindFailedPrecondition:
		return "failed_precondition"
	case KindResourceExhausted:
		return "resource_exhausted"
	case KindDeadlineExceeded:
		return "deadline_exceeded"
	case KindUnavailable:
		return "unavailable"
	case KindCanceled:
		return "canceled"
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// MarshalText implements [encoding.TextMarshaler] by returning
// [Kind.String], so that JSON and log output show a kind by its name.
func (k Kind) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

// UnmarshalText implements [encoding.TextUnmarshaler]: it sets k to the
// kind that text names, as [Kind.String] names it, so that a client can
// tell the kind of an error it got. Like MarshalText, it takes an unknown
// kind as "Kind(42)". Other text, such as "NOT_FOUND" or "Kind(4)", is an
// error.
func (k *Kind) UnmarshalText(text []byte) error {
	s := string(text)
	for c := range numKinds {
		if c.String() == s {
			*k = c
			return nil
		}
	}
	if inner, ok := strings.CutPrefix(s, "Kind("); ok {
		if digits, ok := strings.CutSuffix(inner, ")"); ok {
			// Only as String writes it: no known kind, no leading zeros.
			if n, err := strconv.ParseUint(digits, 10, 8); err == nil && Kind(n).String() == s {
				*k = Kind(n)
				return nil
			}
		}
	}
	return fmt.Errorf("tyr: unknown kind %q", s)
}

// known reports whether k is one of the kinds this package defines.
func (k Kind) known() bool {
	return k < numKinds
}

// Error is an error meant for the client: a [Kind], a message and optional
// details. A handler may return other errors too; [Operation.Call] turns
// them into an Error, by default of [KindInternal] with a generic message.
//
// Transports send clients the kind, the message and the details of an
// Error, except for an error of KindInternal or of a kind this package
// doesn't define: clients get only "internal error" for it, and the API logs
// its message and details, see [WithLogger].
//
// [Error.WithCause] and [Error.WithDetails] return copies, so an *Error can
// be shared, e.g. as a package-level variable, and [errors.Is] still matches
// the copies with it.
type Error struct {
	Kind Kind
	// Message is sent to clients as is, so it must not leak internals;
	// that of an internal error only goes to the logs.
	Message string
	// Details is sent to clients along with Message, e.g. [Violations];
	// those of an internal error only go to the logs.
	Details any

	cause  error  // logged, never sent
	origin *Error // the error With* was called on to make this copy; see Is
}

// Error formats e for logs as "kind: message: cause", omitting empty parts
// and a cause whose text repeats the message; the cause is still there for
// [errors.Is] and [errors.As]. Clients never see it: transports send only
// Kind, Message and Details.
//
// Error, Unwrap and Is take a nil *Error too, which a function may return
// as an error by mistake: Error returns "<nil>", as fmt prints a nil
// pointer, Unwrap nil, and Is false, so that errors.Is doesn't panic.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := e.Kind.String()
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.cause != nil {
		if c := e.cause.Error(); c != e.Message {
			s += ": " + c
		}
	}
	return s
}

// Unwrap returns the cause set by [Error.WithCause], or nil.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is reports whether e was made from target by [Error.WithCause] or
// [Error.WithDetails], directly or through other copies, so that
// [errors.Is] matches an extended error with every error it came from:
//
//	var ErrExpired = tyr.FailedPrecondition("link expired")
//
//	err := ErrExpired.WithCause(cause).WithDetails(d)
//	errors.Is(err, ErrExpired) // true
//
// Distinct errors never match, even with the same kind and message.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	for o := e.origin; o != nil; o = o.origin {
		if target == o {
			return true
		}
	}
	return false
}

// WithCause returns a copy of e with err as its cause. The cause shows up in
// logs and in [errors.Is] and [errors.As], but it's never sent to clients.
func (e *Error) WithCause(err error) *Error {
	c := e.clone()
	c.cause = err
	return c
}

// WithDetails returns a copy of e with d as its details. Transports encode
// details as JSON and send them to clients.
func (e *Error) WithDetails(d any) *Error {
	c := e.clone()
	c.Details = d
	return c
}

// clone returns a copy of e that remembers e as its origin.
func (e *Error) clone() *Error {
	c := *e
	c.origin = e
	return &c
}

// Internal returns an [Error] of kind [KindInternal] with a message
// formatted as with [fmt.Sprintf]. Clients get only "internal error" for it,
// and its message goes to the logs.
func Internal(format string, args ...any) *Error {
	return &Error{Kind: KindInternal, Message: fmt.Sprintf(format, args...)}
}

// InvalidArgument returns an [Error] of kind [KindInvalidArgument] with a
// message formatted as with [fmt.Sprintf].
func InvalidArgument(format string, args ...any) *Error {
	return &Error{Kind: KindInvalidArgument, Message: fmt.Sprintf(format, args...)}
}

// Unauthenticated returns an [Error] of kind [KindUnauthenticated] with a
// message formatted as with [fmt.Sprintf].
func Unauthenticated(format string, args ...any) *Error {
	return &Error{Kind: KindUnauthenticated, Message: fmt.Sprintf(format, args...)}
}

// PermissionDenied returns an [Error] of kind [KindPermissionDenied] with a
// message formatted as with [fmt.Sprintf].
func PermissionDenied(format string, args ...any) *Error {
	return &Error{Kind: KindPermissionDenied, Message: fmt.Sprintf(format, args...)}
}

// NotFound returns an [Error] of kind [KindNotFound] with a message
// formatted as with [fmt.Sprintf].
func NotFound(format string, args ...any) *Error {
	return &Error{Kind: KindNotFound, Message: fmt.Sprintf(format, args...)}
}

// AlreadyExists returns an [Error] of kind [KindAlreadyExists] with a
// message formatted as with [fmt.Sprintf].
func AlreadyExists(format string, args ...any) *Error {
	return &Error{Kind: KindAlreadyExists, Message: fmt.Sprintf(format, args...)}
}

// FailedPrecondition returns an [Error] of kind [KindFailedPrecondition]
// with a message formatted as with [fmt.Sprintf].
func FailedPrecondition(format string, args ...any) *Error {
	return &Error{Kind: KindFailedPrecondition, Message: fmt.Sprintf(format, args...)}
}

// ResourceExhausted returns an [Error] of kind [KindResourceExhausted] with
// a message formatted as with [fmt.Sprintf].
func ResourceExhausted(format string, args ...any) *Error {
	return &Error{Kind: KindResourceExhausted, Message: fmt.Sprintf(format, args...)}
}

// DeadlineExceeded returns an [Error] of kind [KindDeadlineExceeded] with a
// message formatted as with [fmt.Sprintf].
func DeadlineExceeded(format string, args ...any) *Error {
	return &Error{Kind: KindDeadlineExceeded, Message: fmt.Sprintf(format, args...)}
}

// Unavailable returns an [Error] of kind [KindUnavailable] with a message
// formatted as with [fmt.Sprintf].
func Unavailable(format string, args ...any) *Error {
	return &Error{Kind: KindUnavailable, Message: fmt.Sprintf(format, args...)}
}

// Canceled returns an [Error] of kind [KindCanceled] with a message
// formatted as with [fmt.Sprintf].
func Canceled(format string, args ...any) *Error {
	return &Error{Kind: KindCanceled, Message: fmt.Sprintf(format, args...)}
}

// Violation is a field that failed validation. Its shape follows the
// "errors" member from the validation example in RFC 9457.
type Violation struct {
	// Pointer is a JSON Pointer (RFC 6901) to the field in the JSON form of
	// the request, such as "/code". It uses the plain string representation,
	// not the "#/code" URI fragment form.
	Pointer string `json:"pointer" doc:"A JSON Pointer to the field in the JSON form of the request, such as /code."`
	// Detail says what's wrong with the field; it's sent to clients.
	Detail string `json:"detail" doc:"What's wrong with the field."`
}

// Violations collects fields that failed validation. The zero value is
// ready to use.
type Violations []Violation

// Add records that the field with the given JSON name, such as "code", is
// invalid. It builds [Violation.Pointer] itself, escaping "~" and "/" in the
// name as RFC 6901 requires.
func (v *Violations) Add(field, detail string) {
	p := jsontext.Pointer("").AppendToken(field)
	*v = append(*v, Violation{Pointer: string(p), Detail: detail})
}

// Err returns nil if v is empty, or else an [Error] of kind
// [KindInvalidArgument] with v as its details. The nil is an untyped nil
// interface, so a Validate method can end with return v.Err().
func (v Violations) Err() error {
	if len(v) == 0 {
		return nil
	}
	return &Error{Kind: KindInvalidArgument, Message: "validation failed", Details: v}
}
