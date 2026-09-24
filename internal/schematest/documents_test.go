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

// openRPCMetaSchema returns the official schema of OpenRPC 1.4 compiled.
// It and the schema of its schemas, from meta.json-schema.tools, name the
// latter as their $schema, which names itself, and they are Draft 7 but for
// that: they are read as Draft 7. The OpenRPC schema refers to the other
// without the slash of its $id, which a loader resolves.
func openRPCMetaSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	docs := make(map[string]any)
	for name, id := range map[string]string{
		"openrpc-1.4.1-schema.json":           "https://meta.open-rpc.org/",
		"openrpc-json-schema-tools-meta.json": "https://meta.json-schema.tools/",
	} {
		doc := load(t, filepath.Join("testdata", "metaschemas", name)).(map[string]any)
		delete(doc, "$schema")
		docs[id] = doc
		if err := c.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
	}
	c.UseLoader(jsonschema.SchemeURLLoader{"https": loaderFunc(func(url string) (any, error) {
		if doc, ok := docs[url+"/"]; ok {
			return doc, nil
		}
		return nil, os.ErrNotExist
	})})
	sch, err := c.Compile("https://meta.open-rpc.org/")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// loaderFunc is a jsonschema.URLLoader of a function.
type loaderFunc func(url string) (any, error)

func (f loaderFunc) Load(url string) (any, error) { return f(url) }

func TestOpenRPCDocuments(t *testing.T) {
	// Every OpenRPC document in the repository, such as the golden files of
	// jsonrpc, fits the official schema of OpenRPC 1.4, which checks its
	// schemas as JSON Schema Draft 7.
	openRPC := openRPCMetaSchema(t)
	for _, path := range documents(t, "openrpc*.json") {
		t.Run(path, func(t *testing.T) {
			doc := load(t, path)
			if err := openRPC.Validate(doc); err != nil {
				t.Errorf("%s doesn't fit OpenRPC 1.4: %v", path, err)
			}
			checkPairings(t, path, doc)
		})
	}
}

func TestOpenRPCMetaSchema(t *testing.T) {
	// The meta-schema does reject what isn't OpenRPC 1.4, so that the test
	// of the documents can fail.
	openRPC := openRPCMetaSchema(t)
	for _, doc := range []string{
		`{"openrpc":"1.4.1","methods":[]}`,
		`{"openrpc":"1.3.2","info":{"title":"t","version":"1"},"methods":[]}`,
		`{"openrpc":"1.4.1","info":{"title":"t","version":"1"},"methods":[{"name":"m"}]}`,
		`{"openrpc":"1.4.1","info":{"title":"t","version":"1"},"methods":[{"name":"m","params":[{"name":"p","schema":{"type":"strin"}}]}]}`,
	} {
		if err := openRPC.Validate(decode(t, []byte(doc))); err == nil {
			t.Errorf("%s fits OpenRPC 1.4, want it rejected", doc)
		}
	}
}

// checkPairings checks that the values of the example pairings of the
// methods in doc, an OpenRPC document at path, fit the schemas of their
// params and results, as JSON Schema Draft 7.
func checkPairings(t *testing.T, path string, doc any) {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	c.AssertFormat()
	const url = "https://tyr.test/document.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	checked := 0
	methods, _ := doc.(map[string]any)["methods"].([]any)
	for i, m := range methods {
		m := m.(map[string]any)
		schemaOf := func(at ...string) *jsonschema.Schema {
			sch, err := c.Compile(url + "#" + pointer(append([]string{"methods", strconv.Itoa(i)}, at...)))
			if err != nil {
				t.Fatalf("%s: compiling %v of %v: %v", path, at, m["name"], err)
			}
			return sch
		}
		params, _ := m["params"].([]any)
		examples, _ := m["examples"].([]any)
		for _, ex := range examples {
			ex := ex.(map[string]any)
			for _, p := range ex["params"].([]any) {
				p := p.(map[string]any)
				j := slices.IndexFunc(params, func(d any) bool { return d.(map[string]any)["name"] == p["name"] })
				if j < 0 {
					t.Errorf("%s: example %v of %v has the param %v, which the method hasn't", path, ex["name"], m["name"], p["name"])
					continue
				}
				if err := schemaOf("params", strconv.Itoa(j), "schema").Validate(p["value"]); err != nil {
					t.Errorf("%s: param %v of example %v of %v doesn't fit its schema: %v", path, p["name"], ex["name"], m["name"], err)
				}
				checked++
			}
			if r, ok := ex["result"].(map[string]any); ok {
				if err := schemaOf("result", "schema").Validate(r["value"]); err != nil {
					t.Errorf("%s: the result of example %v of %v doesn't fit its schema: %v", path, ex["name"], m["name"], err)
				}
				checked++
			}
		}
	}
	t.Logf("%s: %d values of examples fit their schemas", path, checked)
}
