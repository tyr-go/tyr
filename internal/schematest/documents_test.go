package schematest

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// root is the root of the repository, from the directory of the module.
const root = "../.."

// documents returns the files of the repository whose names match glob,
// such as the golden files openapi.json, outside this module.
func documents(t *testing.T, glob string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "schematest") {
			return filepath.SkipDir
		}
		if ok, _ := filepath.Match(glob, d.Name()); ok && !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("no %s in the repository", glob)
	}
	return out
}

// load returns the JSON file at path for the validator.
func load(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, data)
}

// metaschemas returns a compiler that knows the meta-schemas in
// testdata/metaschemas whose names start with prefix, by their $id.
func metaschemas(t *testing.T, prefix string) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	files, err := filepath.Glob(filepath.Join("testdata", "metaschemas", prefix+"*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no meta-schemas %s*: %v", prefix, err)
	}
	for _, f := range files {
		doc := load(t, f)
		id, _ := doc.(map[string]any)["$id"].(string)
		if err := c.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestOpenAPIDocuments(t *testing.T) {
	// Every OpenAPI document in the repository, such as the golden files of
	// rest, fits the official schema of OpenAPI 3.1, which checks its
	// schemas against the dialect of OpenAPI 3.1 too.
	c := metaschemas(t, "openapi-3.1-")
	c.AssertFormat()
	openAPI, err := c.Compile("https://spec.openapis.org/oas/3.1/schema-base/2026-08-03")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range documents(t, "openapi*.json") {
		t.Run(path, func(t *testing.T) {
			doc := load(t, path)
			if err := openAPI.Validate(doc); err != nil {
				t.Errorf("%s doesn't fit OpenAPI 3.1: %v", path, err)
			}
			checkExamples(t, path, doc, jsonschema.Draft2020)
		})
	}
}

func TestOpenAPIMetaSchema(t *testing.T) {
	// The meta-schema does reject what isn't OpenAPI 3.1, so that the test
	// of the documents can fail.
	c := metaschemas(t, "openapi-3.1-")
	openAPI, err := c.Compile("https://spec.openapis.org/oas/3.1/schema-base/2026-08-03")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{
		`{"openapi":"3.1.2","paths":{}}`,
		`{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{}}`,
		`{"openapi":"3.1.2","info":{"title":"t","version":"1"},"paths":{"/x":{"get":{"responses":{"abc":{"description":"d"}}}}}}`,
		`{"openapi":"3.1.2","info":{"title":"t","version":"1"},"components":{"schemas":{"X":{"type":"strin"}}}}`,
	} {
		if err := openAPI.Validate(decode(t, []byte(doc))); err == nil {
			t.Errorf("%s fits OpenAPI 3.1, want it rejected", doc)
		}
	}
}

// checkExamples checks that the value of every example in doc, a document
// at path whose schemas are of draft, fits the schema beside it: that of a
// parameter, a body, a response or a header.
func checkExamples(t *testing.T, path string, doc any, draft *jsonschema.Draft) {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(draft)
	c.AssertFormat()
	const url = "https://tyr.test/document.json" // a compiler per document
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	checked := 0
	var walk func(v any, at []string)
	walk = func(v any, at []string) {
		switch v := v.(type) {
		case map[string]any:
			if examples, ok := v["examples"].(map[string]any); ok && v["schema"] != nil {
				sch, err := c.Compile(url + "#" + pointer(append(slices.Clip(at), "schema")))
				if err != nil {
					t.Fatalf("%s: compiling the schema at %s: %v", path, pointer(at), err)
				}
				for name, ex := range examples {
					if err := sch.Validate(ex.(map[string]any)["value"]); err != nil {
						t.Errorf("%s: example %q at %s doesn't fit its schema: %v", path, name, pointer(at), err)
					}
					checked++
				}
			}
			for k, x := range v {
				walk(x, append(slices.Clip(at), k))
			}
		case []any:
			for i, x := range v {
				walk(x, append(slices.Clip(at), strconv.Itoa(i)))
			}
		}
	}
	walk(doc, nil)
	t.Logf("%s: %d examples fit their schemas", path, checked)
}
