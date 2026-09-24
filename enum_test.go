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

// state is an enum of the tests.
type state string

const (
	stateTodo  state = "todo"
	stateDoing state = "doing"
	stateDone  state = "done"
)

func (state) EnumValues() []state { return []state{stateTodo, stateDoing, stateDone} }

var _ tyr.Enum[state] = stateTodo

// task is a result with an enum.
type task struct {
	Title  string `json:"title"`
	Status state  `json:"status"`
}

// statusReq is a request with enums, and counts the calls of its Validate.
type statusReq struct {
	Status state   `json:"status" validate:"required"`
	Filter state   `json:"filter"`
	Tags   []state `json:"tags"`
	Short  state   `json:"short" validate:"max=4"`

	validated *int
}

func (r statusReq) Validate() error {
	if r.validated != nil {
		*r.validated++
	}
	return nil
}

func TestEnumRequests(t *testing.T) {
	tests := []struct {
		name string
		req  statusReq
		want tyr.Violations
	}{
		{"valid", statusReq{Status: stateTodo, Tags: []state{stateDoing}}, nil},
		{
			// The tags first: /short fails max=4 and adds no second
			// violation; zero fields aren't set.
			"invalid",
			statusReq{Status: "later", Filter: "x", Tags: []state{stateTodo, "y"}, Short: "toolong"},
			tyr.Violations{
				{Pointer: "/short", Detail: "must be at most 4 characters"},
				{Pointer: "/status", Detail: "must be one of: todo, doing, done"},
				{Pointer: "/filter", Detail: "must be one of: todo, doing, done"},
				{Pointer: "/tags/1", Detail: "must be one of: todo, doing, done"},
			},
		},
		{"not set", statusReq{}, tyr.Violations{{Pointer: "/status", Detail: "is required"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := tyr.New(tyr.WithLogger(slog.New(slog.DiscardHandler)))
			op := api.Handle("tasks.find", func(ctx context.Context, req statusReq) (string, error) { return "found", nil })
			validated := 0
			res, err := op.Call(t.Context(), func(dst any) error {
				*dst.(*statusReq) = tt.req
				dst.(*statusReq).validated = &validated
				return nil
			})
			if tt.want == nil {
				if res != "found" || err != nil || validated != 1 {
					t.Errorf("Call() = %v, %v, validated %d times; want found, <nil>, once", res, err, validated)
				}
				return
			}
			e, ok := errors.AsType[*tyr.Error](err)
			if !ok || e.Kind != tyr.KindInvalidArgument || !slices.Equal(e.Details.(tyr.Violations), tt.want) {
				t.Errorf("Call() error = %v %v, want invalid_argument %v", err, e.Details, tt.want)
			}
			if validated != 0 {
				t.Errorf("Validate was called %d times, want none: it may rely on the enums", validated)
			}
		})
	}
}

func TestEnumResults(t *testing.T) {
	const hint = "; if the field is optional, add omitzero or use a pointer"
	tests := []struct {
		name string
		call func(api *tyr.API) *tyr.Operation
		log  string // of the cause; "" for a result that passes
	}{
		{"valid", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.get", func(ctx context.Context, _ struct{}) (task, error) { return task{Status: stateDone}, nil })
		}, ""},
		{"a nil result", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.get", func(ctx context.Context, _ struct{}) (*task, error) { return nil, nil })
		}, ""},
		{"a value of no status", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.get", func(ctx context.Context, _ struct{}) (task, error) { return task{Status: "archived"}, nil })
		}, `the result of tasks.get has "archived" at /status, which isn't a value of tyr_test.state`},
		{"the zero value of a field", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.get", func(ctx context.Context, _ struct{}) (task, error) { return task{Title: "Draw a logo"}, nil })
		}, `the result of tasks.get has "" at /status, which isn't a value of tyr_test.state` + hint},
		{"an element", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.statuses", func(ctx context.Context, _ struct{}) ([]state, error) { return []state{stateTodo, ""}, nil })
		}, `the result of tasks.statuses has "" at /1, which isn't a value of tyr_test.state`},
		{"the whole result", func(api *tyr.API) *tyr.Operation {
			return api.Handle("tasks.status", func(ctx context.Context, _ struct{}) (state, error) { return "odd", nil })
		}, `the result of tasks.status is "odd", which isn't a value of tyr_test.state`},
		{"a result of an interceptor", func(api *tyr.API) *tyr.Operation {
			api.Use(func(ctx context.Context, op *tyr.Operation, req any, next tyr.Invoker) (any, error) {
				return task{Status: "cached"}, nil
			})
			return api.Handle("tasks.get", func(ctx context.Context, _ struct{}) (task, error) { return task{Status: stateDone}, nil })
		}, `the result of tasks.get has "cached" at /status, which isn't a value of tyr_test.state`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			op := tt.call(tyr.New(tyr.WithLogger(slog.New(rec))))
			_, err := op.Call(t.Context(), nil)
			if tt.log == "" {
				if err != nil {
					t.Errorf("Call() error = %v, want none", err)
				}
				return
			}
			if e, ok := errors.AsType[*tyr.Error](err); !ok || e.Kind != tyr.KindInternal || e.Message != "internal error" {
				t.Errorf("Call() error = %v, want an internal error", err)
			}
			if len(rec.logs) != 1 || !strings.HasSuffix(rec.logs[0].attrs["err"], "tyr: "+tt.log) {
				t.Errorf("logged %+v, want the cause %q", rec.logs, tt.log)
			}
		})
	}
}

// Enums that make no enum, for TestEnumPanics.
type (
	twiceStatus string
	priority    int
)

func (twiceStatus) EnumValues() []twiceStatus { return []twiceStatus{"a", "a"} }
func (priority) EnumValues() []priority       { return []priority{1, 2} }

func TestEnumPanics(t *testing.T) {
	type twiceReq struct {
		T twiceStatus `json:"t"`
	}
	noop := func(ctx context.Context, req statusReq) (task, error) { return task{Status: stateTodo}, nil }
	tests := []struct {
		name string
		f    func(api *tyr.API)
		want string
	}{
		{"a request of a bad enum", func(api *tyr.API) {
			api.Handle("x", func(ctx context.Context, req twiceReq) (string, error) { return "", nil })
		}, `tyr: Handle("x"): tyr_test.twiceReq: tyr_test.twiceStatus.EnumValues has "a" twice`},
		{"a result with numbers as keys", func(api *tyr.API) {
			api.Handle("x", func(ctx context.Context, req struct{}) (map[priority]int, error) { return nil, nil })
		}, `tyr: Handle("x"): map[tyr_test.priority]int: the keys of map[tyr_test.priority]int are of tyr_test.priority, ` +
			"an enum of numbers, which can't name the members of a JSON object"},
		{"an example of a bad request", func(api *tyr.API) {
			api.Implement(tyr.Define[statusReq, task]("x").Example("bad", statusReq{Status: "nope"}, task{Status: stateTodo}), noop)
		}, `tyr: Implement("x"): example "bad": the request fails validation: /status: must be one of: todo, doing, done`},
		{"an example of a bad result", func(api *tyr.API) {
			api.Implement(tyr.Define[statusReq, task]("x").Example("bad", statusReq{Status: stateTodo}, task{Status: "nope"}), noop)
		}, `tyr: Implement("x"): example "bad": the result of x has "nope" at /status, which isn't a value of tyr_test.state`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if got := fmt.Sprint(recover()); got != tt.want {
					t.Errorf("panicked with\n%s\nwant\n%s", got, tt.want)
				}
			}()
			tt.f(tyr.New())
		})
	}
}
