package tyr_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestKindString(t *testing.T) {
	tests := []struct {
		kind tyr.Kind
		want string
	}{
		{tyr.KindInternal, "internal"},
		{tyr.KindInvalidArgument, "invalid_argument"},
		{tyr.KindUnauthenticated, "unauthenticated"},
		{tyr.KindPermissionDenied, "permission_denied"},
		{tyr.KindNotFound, "not_found"},
		{tyr.KindAlreadyExists, "already_exists"},
		{tyr.KindFailedPrecondition, "failed_precondition"},
		{tyr.KindResourceExhausted, "resource_exhausted"},
		{tyr.KindDeadlineExceeded, "deadline_exceeded"},
		{tyr.KindUnavailable, "unavailable"},
		{tyr.KindCanceled, "canceled"},
		{tyr.Kind(42), "Kind(42)"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestKindMarshalText(t *testing.T) {
	for _, k := range []tyr.Kind{tyr.KindNotFound, tyr.Kind(42)} {
		got, err := k.MarshalText()
		if string(got) != k.String() || err != nil {
			t.Errorf("Kind(%d).MarshalText() = %q, %v; want %q, <nil>", k, got, err, k.String())
		}
	}

	// JSON encoders pick the method up.
	got, err := json.Marshal(tyr.KindNotFound)
	if want := `"not_found"`; string(got) != want || err != nil {
		t.Errorf("json.Marshal(KindNotFound) = %s, %v; want %s, <nil>", got, err, want)
	}
}

func TestKindUnmarshalText(t *testing.T) {
	// Every kind reads back what MarshalText writes, unknown ones too.
	for n := range 256 {
		want := tyr.Kind(n)
		text, _ := want.MarshalText()
		var got tyr.Kind
		if err := got.UnmarshalText(text); got != want || err != nil {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v, <nil>", text, got, err, want)
		}
	}

	for _, text := range []string{
		"", "NOT_FOUND", "not-found", " not_found", "not_found ",
		"Kind(4)",   // a known kind goes by its name
		"Kind(042)", // not as String writes it
		"Kind(+42)", "Kind(-1)", "Kind(256)", "Kind(42", "Kind()", "kind(42)",
	} {
		got := tyr.KindUnavailable
		err := got.UnmarshalText([]byte(text))
		if want := fmt.Sprintf("tyr: unknown kind %q", text); err == nil || err.Error() != want || got != tyr.KindUnavailable {
			t.Errorf("UnmarshalText(%q) = %v, %v; want the kind unchanged and %q", text, got, err, want)
		}
	}

	// JSON decoders pick the method up.
	var got tyr.Kind
	if err := json.Unmarshal([]byte(`"not_found"`), &got); got != tyr.KindNotFound || err != nil {
		t.Errorf(`json.Unmarshal("not_found") = %v, %v; want not_found, <nil>`, got, err)
	}
}

func TestConstructors(t *testing.T) {
	tests := []struct {
		name   string
		newErr func(format string, args ...any) *tyr.Error
		kind   tyr.Kind
	}{
		{"Internal", tyr.Internal, tyr.KindInternal},
		{"InvalidArgument", tyr.InvalidArgument, tyr.KindInvalidArgument},
		{"Unauthenticated", tyr.Unauthenticated, tyr.KindUnauthenticated},
		{"PermissionDenied", tyr.PermissionDenied, tyr.KindPermissionDenied},
		{"NotFound", tyr.NotFound, tyr.KindNotFound},
		{"AlreadyExists", tyr.AlreadyExists, tyr.KindAlreadyExists},
		{"FailedPrecondition", tyr.FailedPrecondition, tyr.KindFailedPrecondition},
		{"ResourceExhausted", tyr.ResourceExhausted, tyr.KindResourceExhausted},
		{"DeadlineExceeded", tyr.DeadlineExceeded, tyr.KindDeadlineExceeded},
		{"Unavailable", tyr.Unavailable, tyr.KindUnavailable},
		{"Canceled", tyr.Canceled, tyr.KindCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.newErr("link %q not found", "abc")
			if e.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", e.Kind, tt.kind)
			}
			if want := `link "abc" not found`; e.Message != want {
				t.Errorf("Message = %q, want %q", e.Message, want)
			}
			if e.Details != nil || e.Unwrap() != nil {
				t.Errorf("Details, Unwrap() = %v, %v; want <nil>, <nil>", e.Details, e.Unwrap())
			}
		})
	}
}

func TestErrorError(t *testing.T) {
	cause := errors.New("store: not found")

	tests := []struct {
		name string
		err  *tyr.Error
		want string
	}{
		{"zero value", &tyr.Error{}, "internal"},
		{"kind", &tyr.Error{Kind: tyr.KindNotFound}, "not_found"},
		{"message", tyr.NotFound("link not found"), "not_found: link not found"},
		{"cause", tyr.NotFound("").WithCause(cause), "not_found: store: not found"},
		{"message and cause", tyr.NotFound("link not found").WithCause(cause), "not_found: link not found: store: not found"},
		{"cause repeating the message", tyr.NotFound("store: not found").WithCause(cause), "not_found: store: not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("store: not found")

	if got := tyr.NotFound("link not found").Unwrap(); got != nil {
		t.Errorf("Unwrap() without a cause = %v, want <nil>", got)
	}
	err := tyr.NotFound("link not found").WithCause(cause)
	if got := err.Unwrap(); got != cause {
		t.Errorf("Unwrap() = %v, want %v", got, cause)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false, want true")
	}

	// A cause that Error leaves out for repeating the message is still there.
	err = tyr.NotFound("store: not found").WithCause(cause)
	if got := err.Unwrap(); got != cause || !errors.Is(err, cause) {
		t.Errorf("Unwrap() = %v, want %v, also for errors.Is", got, cause)
	}
}

func TestErrorWithCause(t *testing.T) {
	cause := errors.New("store: not found")
	orig := tyr.NotFound("link not found").WithDetails("details")
	before := *orig

	got := orig.WithCause(cause)
	if got == orig {
		t.Fatal("WithCause() returned the receiver, want a copy")
	}
	if *orig != before {
		t.Errorf("WithCause() changed the receiver to %v", orig)
	}
	if got.Kind != orig.Kind || got.Message != orig.Message || got.Details != orig.Details {
		t.Errorf("WithCause() = %v with details %v, want %v with details %v", got, got.Details, orig, orig.Details)
	}
	if got.Unwrap() != cause {
		t.Errorf("WithCause().Unwrap() = %v, want %v", got.Unwrap(), cause)
	}
}

func TestErrorWithDetails(t *testing.T) {
	cause := errors.New("store: not found")
	orig := tyr.NotFound("link not found").WithCause(cause)
	before := *orig

	got := orig.WithDetails("details")
	if got == orig {
		t.Fatal("WithDetails() returned the receiver, want a copy")
	}
	if *orig != before {
		t.Errorf("WithDetails() changed the receiver to %v with details %v", orig, orig.Details)
	}
	if got.Kind != orig.Kind || got.Message != orig.Message || got.Unwrap() != cause {
		t.Errorf("WithDetails() = %v, want %v", got, orig)
	}
	if got.Details != "details" {
		t.Errorf("WithDetails().Details = %v, want %q", got.Details, "details")
	}
}

// Errors an application shares, e.g. as package-level variables.
var (
	errLinkExpired = tyr.FailedPrecondition("link expired")
	errQuota       = tyr.ResourceExhausted("quota exceeded").WithDetails("daily") // itself built with With*
)

func TestErrorIs(t *testing.T) {
	cause := errors.New("store: expired")
	same := tyr.NotFound("same")

	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"copy", errLinkExpired.WithCause(cause), errLinkExpired, true},
		{"copy of a copy", errLinkExpired.WithCause(cause).WithDetails("details"), errLinkExpired, true},
		{"copy of an error built with With*", errQuota.WithCause(cause), errQuota, true},
		{"wrapped copy", fmt.Errorf("links.get: %w", errLinkExpired.WithCause(cause)), errLinkExpired, true},
		{"cause of a copy", errLinkExpired.WithCause(cause), cause, true},
		{"distinct errors with the same message", tyr.NotFound("same"), same, false},
		{"copy of a distinct error", tyr.NotFound("same").WithCause(cause), same, false},
		{"original and its copy", errLinkExpired, errLinkExpired.WithCause(cause), false},
		{"typed nil", tyr.NotFound("same"), (*tyr.Error)(nil), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Errorf("errors.Is() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNilError(t *testing.T) {
	// A nil *Error that a function returned as an error by mistake: errors.Is
	// calls its Is and then its Unwrap, and logs call its Error.
	var e *tyr.Error
	var err error = e
	for _, target := range []error{errLinkExpired, context.DeadlineExceeded, io.EOF} {
		if errors.Is(err, target) || errors.Is(fmt.Errorf("wrapped: %w", err), target) {
			t.Errorf("errors.Is(nil *Error, %v) = true, want false", target)
		}
	}
	if got := errors.Unwrap(err); got != nil {
		t.Errorf("errors.Unwrap(nil *Error) = %v, want <nil>", got)
	}
	if got := err.Error(); got != "<nil>" {
		t.Errorf("nil *Error: Error() = %q, want <nil>", got)
	}
}

func TestViolationsAdd(t *testing.T) {
	tests := []struct {
		field string
		want  string
	}{
		{"code", "/code"},
		{"a/b", "/a~1b"},
		{"a~b", "/a~0b"},
		{"~1", "/~01"}, // a literal "~1" stays distinct from an escaped "/"
		{"", "/"},      // the member with the empty name; "" is the whole document
	}
	for _, tt := range tests {
		var v tyr.Violations
		v.Add(tt.field, "invalid")
		if want := (tyr.Violations{{Pointer: tt.want, Detail: "invalid"}}); !slices.Equal(v, want) {
			t.Errorf("Add(%q) made %v, want %v", tt.field, v, want)
		}
	}
}

func TestViolationsErr(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		for _, v := range []tyr.Violations{nil, {}} {
			if err := v.Err(); err != nil {
				t.Errorf("%#v.Err() = %#v, want <nil>", v, err)
			}
		}
	})
	t.Run("not empty", func(t *testing.T) {
		var v tyr.Violations
		v.Add("url", "invalid URL")
		v.Add("code", "too short")

		e, ok := errors.AsType[*tyr.Error](v.Err())
		if !ok {
			t.Fatalf("Err() = %#v, want a *tyr.Error", v.Err())
		}
		if e.Kind != tyr.KindInvalidArgument || e.Message != "validation failed" {
			t.Errorf("Err() = %v, want invalid_argument: validation failed", e)
		}
		if got, _ := e.Details.(tyr.Violations); !slices.Equal(got, v) {
			t.Errorf("Err().Details = %v, want %v", e.Details, v)
		}
	})
}

func TestViolationsJSON(t *testing.T) {
	var v tyr.Violations
	v.Add("code", "only a-z, 0-9 and '-'")

	got, err := json.Marshal(v)
	if want := `[{"pointer":"/code","detail":"only a-z, 0-9 and '-'"}]`; string(got) != want || err != nil {
		t.Errorf("json.Marshal() = %s, %v; want %s, <nil>", got, err, want)
	}
}
