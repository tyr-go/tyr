package rest

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonschema"
	"github.com/tyr-go/tyr/internal/plan"
)

// OpenAPI returns a handler that serves the OpenAPI 3.1 document of the
// routes, as JSON, with info on the API; the document shows the problem
// types and the challenges of the options of [Mount]. The document is made
// once, here.
//
//	routes := rest.Mount(mux, api)
//	mux.Handle("GET /openapi.json", routes.OpenAPI(tyr.Info{Title: "shortlink", Version: "1.0.0"}))
//
// An operation gets its route, its operationId, which is its name, and its
// documentation (see [tyr.Doc]). The fields of its request tagged path,
// query or header are its parameters, the other members its JSON body, and
// the fields of its result tagged header the headers of its success. Its
// errors are invalid_argument, which any request may get, and the kinds of
// [tyr.Errors], as application/problem+json, by status; default stands for
// the rest. The schemas are JSON Schema 2020-12 and say what the server
// reads and writes, as json/v2 does; the validate tags of requests add
// their constraints, and doc tags describe fields.
//
// The schemas fall short of the server in two ways. The schema of an
// element of a slice or a map is that of its type, with the constraints of
// its validate tags, which the server doesn't check without dive. And some
// JSON fits the schema of a request but doesn't decode, since JSON Schema
// can't tell how a value is written, such as an integer written as 1.0, a
// number beyond the range of its type, a time that time.Parse doesn't
// read, or a string that a type which parses itself rejects.
//
// OpenAPI panics if info has no Title or Version, a route has a method that
// OpenAPI 3.1 doesn't know, two routes of different hosts have the same
// method and path, or a type of a request or a result has a field that
// JSON can't carry, such as a time.Duration.
func (rs *Routes) OpenAPI(info tyr.Info) http.Handler {
	if info.Title == "" || info.Version == "" {
		panic("rest: OpenAPI: the Info needs a Title and a Version")
	}
	data, err := json.Marshal(openAPIOf(rs, info), jsontext.WithIndent("  "))
	if err != nil {
		panic("rest: OpenAPI: " + err.Error()) // a bug: every part encodes
	}
	return document(data)
}

// document serves a document as JSON.
type document []byte

func (d document) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(d)
}

// openAPIVersion is the version of OpenAPI of the documents.
const openAPIVersion = "3.1.2"

// The objects of an OpenAPI document that rest writes, with their members
// in the order of the specification.
type (
	openAPIDoc struct {
		OpenAPI    string             `json:"openapi"`
		Info       openAPIInfo        `json:"info"`
		Paths      ordered[*pathItem] `json:"paths"`
		Components openAPIComponents  `json:"components,omitzero"`
	}
	openAPIInfo struct {
		Title       string `json:"title"`
		Description string `json:"description,omitzero"`
		Version     string `json:"version"`
	}
	openAPIComponents struct {
		Schemas jsonschema.Properties `json:"schemas,omitzero"`
	}
	pathItem struct {
		Get     *operation `json:"get,omitzero"`
		Put     *operation `json:"put,omitzero"`
		Post    *operation `json:"post,omitzero"`
		Delete  *operation `json:"delete,omitzero"`
		Options *operation `json:"options,omitzero"`
		Head    *operation `json:"head,omitzero"`
		Patch   *operation `json:"patch,omitzero"`
		Trace   *operation `json:"trace,omitzero"`
	}
	operation struct {
		Tags        []string           `json:"tags,omitzero"`
		Summary     string             `json:"summary,omitzero"`
		Description string             `json:"description,omitzero"`
		OperationID string             `json:"operationId"`
		Parameters  []*parameter       `json:"parameters,omitzero"`
		RequestBody *requestBody       `json:"requestBody,omitzero"`
		Responses   ordered[*response] `json:"responses"`
		Deprecated  bool               `json:"deprecated,omitzero"`
		Servers     []server           `json:"servers,omitzero"`
	}
	parameter struct {
		Name        string             `json:"name"`
		In          string             `json:"in"`
		Description string             `json:"description,omitzero"`
		Required    bool               `json:"required,omitzero"`
		Schema      *jsonschema.Schema `json:"schema"`
		Examples    ordered[example]   `json:"examples,omitzero"`
	}
	requestBody struct {
		Content  ordered[*mediaType] `json:"content"`
		Required bool                `json:"required,omitzero"`
	}
	mediaType struct {
		Schema   *jsonschema.Schema `json:"schema"`
		Examples ordered[example]   `json:"examples,omitzero"`
	}
	response struct {
		Description string              `json:"description"`
		Headers     ordered[*header]    `json:"headers,omitzero"`
		Content     ordered[*mediaType] `json:"content,omitzero"`
	}
	header struct {
		Description string             `json:"description,omitzero"`
		Schema      *jsonschema.Schema `json:"schema"`
		Examples    ordered[example]   `json:"examples,omitzero"`
	}
	example struct {
		Value jsontext.Value `json:"value"`
	}
	server struct {
		URL string `json:"url"`
	}
)

// ordered is a JSON object whose members keep their order.
type ordered[T any] []member[T]

// member is a member of an ordered object.
type member[T any] struct {
	name  string
	value T
}

// MarshalJSONTo writes o as a JSON object, in order.
func (o ordered[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, m := range o {
		if err := enc.WriteToken(jsontext.String(m.name)); err != nil {
			return err
		}
		if err := json.MarshalEncode(enc, m.value); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// get returns the value of the member name and reports whether o has it.
func (o ordered[T]) get(name string) (T, bool) {
	for _, m := range o {
		if m.name == name {
			return m.value, true
		}
	}
	var zero T
	return zero, false
}

// openAPIOf returns the OpenAPI document of the routes, as Mount serves
// them.
func openAPIOf(rs *Routes, info tyr.Info) *openAPIDoc {
	b := &openAPIBuilder{mount: rs.mount, schemas: plan.NewSchemas("#/components/schemas/")}
	b.schemas.Name(reflect.TypeFor[problem](), "Problem")
	problemRef, err := b.schemas.Of(reflect.TypeFor[problem](), plan.Output)
	if err != nil {
		panic("rest: OpenAPI: " + err.Error()) // a bug: problem encodes
	}
	b.problem = ordered[*mediaType]{{"application/problem+json", &mediaType{Schema: problemRef}}}

	doc := &openAPIDoc{
		OpenAPI: openAPIVersion,
		Info:    openAPIInfo{Title: info.Title, Description: info.Description, Version: info.Version},
		Paths:   ordered[*pathItem]{},
	}
	for _, h := range rs.handlers {
		op := h.op
		method, host, path := splitPattern(h.pattern)
		o := b.operation(op, h)
		if host != "" {
			o.Servers = []server{{URL: "//" + host}}
		}
		item, ok := doc.Paths.get(path)
		if !ok {
			item = &pathItem{}
			doc.Paths = append(doc.Paths, member[*pathItem]{path, item})
		}
		slot := item.method(method)
		switch {
		case slot == nil:
			panicf(op, "OpenAPI 3.1 has no method %s", method)
		case *slot != nil:
			panicf(op, "operation %q is at %s %s too", (*slot).OperationID, method, path)
		}
		*slot = o
	}

	for _, d := range b.schemas.Defs() {
		doc.Components.Schemas = append(doc.Components.Schemas, jsonschema.Property{Name: d.Name, Schema: d.Schema})
	}
	describeProblem(problemRef.Ref.Schema)
	return doc
}

// splitPattern returns the method, the host and the path of a pattern of
// ServeMux, the path as OpenAPI writes it: {name...} is {name}, and {$} is
// left out.
func splitPattern(pattern string) (method, host, path string) {
	i := strings.IndexAny(pattern, " \t")
	method, rest := pattern[:i], strings.TrimLeft(pattern[i+1:], " \t")
	j := strings.IndexByte(rest, '/')
	host, path = rest[:j], rest[j:]
	segs := strings.Split(path, "/")
	for k, seg := range segs {
		switch {
		case seg == "{$}":
			segs[k] = ""
		case strings.HasSuffix(seg, "...}"):
			segs[k] = strings.TrimSuffix(seg, "...}") + "}"
		}
	}
	return method, host, strings.Join(segs, "/")
}

// method returns the slot of the operation with method in p, or nil if
// OpenAPI 3.1 has no such method.
func (p *pathItem) method(method string) **operation {
	switch method {
	case http.MethodGet:
		return &p.Get
	case http.MethodPut:
		return &p.Put
	case http.MethodPost:
		return &p.Post
	case http.MethodDelete:
		return &p.Delete
	case http.MethodOptions:
		return &p.Options
	case http.MethodHead:
		return &p.Head
	case http.MethodPatch:
		return &p.Patch
	case http.MethodTrace:
		return &p.Trace
	}
	return nil
}

// openAPIBuilder builds the operations of a document.
type openAPIBuilder struct {
	mount   *mount
	schemas *plan.Schemas
	problem ordered[*mediaType] // the content of a problem
}

// operation returns the OpenAPI operation of op, which h serves.
func (b *openAPIBuilder) operation(op *tyr.Operation, h *handler) *operation {
	d := op.Doc()
	o := &operation{
		Tags:        d.Tags,
		Summary:     d.Summary,
		Description: d.Description,
		OperationID: op.Name(),
		Deprecated:  d.Deprecated,
	}
	members, err := b.schemas.Members(op.Req(), plan.Input)
	if err != nil {
		panicf(op, "%v", err)
	}

	// The bound fields are parameters, the other members the body.
	var body []plan.Member
	for _, mem := range members {
		i := slices.IndexFunc(h.binding.Fields, func(f plan.Field) bool { return f.JSON == mem.Name })
		if i < 0 {
			body = append(body, mem)
			continue
		}
		f := h.binding.Fields[i]
		o.Parameters = append(o.Parameters, &parameter{
			Name:        f.Name,
			In:          f.Source.Tag(),
			Description: mem.Description,
			Required:    f.Source == plan.Path || mem.Required,
			Schema:      paramSchema(mem.Schema, op.Req().FieldByIndex(mem.Index).Type, f.Source),
		})
	}
	if len(body) > 0 {
		sch := plan.Object(body)
		if len(body) == len(members) { // the definition of the request
			if sch, err = b.schemas.Of(op.Req(), plan.Input); err != nil {
				panicf(op, "%v", err)
			}
		}
		o.RequestBody = &requestBody{
			Content:  ordered[*mediaType]{{"application/json", &mediaType{Schema: sch}}},
			Required: slices.ContainsFunc(body, func(m plan.Member) bool { return m.Required }),
		}
	}

	success := &response{Description: statusText(h.status)}
	for _, f := range h.headers.Fields() {
		sch, err := b.schemas.Of(f.Type, plan.Output)
		if err != nil {
			panicf(op, "%v", err)
		}
		success.Headers = append(success.Headers, member[*header]{f.Name, &header{
			Description: resultField(op.Res(), f.Index).Tag.Get("doc"),
			Schema:      paramSchema(sch, f.Type, plan.Header),
		}})
	}
	if !h.noBody {
		sch, err := b.schemas.Of(op.Res(), plan.Output)
		if err != nil {
			panicf(op, "%v", err)
		}
		success.Content = ordered[*mediaType]{{"application/json", &mediaType{Schema: sch}}}
	}
	o.Responses = ordered[*response]{{strconv.Itoa(h.status), success}}
	o.Responses = append(o.Responses, b.errors(d.Errors)...)

	for _, ex := range d.Examples {
		b.example(o, h, members, ex)
	}
	return o
}

// errors returns the responses of the errors of the kinds and those of
// invalid_argument, which any request may get, by status, and default, for
// the rest.
func (b *openAPIBuilder) errors(kinds []tyr.Kind) ordered[*response] {
	all := append([]tyr.Kind{tyr.KindInvalidArgument}, kinds...)
	var statuses []int
	titles := make(map[int][]string)
	for _, k := range all {
		status := statusOf(k)
		title := kindTitle(k)
		if !slices.Contains(titles[status], title) {
			titles[status] = append(titles[status], title)
		}
		if !slices.Contains(statuses, status) {
			statuses = append(statuses, status)
		}
	}
	slices.Sort(statuses)
	var out ordered[*response]
	for _, status := range statuses {
		r := &response{Description: strings.Join(titles[status], " or "), Content: b.problem}
		if status == http.StatusUnauthorized && len(b.mount.challenges) > 0 {
			var examples ordered[example]
			for i, c := range b.mount.challenges {
				examples = append(examples, member[example]{strconv.Itoa(i + 1), example{quote(c)}})
			}
			r.Headers = ordered[*header]{{"WWW-Authenticate", &header{
				Description: "A challenge per scheme that the server accepts.",
				Schema:      &jsonschema.Schema{Type: jsonschema.Types{"string"}},
				Examples:    examples,
			}}}
		}
		out = append(out, member[*response]{strconv.Itoa(status), r})
	}
	return append(out, member[*response]{"default", &response{Description: "An error of another kind, or a problem of the HTTP request itself.", Content: b.problem}})
}

// example adds ex to the parameters, the body, the success and its headers
// of o, which h serves; members are those of the request.
func (b *openAPIBuilder) example(o *operation, h *handler, members []plan.Member, ex tyr.ExampleCall) {
	values := make(map[string]jsontext.Value)
	req, _ := json.Marshal(ex.Req) // Implement checked that it encodes
	_ = json.Unmarshal(req, &values)

	body := ordered[jsontext.Value]{}
	for _, mem := range members {
		v, ok := values[mem.Name]
		if !ok || v.Kind() == 'n' { // null is as good as none: it decodes to the zero value
			continue
		}
		if i := slices.IndexFunc(o.Parameters, func(p *parameter) bool { return h.bindsMember(p, mem.Name) }); i >= 0 {
			o.Parameters[i].Examples = append(o.Parameters[i].Examples, member[example]{ex.Name, example{v}})
			continue
		}
		body = append(body, member[jsontext.Value]{mem.Name, v})
	}
	if o.RequestBody != nil {
		value, _ := json.Marshal(body)
		mt := o.RequestBody.Content[0].value
		mt.Examples = append(mt.Examples, member[example]{ex.Name, example{value}})
	}

	success := o.Responses[0].value
	if success.Content != nil {
		res, _ := json.Marshal(ex.Res)
		mt := success.Content[0].value
		mt.Examples = append(mt.Examples, member[example]{ex.Name, example{res}})
	}
	headers, err := h.headers.Of(reflect.ValueOf(ex.Res))
	if err != nil {
		panicf(h.op, "example %q: %v", ex.Name, err)
	}
	for _, hd := range success.Headers {
		if v := headers.Get(hd.name); v != "" {
			hd.value.Examples = append(hd.value.Examples, member[example]{ex.Name, example{quote(v)}})
		}
	}
}

// bindsMember reports whether the parameter p is bound to the member name
// of the request.
func (h *handler) bindsMember(p *parameter, name string) bool {
	return slices.ContainsFunc(h.binding.Fields, func(f plan.Field) bool {
		return f.JSON == name && f.Name == p.Name && f.Source.Tag() == p.In
	})
}

// paramSchema returns the schema of a parameter or a header from sch, that
// of the values of a field of type t: without null, which a parameter
// can't be, and, for a time in a header, a string, which is an HTTP date.
func paramSchema(sch *jsonschema.Schema, t reflect.Type, src plan.Source) *jsonschema.Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if src == plan.Header && t == reflect.TypeFor[time.Time]() {
		return &jsonschema.Schema{Type: jsonschema.Types{"string"}}
	}
	if len(sch.AnyOf) == 2 && slices.Equal(sch.AnyOf[1].Type, jsonschema.Types{"null"}) {
		return sch.AnyOf[0]
	}
	if i := slices.Index(sch.Type, "null"); i >= 0 {
		c := *sch
		c.Type = slices.Delete(slices.Clone(sch.Type), i, i+1)
		c.Enum = slices.DeleteFunc(slices.Clone(sch.Enum), func(v jsontext.Value) bool { return string(v) == "null" })
		if len(c.Enum) == 0 {
			c.Enum = nil
		}
		return &c
	}
	return sch
}

// resultField returns the field at index of the result type t, a struct or
// a pointer to one.
func resultField(t reflect.Type, index []int) reflect.StructField {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.FieldByIndex(index)
}

// describeProblem adds to the schema of a problem what its Go type can't
// say: the type is a URI reference, relative by default, and the kind one
// of those tyr defines.
func describeProblem(sch *jsonschema.Schema) {
	for _, p := range sch.Properties {
		switch p.Name {
		case "type":
			p.Schema.Format = "uri-reference"
		case "kind":
			for k := tyr.Kind(0); !strings.HasPrefix(k.String(), "Kind("); k++ {
				p.Schema.Enum = append(p.Schema.Enum, quote(k.String()))
			}
		}
	}
}

// quote returns the JSON string of s.
func quote(s string) jsontext.Value {
	b, _ := jsontext.AppendQuote(nil, s)
	return b
}
