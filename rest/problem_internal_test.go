package rest

import (
	"bytes"
	"encoding/json/v2"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyr-go/tyr"
)

func TestWriteErrorOfOtherType(t *testing.T) {
	// Operation.Call returns only *tyr.Error; another error would reach
	// writeError as nil and still be sent, as an internal one.
	rec := httptest.NewRecorder()
	writeError(t.Context(), slog.New(slog.DiscardHandler), rec, nil, &mount{})

	want := `{"type":"https://pkg.go.dev/github.com/tyr-go/tyr#KindInternal","title":"Internal Error","status":500,"detail":"internal error","kind":"internal"}`
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
		_, title := kindType(k)
		problems = append(problems, problem{Type: m.problemType(k), Title: title, Status: statusOf(k)})
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

func TestKindTypes(t *testing.T) {
	// The default type of a kind points to the constant of the kind in the
	// documentation of package tyr, so the constant must have that name.
	// The constants are declared with iota, in the order of their values.
	constants := kindConstants(t)
	if len(constants) != int(tyr.KindCanceled)+1 {
		t.Fatalf("errors.go declares %d kinds, %v; want %d", len(constants), constants, tyr.KindCanceled+1)
	}
	titles := make(map[string]tyr.Kind)
	for i, name := range constants {
		k := tyr.Kind(i)
		constant, title := kindType(k)
		if constant != name {
			t.Errorf("kindType(%v) = %q, the name of the constant is %q", k, constant, name)
		}
		if other, ok := titles[title]; ok {
			t.Errorf("kinds %v and %v have the same title %q", other, k, title)
		}
		titles[title] = k
	}

	// A kind rest doesn't know is internal.
	if constant, title := kindType(tyr.Kind(42)); constant != "KindInternal" || title != "Internal Error" {
		t.Errorf("kindType(Kind(42)) = %q, %q; want those of KindInternal", constant, title)
	}
}

// kindConstants returns the names of the constants of type Kind in package
// tyr, in the order of their declaration.
func kindConstants(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "errors.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok || d.Tok != token.CONST {
			continue
		}
		for _, spec := range d.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if strings.HasPrefix(name.Name, "Kind") {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names
}
