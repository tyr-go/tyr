package tyr_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"testing"
	"testing/slogtest"

	"github.com/tyr-go/tyr"
)

// noTime drops the time from records, so that the output is stable.
var noTime = &slog.HandlerOptions{
	ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	},
}

func TestLogHandlerSlogtest(t *testing.T) {
	var buf bytes.Buffer
	slogtest.Run(t, func(*testing.T) slog.Handler {
		buf.Reset()
		return tyr.NewLogHandler(slog.NewJSONHandler(&buf, nil))
	}, func(t *testing.T) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", buf.Bytes(), err)
		}
		return m
	})
}

// logInCall calls log in a call of the operation links.get with the request
// ID req-1 in its context, and returns what log wrote to l, a JSON logger
// wrapped by NewLogHandler.
func logInCall(t *testing.T, log func(ctx context.Context, l *slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, noTime)))
	op := tyr.New().Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		log(ctx, l)
		return nil, nil
	})
	if _, err := op.Call(tyr.WithRequestID(t.Context(), "req-1"), nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	return buf.String()
}

func TestLogHandlerTopLevel(t *testing.T) {
	const ids = `"request_id":"req-1","operation":"links.get"`
	tests := []struct {
		name string
		log  func(ctx context.Context, l *slog.Logger)
		want string
	}{
		{
			name: "no groups",
			log:  func(ctx context.Context, l *slog.Logger) { l.InfoContext(ctx, "m", "code", "go") },
			want: `{"level":"INFO","msg":"m",` + ids + `,"code":"go"}`,
		},
		{
			name: "attributes before a group",
			log: func(ctx context.Context, l *slog.Logger) {
				l.With("svc", "links").WithGroup("db").InfoContext(ctx, "m", "rows", 1)
			},
			want: `{"level":"INFO","msg":"m","svc":"links",` + ids + `,"db":{"rows":1}}`,
		},
		{
			name: "nested groups with attributes",
			log: func(ctx context.Context, l *slog.Logger) {
				l.WithGroup("db").With("table", "links").WithGroup("q").InfoContext(ctx, "m", "rows", 1)
			},
			want: `{"level":"INFO","msg":"m",` + ids + `,"db":{"table":"links","q":{"rows":1}}}`,
		},
		{
			name: "loggers derived from one group",
			log: func(ctx context.Context, l *slog.Logger) {
				db := l.WithGroup("db")
				db.With("table", "links").InfoContext(ctx, "m", "rows", 1)
				db.With("table", "users").InfoContext(ctx, "m", "rows", 2)
			},
			want: `{"level":"INFO","msg":"m",` + ids + `,"db":{"table":"links","rows":1}}` + "\n" +
				`{"level":"INFO","msg":"m",` + ids + `,"db":{"table":"users","rows":2}}`,
		},
		{
			name: "group without attributes",
			log:  func(ctx context.Context, l *slog.Logger) { l.WithGroup("db").InfoContext(ctx, "m") },
			want: `{"level":"INFO","msg":"m",` + ids + `}`,
		},
		{
			name: "empty group name",
			log:  func(ctx context.Context, l *slog.Logger) { l.WithGroup("").InfoContext(ctx, "m", "rows", 1) },
			want: `{"level":"INFO","msg":"m",` + ids + `,"rows":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logInCall(t, tt.log); got != tt.want+"\n" {
				t.Errorf("logged:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestLogHandlerMissingValues(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, noTime)))

	l.InfoContext(t.Context(), "no values")
	l.InfoContext(tyr.WithRequestID(t.Context(), "req-1"), "request ID only")
	op := tyr.New().Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		l.InfoContext(ctx, "operation only")
		return nil, nil
	})
	if _, err := op.Call(t.Context(), nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	want := `{"level":"INFO","msg":"no values"}
{"level":"INFO","msg":"request ID only","request_id":"req-1"}
{"level":"INFO","msg":"operation only","operation":"links.get"}
`
	if got := buf.String(); got != want {
		t.Errorf("logged:\n%s\nwant:\n%s", got, want)
	}
}

func TestLogHandlerSameOutput(t *testing.T) {
	logAll := func(l *slog.Logger) {
		l.Info("flat", "a", 1, slog.Group("g", "b", 2))
		l.With("svc", "links").WithGroup("db").With("table", "links").WithGroup("q").Info("nested", "rows", 3)
		l.WithGroup("db").Info("group without attributes")
		l.WithGroup("").Info("empty group name", "rows", 3)
	}
	handlers := []struct {
		name string
		new  func(w io.Writer) slog.Handler
	}{
		{"JSON", func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, noTime) }},
		{"Text", func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, noTime) }},
	}

	// Without values in the context, NewLogHandler only moves the groups
	// from WithGroup into the records, which these handlers print the same.
	for _, h := range handlers {
		var plain, wrapped bytes.Buffer
		logAll(slog.New(h.new(&plain)))
		logAll(slog.New(tyr.NewLogHandler(h.new(&wrapped))))
		if plain.String() != wrapped.String() {
			t.Errorf("%s: NewLogHandler changed the output to:\n%s\nwant:\n%s", h.name, &wrapped, &plain)
		}
	}
}

func TestLogHandlerCoreRecords(t *testing.T) {
	var buf bytes.Buffer
	api := tyr.New(tyr.WithLogger(slog.New(tyr.NewLogHandler(slog.NewJSONHandler(&buf, noTime)))))
	failing := api.Handle("links.get", func(ctx context.Context, req getLinkReq) (*link, error) {
		return nil, errors.New("db: connection refused")
	})
	panicking := api.Handle("links.purge", func(ctx context.Context, req getLinkReq) (*link, error) {
		panic("boom")
	})

	ctx := tyr.WithRequestID(t.Context(), "req-1")
	for _, op := range []*tyr.Operation{failing, panicking} {
		buf.Reset()
		_, _ = op.Call(ctx, nil)

		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("%s: unmarshal %q: %v", op.Name(), buf.Bytes(), err)
		}
		if got["request_id"] != "req-1" || got["operation"] != op.Name() {
			t.Errorf("%s logged %v, want it with request_id and operation", op.Name(), got)
		}
	}
}

func TestLogHandlerEmptyWith(t *testing.T) {
	// slog.Logger doesn't pass these on, but the slog.Handler contract says
	// that WithGroup("") returns the receiver.
	h := tyr.NewLogHandler(slog.NewJSONHandler(io.Discard, nil))
	for _, h := range []slog.Handler{h, h.WithGroup("db")} {
		if h.WithAttrs(nil) != h || h.WithGroup("") != h {
			t.Errorf("WithAttrs(nil) or WithGroup(\"\") didn't return the receiver %v", h)
		}
	}
}

func TestLogHandlerEnabled(t *testing.T) {
	h := tyr.NewLogHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if h.Enabled(t.Context(), slog.LevelInfo) || !h.Enabled(t.Context(), slog.LevelWarn) {
		t.Error("Enabled doesn't follow the level of the wrapped handler")
	}
}

func TestNewLogHandlerNil(t *testing.T) {
	if got, want := panicValue(func() { tyr.NewLogHandler(nil) }), "tyr: NewLogHandler: nil handler"; got != want {
		t.Errorf("NewLogHandler(nil) panicked with %v, want %q", got, want)
	}
}
