package jsonrpc

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/jsonschema"
	"github.com/tyr-go/tyr/internal/plan"
	"github.com/tyr-go/tyr/internal/tagcheck"
)

// discoverMethod is the method that answers with the OpenRPC document of
// the API, as the OpenRPC specification names it.
const discoverMethod = "rpc.discover"

// openRPCVersion is the version of OpenRPC of the documents.
const openRPCVersion = "1.4.1"

// The objects of an OpenRPC document that jsonrpc writes.
type (
	openRPCDoc struct {
		OpenRPC    string            `json:"openrpc"`
		Info       openRPCInfo       `json:"info"`
		Methods    []*method         `json:"methods"`
		Components openRPCComponents `json:"components,omitzero"`
	}
	openRPCInfo struct {
		Title       string `json:"title"`
		Description string `json:"description,omitzero"`
		Version     string `json:"version"`
	}
	openRPCComponents struct {
		Schemas jsonschema.Properties `json:"schemas,omitzero"`
	}
	method struct {
		Name           string               `json:"name"`
		Summary        string               `json:"summary,omitzero"`
		Description    string               `json:"description,omitzero"`
		Tags           []tag                `json:"tags,omitzero"`
		ParamStructure string               `json:"paramStructure"`
		Params         []*contentDescriptor `json:"params"`
		Result         *contentDescriptor   `json:"result"`
		Errors         []*methodError       `json:"errors,omitzero"`
		Examples       []*examplePairing    `json:"examples,omitzero"`
		Deprecated     bool                 `json:"deprecated,omitzero"`
	}
	tag struct {
		Name string `json:"name"`
	}
	contentDescriptor struct {
		Name        string             `json:"name"`
		Description string             `json:"description,omitzero"`
		Required    bool               `json:"required,omitzero"`
		Schema      *jsonschema.Schema `json:"schema"`
	}
	methodError struct {
		Code    int        `json:"code"`
		Message string     `json:"message"`
		Data    *errorData `json:"data,omitzero"`
	}
	examplePairing struct {
		Name   string          `json:"name"`
		Params []*exampleValue `json:"params"`
		Result *exampleValue   `json:"result,omitzero"`
	}
	exampleValue struct {
		Name  string         `json:"name"`
		Value jsontext.Value `json:"value"`
	}
)

// openRPCOf returns the OpenRPC document of the operations of api, as
// Handler serves them, in JSON. It panics on a type of a request or a
// result that JSON can't carry; fn names the function in panics.
func openRPCOf(api *tyr.API, info tyr.Info, fn string) jsontext.Value {
	schemas := plan.NewSchemas("#/components/schemas/")
	schemas.SetFailing(tagcheck.Failing(api.Validator()))
	doc := &openRPCDoc{
		OpenRPC: openRPCVersion,
		Info:    openRPCInfo{Title: info.Title, Description: info.Description, Version: info.Version},
		Methods: []*method{},
	}
	for op := range api.Operations() {
		m, err := methodOf(op, schemas)
		if err != nil {
			panic(fmt.Sprintf("jsonrpc: %s: operation %q: %v", fn, op.Name(), err))
		}
		doc.Methods = append(doc.Methods, m)
	}
	defs, err := schemas.Defs()
	if err != nil {
		panic("jsonrpc: " + fn + ": " + err.Error())
	}
	for _, d := range defs {
		doc.Components.Schemas = append(doc.Components.Schemas, jsonschema.Property{Name: d.Name, Schema: d.Schema})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		panic("jsonrpc: " + fn + ": " + err.Error()) // a bug: every part encodes
	}
	return data
}

// methodOf returns the OpenRPC method of op.
func methodOf(op *tyr.Operation, schemas *plan.Schemas) (*method, error) {
	d := op.Doc()
	m := &method{
		Name:           op.Name(),
		Summary:        d.Summary,
		Description:    d.Description,
		ParamStructure: "by-name",
		Params:         []*contentDescriptor{},
		Errors:         errorsOf(d.Errors),
		Deprecated:     d.Deprecated,
	}
	for _, t := range d.Tags {
		m.Tags = append(m.Tags, tag{t})
	}
	members, err := schemas.Members(op.Req(), plan.Input)
	if err != nil {
		return nil, err
	}
	for _, mem := range members {
		m.Params = append(m.Params, &contentDescriptor{Name: mem.Name, Description: mem.Description, Required: mem.Required, Schema: mem.Schema})
	}
	res, err := schemas.Of(op.Res(), plan.Output)
	if err != nil {
		return nil, err
	}
	m.Result = &contentDescriptor{Name: "result", Schema: res}

	for _, ex := range d.Examples {
		values := make(map[string]jsontext.Value)
		req, _ := json.Marshal(ex.Req) // Implement checked that it encodes
		_ = json.Unmarshal(req, &values)
		pairing := &examplePairing{Name: ex.Name, Params: []*exampleValue{}}
		for _, mem := range members {
			// null is as good as none: it decodes to the zero value.
			if v, ok := values[mem.Name]; ok && v.Kind() != 'n' {
				pairing.Params = append(pairing.Params, &exampleValue{Name: mem.Name, Value: v})
			}
		}
		result, _ := json.Marshal(ex.Res)
		pairing.Result = &exampleValue{Name: "result", Value: result}
		m.Examples = append(m.Examples, pairing)
	}
	return m, nil
}

// errorsOf returns the errors of the kinds and of invalid_argument, which
// any call may get, one per code, as OpenRPC wants them, in the order of
// the codes. The message names the kinds of the code, and the data is that
// of the errors of a single kind.
func errorsOf(kinds []tyr.Kind) []*methodError {
	var codes []int
	byCode := make(map[int][]tyr.Kind)
	for _, k := range append([]tyr.Kind{tyr.KindInvalidArgument}, kinds...) {
		code := codeOf(k)
		if !slices.Contains(byCode[code], k) {
			byCode[code] = append(byCode[code], k)
		}
		if !slices.Contains(codes, code) {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	var out []*methodError
	for _, code := range codes {
		var names []string
		for _, k := range byCode[code] {
			names = append(names, strings.ReplaceAll(k.String(), "_", " "))
		}
		e := &methodError{Code: code, Message: strings.Join(names, " or ")}
		if ks := byCode[code]; len(ks) == 1 && code != codeInternalError {
			e.Data = &errorData{Kind: ks[0].String()}
		}
		out = append(out, e)
	}
	return out
}
