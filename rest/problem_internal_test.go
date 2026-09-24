package rest

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestWriteErrorOfOtherType(t *testing.T) {
	// Operation.Call returns only *tyr.Error; another error would reach
	// writeError as nil and still be sent, as an internal one.
	rec := httptest.NewRecorder()
	writeError(t.Context(), slog.New(slog.DiscardHandler), rec, nil, &mount{})

	want := `{"type":"/problems/internal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}`
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != want {
		t.Errorf("writeError() = %d %s, want 500 %s", rec.Code, rec.Body, want)
	}
}

func TestMinimalProblem(t *testing.T) {
	// Written without the encoder, it's what the encoder writes.
	var problems []problem
	for _, status := range []int{400, 404, 413, 499, 500, 503, 599} {
		problems = append(problems, blank(status, "detail"))
	}
	m := &mount{}
	for k := range tyr.KindCanceled + 1 {
		problems = append(problems, problem{Type: m.problemType(k), Title: kindTitle(k), Status: statusOf(k)})
	}
	for _, p := range problems {
		want, err := json.Marshal(problem{Type: p.Type, Title: p.Title, Status: p.Status})
		if err != nil {
			t.Fatal(err)
		}
		if got := minimalProblem(p); !bytes.Equal(got, want) {
			t.Errorf("minimalProblem(%v) = %s, want %s", p, got, want)
		}
	}
}

func TestKindTitles(t *testing.T) {
	// Every kind has a title of its own, and a problem type of its own name.
	titles := make(map[string]tyr.Kind)
	m := &mount{}
	for k := tyr.Kind(0); !strings.HasPrefix(k.String(), "Kind("); k++ {
		title := kindTitle(k)
		if other, ok := titles[title]; ok {
			t.Errorf("kinds %v and %v have the same title %q", other, k, title)
		}
		titles[title] = k
		if got, want := m.problemType(k), "/problems/"+k.String(); got != want {
			t.Errorf("problemType(%v) = %q, want %q", k, got, want)
		}
	}
	if len(titles) != int(tyr.KindCanceled)+1 {
		t.Errorf("%d kinds have titles, want %d", len(titles), tyr.KindCanceled+1)
	}

	// A kind rest doesn't know is internal.
	if title := kindTitle(tyr.Kind(42)); title != "Internal Error" {
		t.Errorf("kindTitle(Kind(42)) = %q, want that of KindInternal", title)
	}
}
