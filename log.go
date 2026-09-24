package tyr

import (
	"context"
	"log/slog"
	"slices"
)

// NewLogHandler returns a handler that adds the request ID and the name of
// the operation from a record's context to the record, as "request_id" and
// "operation", and passes the record to next; see [RequestIDFrom] and
// [OperationFrom]. Values missing from the context are left out. Code logs
// as usual, with the context:
//
//	slog.InfoContext(ctx, "link created", "code", code)
//
// request_id and operation are always at the top level of a record, even
// when the logger has groups, so that logs can be searched by them. For
// that, the handler doesn't pass WithGroup on to next: next gets the groups
// as nested attributes instead. The output of [slog.JSONHandler] and
// [slog.TextHandler] stays the same, but a handler of your own that relies
// on WithGroup behaves differently. [LogAttrs] adds more of the context
// there, such as the IDs of a trace.
//
// NewLogHandler panics if next or an option is nil.
func NewLogHandler(next slog.Handler, opts ...LogOption) slog.Handler {
	if next == nil {
		panic("tyr: NewLogHandler: nil handler")
	}
	h := &logHandler{next: next}
	for _, opt := range opts {
		if opt == nil {
			panic("tyr: NewLogHandler: nil option")
		}
		opt(h)
	}
	return h
}

// LogOption configures a handler that [NewLogHandler] makes.
type LogOption func(*logHandler)

// LogAttrs makes the handler add the attributes that f appends to attrs
// from the context of a record, at the top level of the record, as
// request_id and operation are, and after them, such as the trace_id and
// span_id of github.com/tyr-go/tyr/oteltyr.TraceIDs:
//
//	logger := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(os.Stderr, nil), tyr.LogAttrs(oteltyr.TraceIDs)))
//
// f gets the attributes so far and returns them with its own, if any,
// appended, so that it allocates nothing it doesn't need. LogAttrs panics
// if f is nil.
func LogAttrs(f func(ctx context.Context, attrs []slog.Attr) []slog.Attr) LogOption {
	if f == nil {
		panic("tyr: LogAttrs: nil func")
	}
	return func(h *logHandler) { h.attrs = append(h.attrs, f) }
}

// logHandler is the handler NewLogHandler returns.
type logHandler struct {
	next   slog.Handler                                               // with the attributes added before the first group
	groups []logGroup                                                 // opened by WithGroup, with the attributes added in them
	attrs  []func(ctx context.Context, attrs []slog.Attr) []slog.Attr // of LogAttrs
}

// logGroup is a group opened by WithGroup and the attributes added in it.
type logGroup struct {
	name  string
	attrs []slog.Attr
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	if len(h.groups) == 0 {
		return &logHandler{next: h.next.WithAttrs(attrs), attrs: h.attrs}
	}
	groups := slices.Clone(h.groups)
	last := &groups[len(groups)-1]
	last.attrs = slices.Concat(last.attrs, attrs)
	return &logHandler{next: h.next, groups: groups, attrs: h.attrs}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &logHandler{next: h.next, groups: append(slices.Clone(h.groups), logGroup{name: name}), attrs: h.attrs}
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	var top []slog.Attr
	if id, ok := RequestIDFrom(ctx); ok {
		top = append(top, slog.String("request_id", id))
	}
	if op, ok := OperationFrom(ctx); ok {
		top = append(top, slog.String("operation", op.Name()))
	}
	for _, f := range h.attrs {
		top = f(ctx, top)
	}
	if len(top) == 0 && len(h.groups) == 0 {
		return h.next.Handle(ctx, r)
	}

	// A record shares state with its copies, so build a new one.
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(top...)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	for _, g := range slices.Backward(h.groups) {
		attrs = []slog.Attr{slog.GroupAttrs(g.name, slices.Concat(g.attrs, attrs)...)}
	}
	out.AddAttrs(attrs...)
	return h.next.Handle(ctx, out)
}
