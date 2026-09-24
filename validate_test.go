package tyr_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
)

// checked is a request whose Validate records that it ran and returns Err,
// or panics if Panic is set.
type checked struct {
	Err   error `json:"-"`
	Panic bool  `json:"-"`
	ran   *bool
}

func (r checked) Validate() error {
	if r.ran != nil {
		*r.ran = true
	}
	if r.Panic {
		panic("boom")
	}
	return r.Err
}

// checkedByPointer is a request whose Validate has a pointer receiver.
type checkedByPointer struct {
	ran *bool
}

func (r *checkedByPointer) Validate() error {
	*r.ran = true
	return nil
}

// tagged is a request with validate tags whose Validate records that it
// ran and relies on the tags: URL is set.
type tagged struct {
	URL  string `json:"url" validate:"required,url"`
	Code string `json:"code" validate:"omitempty,min=4"`
	ran  *bool
}

func (r tagged) Validate() error {
	*r.ran = true
	if strings.HasSuffix(r.URL, ".example") {
		return errors.New("example URLs aren't allowed")
	}
	return nil
}

func TestValidateTags(t *testing.T) {
	handled := false
	op := tyr.New().Handle("links.create", func(ctx context.Context, req tagged) (string, error) {
		handled = true
		return "ok", nil
	})

	t.Run("violations", func(t *testing.T) {
		ran := false
		handled = false
		_, err := op.Call(t.Context(), fill(tagged{Code: "ab", ran: &ran}))

		// In the order of the fields; neither Validate nor the handler runs.
		want := tyr.Violations{
			{Pointer: "/url", Detail: "is required"},
			{Pointer: "/code", Detail: "must be at least 4 characters"},
		}
		e, ok := err.(*tyr.Error)
		if !ok || e.Kind != tyr.KindInvalidArgument || e.Message != "validation failed" {
			t.Fatalf("Call() error = %v, want invalid_argument: validation failed", err)
		}
		if got, _ := e.Details.(tyr.Violations); !slices.Equal(got, want) {
			t.Errorf("Call() violations = %q, want %q", got, want)
		}
		if ran || handled {
			t.Errorf("Validate ran: %t, handler called: %t; want neither", ran, handled)
		}
	})
	t.Run("valid tags", func(t *testing.T) {
		ran := false
		handled = false
		_, err := op.Call(t.Context(), fill(tagged{URL: "https://links.example", ran: &ran}))
		if e, ok := err.(*tyr.Error); !ok || e.Message != "example URLs aren't allowed" || !ran || handled {
			t.Errorf("Call() error = %v, Validate ran: %t, handler called: %t; want Validate's error only", err, ran, handled)
		}
	})
}

func TestHandleBadValidateTag(t *testing.T) {
	got := panicValue(func() {
		tyr.New().Handle("links.get", func(ctx context.Context, req struct {
			Code string `json:"code" validate:"required,maxx=4"`
		}) (string, error) {
			return "", nil
		})
	})
	want := `tyr: Handle("links.get"): field Code: validate:"required,maxx=4": unknown rule "maxx"; ` +
		`check the tags with tyr.WithValidator(playground.New()) of the module validate/playground for more rules, or move the check to Validate()`
	if got != want {
		t.Errorf("Handle() panicked with %v, want %q", got, want)
	}
}

// fill returns a decode that fills in req, as a transport would.
func fill[Req any](req Req) func(dst any) error {
	return func(dst any) error {
		*dst.(*Req) = req
		return nil
	}
}

func TestValidate(t *testing.T) {
	var nilErr *tyr.Error
	plain := errors.New("end must be after start")
	expired := tyr.FailedPrecondition("link expired")
	var v tyr.Violations
	v.Add("code", "only a-z, 0-9 and '-'")
	violations := v.Err()

	tests := []struct {
		name     string
		req      checked
		wantKind tyr.Kind // of the call's error
		wantMsg  string   // of the call's error; "" if the call succeeds
		wantSame error    // an Error the call returns as is, if set
		wantIs   error    // an error the call's error wraps, if set
		wantText string   // the Error() of the call's error, if set
		wantLog  string   // the message of the one record logged, if any
	}{
		{name: "nil"},
		{
			name:     "violations",
			req:      checked{Err: violations},
			wantKind: tyr.KindInvalidArgument,
			wantMsg:  "validation failed",
			wantSame: violations,
		},
		{
			name:     "other Error",
			req:      checked{Err: expired},
			wantKind: tyr.KindFailedPrecondition,
			wantMsg:  "link expired",
			wantSame: expired,
		},
		{
			name:     "wrapped Error",
			req:      checked{Err: fmt.Errorf("check: %w", expired)},
			wantKind: tyr.KindFailedPrecondition,
			wantMsg:  "link expired",
			wantSame: expired,
		},
		{
			// Validate writes its errors for the client, so the text
			// becomes the message.
			name:     "plain error",
			req:      checked{Err: plain},
			wantKind: tyr.KindInvalidArgument,
			wantMsg:  "end must be after start",
			wantIs:   plain,
			wantText: "invalid_argument: end must be after start",
		},
		{
			name:     "nil *tyr.Error",
			req:      checked{Err: nilErr},
			wantKind: tyr.KindInternal,
			wantMsg:  "internal error",
			wantLog:  "tyr: operation failed",
		},
		{
			name:     "panic",
			req:      checked{Panic: true},
			wantKind: tyr.KindInternal,
			wantMsg:  "internal error",
			wantLog:  "tyr: panic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			api := tyr.New(tyr.WithLogger(slog.New(rec)))
			mapped := false
			api.MapError(func(error) error {
				mapped = true
				return nil
			})
			handled := false
			op := api.Handle("links.check", func(ctx context.Context, req checked) (string, error) {
				handled = true
				return "ok", nil
			})

			res, err := op.Call(t.Context(), fill(tt.req))

			if tt.wantMsg == "" {
				if res != "ok" || err != nil || !handled {
					t.Errorf("Call() = %v, %v, handler called: %t; want ok, <nil>, true", res, err, handled)
				}
			} else {
				if e, ok := err.(*tyr.Error); !ok || e.Kind != tt.wantKind || e.Message != tt.wantMsg {
					t.Errorf("Call() error = %#v, want a *tyr.Error of kind %v with message %q", err, tt.wantKind, tt.wantMsg)
				}
				if tt.wantSame != nil && err != tt.wantSame {
					t.Errorf("Call() error = %v, want the Error from Validate as is", err)
				}
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Errorf("Call() error %v doesn't wrap %v", err, tt.wantIs)
				}
				if tt.wantText != "" && err.Error() != tt.wantText {
					t.Errorf("Call() error = %q, want %q", err.Error(), tt.wantText)
				}
				if handled {
					t.Error("the handler was called")
				}
			}
			if mapped {
				t.Error("a mapper got the error of Validate")
			}
			if tt.wantLog == "" && len(rec.logs) != 0 || tt.wantLog != "" && (len(rec.logs) != 1 || rec.logs[0].msg != tt.wantLog) {
				t.Errorf("logged %+v, want %q", rec.logs, tt.wantLog)
			}
		})
	}
}

func TestValidateReceivers(t *testing.T) {
	t.Run("value", func(t *testing.T) {
		ran := false
		op := tyr.New().Handle("links.check", func(ctx context.Context, req checked) (string, error) {
			return "ok", nil
		})
		if _, err := op.Call(t.Context(), fill(checked{ran: &ran})); err != nil || !ran {
			t.Errorf("Call() error = %v, Validate ran: %t; want <nil>, true", err, ran)
		}
	})
	t.Run("pointer", func(t *testing.T) {
		ran := false
		op := tyr.New().Handle("links.check", func(ctx context.Context, req checkedByPointer) (string, error) {
			return "ok", nil
		})
		if _, err := op.Call(t.Context(), fill(checkedByPointer{ran: &ran})); err != nil || !ran {
			t.Errorf("Call() error = %v, Validate ran: %t; want <nil>, true", err, ran)
		}
	})
}

// paging, sorting and filter are embedded in requests, each with a Validate
// method of its own.
type paging struct {
	Limit int `json:"limit"`
}

func (paging) Validate() error { return errors.New("paging") }

type sorting struct {
	By string `json:"by"`
}

func (*sorting) Validate() error { return errors.New("sorting") }

type filter struct {
	Q string `json:"q"`
}

func (filter) Validate() error { return errors.New("filter") }

// page embeds two structs with Validate, so it has none.
type page struct {
	paging
	sorting
}

// pagedReq embeds one struct with Validate, which becomes its own.
type pagedReq struct{ paging }

// pagedSortedReq embeds two, so it has none.
type pagedSortedReq struct {
	paging
	sorting
}

// pagedPointerReq embeds one of the two through a pointer.
type pagedPointerReq struct {
	*paging
	sorting
}

// nestedPageReq embeds a struct that has none.
type nestedPageReq struct{ page }

// threeReq embeds three.
type threeReq struct {
	paging
	sorting
	filter
}

// ownValidateReq embeds two but has a Validate of its own.
type ownValidateReq struct {
	paging
	sorting
}

func (ownValidateReq) Validate() error { return errors.New("own") }

// boolValidate and stringValidate have Validate methods of other
// signatures, so neither is a Validator.
type (
	boolValidate struct {
		Strict bool `json:"strict"`
	}
	stringValidate struct {
		Note string `json:"note"`
	}
)

func (boolValidate) Validate() bool     { return true }
func (stringValidate) Validate() string { return "" }

// They do have Validate methods, of other signatures.
var (
	_ interface{ Validate() bool }   = boolValidate{}
	_ interface{ Validate() string } = stringValidate{}
)

// notValidatorsReq embeds both: their Validate methods conflict, but Call
// wouldn't call them anyway.
type notValidatorsReq struct {
	boolValidate
	stringValidate
}

// checkedPagingReq embeds paging and checked, a request that validates
// itself.
type checkedPagingReq struct {
	paging
	checked
}

func TestHandleConflictingValidate(t *testing.T) {
	const hint = ", whose Validate methods conflict, so none of them is called; give %[1]s a Validate method of its own that calls theirs"
	tests := []struct {
		name     string
		register func(api *tyr.API) *tyr.Operation
		want     string // what Handle panics with, or "" if it doesn't
		wantErr  string // the message of the call's error, from Validate, if Handle doesn't panic
	}{
		{name: "one", register: handleReq[pagedReq], wantErr: "paging"},
		{name: "its own", register: handleReq[ownValidateReq], wantErr: "own"},
		{name: "no Validator among them", register: handleReq[notValidatorsReq]},
		{
			name:     "two",
			register: handleReq[pagedSortedReq],
			want:     fmt.Sprintf("request type %[1]s embeds paging and sorting"+hint, "tyr_test.pagedSortedReq"),
		},
		{
			name:     "through a pointer",
			register: handleReq[pagedPointerReq],
			want:     fmt.Sprintf("request type %[1]s embeds paging and sorting"+hint, "tyr_test.pagedPointerReq"),
		},
		{
			name:     "in an embedded struct",
			register: handleReq[nestedPageReq],
			want:     fmt.Sprintf("request type %[1]s embeds page, which embeds paging and sorting"+hint, "tyr_test.nestedPageReq"),
		},
		{
			name:     "three",
			register: handleReq[threeReq],
			want:     fmt.Sprintf("request type %[1]s embeds paging, sorting and filter"+hint, "tyr_test.threeReq"),
		},
		{
			name:     "with a request that validates itself",
			register: handleReq[checkedPagingReq],
			want:     fmt.Sprintf("request type %[1]s embeds paging and checked"+hint, "tyr_test.checkedPagingReq"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var op *tyr.Operation
			got := panicValue(func() { op = tt.register(tyr.New()) })
			if tt.want != "" {
				if want := `tyr: Handle("links.list"): ` + tt.want; got != want {
					t.Errorf("Handle() panicked with %v, want %q", got, want)
				}
				return
			}
			if got != nil {
				t.Fatalf("Handle() panicked with %v", got)
			}
			_, err := op.Call(t.Context(), nil)
			var msg string // of the error of Validate
			if e, ok := errors.AsType[*tyr.Error](err); ok {
				msg = e.Message
			}
			if msg != tt.wantErr {
				t.Errorf("Call() error = %v, want the error %q of Validate", err, tt.wantErr)
			}
		})
	}
}

// handleReq registers an operation with the request type Req.
func handleReq[Req any](api *tyr.API) *tyr.Operation {
	return api.Handle("links.list", func(ctx context.Context, req Req) (string, error) {
		return "ok", nil
	})
}

func TestValidateAfterInterceptors(t *testing.T) {
	handler := func(ctx context.Context, req checked) (string, error) { return "ok", nil }

	t.Run("denied", func(t *testing.T) {
		api := tyr.New()
		api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
			return nil, tyr.Unauthenticated("log in first")
		})
		op := api.Handle("links.check", handler)

		// A client that isn't logged in learns that, not what's wrong with
		// its request.
		ran := false
		_, err := op.Call(t.Context(), fill(checked{Err: errors.New("bad code"), ran: &ran}))
		if e, ok := err.(*tyr.Error); !ok || e.Kind != tyr.KindUnauthenticated || ran {
			t.Errorf("Call() error = %v, Validate ran: %t; want unauthenticated, false", err, ran)
		}
	})
	t.Run("changed request", func(t *testing.T) {
		api := tyr.New()
		api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
			req.(*checked).Err = nil // fixes up the request before it's validated
			return next(ctx, req)
		})
		op := api.Handle("links.check", handler)

		if _, err := op.Call(t.Context(), fill(checked{Err: errors.New("bad code")})); err != nil {
			t.Errorf("Call() error = %v, want <nil>: Validate sees the changed request", err)
		}
	})
}
