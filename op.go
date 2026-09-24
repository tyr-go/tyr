package tyr

import (
	"fmt"
	"reflect"
	"slices"
)

// Op is the contract of an operation: its name, the types of its request
// and its result, and its options, such as a route for a transport. It is a
// plain value that the server and its clients share, such as the client of
// [github.com/tyr-go/tyr/jsonrpc], and the compiler checks both sides
// against it:
//
//	// package contract, which the server and its clients import
//	var GetLink = tyr.Define[GetLinkReq, *Link]("links.get", rest.Route("GET /links/{code}"))
//
//	// the server
//	api.Implement(contract.GetLink, links.Get) // doesn't compile unless links.Get fits
//
// Make an Op with [Define]. The zero Op has no name: [API.Implement] panics
// on it.
type Op[Req, Res any] struct {
	name string
	opts []OpOption
}

// Define returns the contract of an operation with the given name, request
// type Req and result type Res. opts apply to the operation when it is
// implemented, in order, after the options of its groups. Define registers
// nothing; see [API.Implement].
//
// The name is as for [API.Handle], and Req must be a struct type. Define
// panics if either is wrong or an option is nil, so that a contract
// declared as a package-level variable fails at startup, as a
// regexp.MustCompile does, in a program that only calls the operation too.
// What depends on the API, such as a name already taken or the validate
// tags of Req, is checked when the operation is implemented.
func Define[Req, Res any](name string, opts ...OpOption) Op[Req, Res] {
	return define[Req, Res](fmt.Sprintf("Define(%q)", name), name, opts)
}

// define implements Define for call, which names the call in panics.
func define[Req, Res any](call, name string, opts []OpOption) Op[Req, Res] {
	if problem := checkName(name); problem != "" {
		panic("tyr: " + call + ": " + problem)
	}
	if t := reflect.TypeFor[Req](); t.Kind() != reflect.Struct {
		msg := fmt.Sprintf("tyr: %s: request type %v is not a struct", call, t)
		if t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct {
			msg += fmt.Sprintf("; use %v", t.Elem())
		}
		panic(msg)
	}
	for _, opt := range opts {
		if opt == nil {
			panic("tyr: " + call + ": nil option")
		}
	}
	return Op[Req, Res]{name: name, opts: slices.Clone(opts)}
}

// Name returns the name of the operation, or "" for the zero Op.
func (o Op[Req, Res]) Name() string {
	return o.name
}

// Example returns a copy of the contract with an example of a call for the
// documentation of the operation: req and the result res that it gets. The
// compiler checks their types, as it checks the handler and the calls of
// clients:
//
//	var GetLink = tyr.Define[GetLinkReq, *Link]("links.get", rest.Route("GET /links/{code}")).
//		Example("go", GetLinkReq{Code: "go"}, &Link{Code: "go", URL: "https://go.dev"})
//
// [API.Implement] checks the rest when the operation is registered, as it
// does for the option [Example]. Example panics if the name is empty.
func (o Op[Req, Res]) Example(name string, req Req, res Res) Op[Req, Res] {
	return Op[Req, Res]{name: o.name, opts: append(slices.Clip(o.opts), Example(name, req, res))}
}
