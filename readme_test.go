package tyr_test

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// readmePackages are the directories of the packages whose examples
// README.md shows, by the names its markers give them.
var readmePackages = map[string]string{
	"tyr":        ".",
	"ctxkey":     "ctxkey",
	"health":     "health",
	"jsonrpc":    "jsonrpc",
	"middleware": "middleware",
	"oteltyr":    "oteltyr",
	"playground": "validate/playground",
	"rest":       "rest",
}

// readmeMarker precedes a block of README.md that shows results of an
// example, such as rest.ExampleMount.
var readmeMarker = regexp.MustCompile(`^<!-- Output: (\w+\.Example\w*) -->$`)

// TestREADME checks the results that README.md shows against the output of
// the examples they come from, which go test checks in turn, so that README
// can't tell of a behavior the code no longer has. A block of code that
// shows results follows a marker with the example:
//
//	<!-- Output: rest.ExampleMount -->
//
// Its results are what follows "// => " in Go and "# => " in other blocks,
// such as those of a shell. Each must be a line of the output of the
// example, in the order of the output. A block that shows results without
// a marker fails the test, as does a marker that nothing follows or that
// names an example without an output.
func TestREADME(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	outputs := exampleOutputs(t)
	blocks, dangling := readmeBlocks(string(data))
	for _, line := range dangling {
		t.Errorf("README.md:%d: no block of code right after the marker", line)
	}

	checked := 0
	for _, b := range blocks {
		results := b.results()
		if b.example == "" {
			if len(results) > 0 {
				t.Errorf("README.md:%d: the block shows results, but no <!-- Output: --> marker names their example", b.line)
			}
			continue
		}
		out, ok := outputs[b.example]
		switch {
		case !ok:
			t.Errorf("README.md:%d: no example %s with an output", b.line, b.example)
			continue
		case len(results) == 0:
			t.Errorf("README.md:%d: the block of %s shows no results", b.line, b.example)
			continue
		}
		lines := out.lines
		for _, r := range results {
			i := slices.Index(lines, r)
			if i < 0 {
				t.Errorf("README.md:%d: %s doesn't print %q after the results before it", b.line, b.example, r)
				break
			}
			if !out.unordered {
				lines = lines[i+1:]
			}
		}
		checked++
	}
	if checked == 0 {
		t.Error("README.md shows no results of examples")
	}
}

// output is the output of an example, line by line.
type output struct {
	lines     []string
	unordered bool
}

// exampleOutputs returns the outputs of the examples of readmePackages, by
// names such as rest.ExampleMount.
func exampleOutputs(t *testing.T) map[string]output {
	t.Helper()
	outputs := make(map[string]output)
	for pkg, dir := range readmePackages {
		names, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		var files []*ast.File
		for _, name := range names {
			f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		for _, ex := range doc.Examples(files...) {
			if ex.Output == "" {
				continue
			}
			var lines []string
			for line := range strings.Lines(ex.Output) {
				lines = append(lines, strings.TrimSpace(line))
			}
			outputs[pkg+".Example"+ex.Name] = output{lines: lines, unordered: ex.Unordered}
		}
	}
	return outputs
}

// block is a fenced block of code of README.md.
type block struct {
	line    int    // of the opening fence
	lang    string // after the fence, such as go
	lines   []string
	example string // that the marker right before the block names, if any
}

// readmeBlocks returns the blocks of code of readme and the lines of the
// markers that no block follows.
func readmeBlocks(readme string) (blocks []block, dangling []int) {
	var cur *block
	example, markerLine := "", 0 // of the last marker, until a block or other text
	for i, line := range strings.Split(readme, "\n") {
		if cur != nil {
			if strings.HasPrefix(line, "```") {
				blocks = append(blocks, *cur)
				cur = nil
			} else {
				cur.lines = append(cur.lines, line)
			}
			continue
		}
		switch m := readmeMarker.FindStringSubmatch(strings.TrimSpace(line)); {
		case strings.HasPrefix(line, "```"):
			cur = &block{line: i + 1, lang: strings.TrimPrefix(line, "```"), example: example}
			example = ""
		case m != nil:
			if example != "" {
				dangling = append(dangling, markerLine)
			}
			example, markerLine = m[1], i+1
		case strings.TrimSpace(line) != "" && example != "":
			dangling = append(dangling, markerLine)
			example = ""
		}
	}
	if example != "" {
		dangling = append(dangling, markerLine)
	}
	return blocks, dangling
}

// results returns the results that b shows.
func (b block) results() []string {
	sep := "# => "
	if b.lang == "go" {
		sep = "// => "
	}
	var results []string
	for _, line := range b.lines {
		if _, r, ok := strings.Cut(line, sep); ok {
			results = append(results, strings.TrimSpace(r))
		}
	}
	return results
}
