package tyr

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
)

// Doc is the documentation of an operation, for the documents that
// transports make of an API. The options [Summary], [Description], [Tags],
// [Deprecated] and [Errors] set it, and so do the examples of
// [Contract.Example]; it changes nothing at run time. See [Operation.Doc].
type Doc struct {
	Summary     string
	Description string   // in Markdown
	Tags        []string // in the order added, without repeats
	Deprecated  bool
	// Errors are the kinds of errors that the operation may return, as
	// declared, in the order added and without repeats. Transports add the
	// kinds that any operation may return, such as KindInvalidArgument for
	// a request that can't be decoded.
	Errors   []Kind
	Examples []ExampleCall
}

// ExampleCall is an example of a call of an operation: a request and the
// result it gets.
type ExampleCall struct {
	Name string
	Req  any // of the operation's Req type
	Res  any // of the operation's Res type
}

// Info describes an API in the documents that transports make of it.
type Info struct {
	Title       string // required
	Version     string // required: the version of the API, not of tyr
	Description string // in Markdown
}

// SchemaNamer is implemented by a type that names its schemas in the
// documents that transports make of an API, such as those of
// [github.com/tyr-go/tyr/rest.Routes.OpenAPI]: the schema of the type in
// results is named SchemaName(), and that in requests SchemaName()+"Input".
// Without the method, a schema is named after its Go type, such as Link and
// LinkInput, and generic types after their type arguments too, such as
// Page_Link. Only a struct type that JSON writes as an object has named
// schemas.
//
// A name depends on its type and direction only, so that adding an
// operation or changing a validate tag renames nothing. Two types of one
// name make a transport panic when it makes its document, rather than
// rename one of them behind the back of the clients that generated code
// from it; SchemaName settles it:
//
//	func (Link) SchemaName() string { return "ShortLink" } // ShortLink and ShortLinkInput
//
// The name must not depend on the value, and it is letters, digits, '.',
// '-' and '_'. A type that the method can't be added to, as one of another
// package, or an instantiation of a generic type gets a name of its own as
// a defined type: type BillingLink billing.Link. A defined type has the
// fields of the original but none of the methods declared on it, such as
// MarshalJSON, MarshalText or Validate, so the way is only for types
// without such methods: otherwise the JSON on the wire changes, or a check
// goes missing.
type SchemaNamer interface {
	SchemaName() string
}

// The keys of the documentation; Operation.Doc reads them.
var (
	summaryKey     = NewMetaKey[string]("tyr.summary")
	descriptionKey = NewMetaKey[string]("tyr.description")
	tagsKey        = NewMetaKey[[]string]("tyr.tags")
	deprecatedKey  = NewMetaKey[bool]("tyr.deprecated")
	errorsKey      = NewMetaKey[[]Kind]("tyr.errors")
	examplesKey    = NewMetaKey[[]ExampleCall]("tyr.examples")
)

// Summary sets a short summary of an operation, for its documentation. When
// several options set it, the last one applied wins, as with a [MetaKey].
func Summary(s string) OpOption {
	return summaryKey.Option(s)
}

// Description sets the description of an operation, in Markdown, for its
// documentation. When several options set it, the last one applied wins,
// as with a [MetaKey].
func Description(s string) OpOption {
	return descriptionKey.Option(s)
}

// Deprecated marks an operation as deprecated in its documentation. The
// operation still serves calls.
func Deprecated() OpOption {
	return deprecatedKey.Option(true)
}

// Tags adds tags to the documentation of an operation, by which documents
// group operations. The tags add to those of the operation's groups, rather
// than replace them. Tags panics if a tag is empty.
func Tags(tags ...string) OpOption {
	if slices.Contains(tags, "") {
		panic("tyr: Tags: empty tag")
	}
	tags = slices.Clone(tags)
	return func(op *Operation) {
		cur, _ := tagsKey.Get(op)
		tagsKey.Option(appendNew(cur, tags))(op)
	}
}

// Errors declares, for the documentation of an operation, that it may
// return errors of the kinds. The kinds add to those that the operation's
// groups declare: a group whose interceptor requires a role may declare
// [KindUnauthenticated] and [KindPermissionDenied], and each of its
// operations the kinds of its own. Transports add the kinds that any
// operation may return. Errors panics on a kind this package doesn't
// define.
func Errors(kinds ...Kind) OpOption {
	for _, k := range kinds {
		if !k.known() {
			panic(fmt.Sprintf("tyr: Errors: unknown kind %v", k))
		}
	}
	kinds = slices.Clone(kinds)
	return func(op *Operation) {
		cur, _ := errorsKey.Get(op)
		errorsKey.Option(appendNew(cur, kinds))(op)
	}
}

// appendNew returns a copy of s with the elements of add that it doesn't
// have yet, in order.
func appendNew[T comparable](s, add []T) []T {
	out := slices.Clone(s)
	for _, x := range add {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// exampleOption returns the option of Contract.Example: it adds an example
// of a call to the documentation of an operation. It panics if the name is
// empty.
func exampleOption[Req, Res any](name string, req Req, res Res) OpOption {
	if name == "" {
		panic("tyr: Example: empty name")
	}
	ex := ExampleCall{Name: name, Req: req, Res: res}
	return func(op *Operation) {
		cur, _ := examplesKey.Get(op)
		examplesKey.Option(append(slices.Clip(cur), ex))(op)
	}
}

// Doc returns the documentation of the operation, which options set; see
// [Doc]. Its slices are shared by all calls and must not be changed.
func (op *Operation) Doc() Doc {
	var d Doc
	d.Summary, _ = summaryKey.Get(op)
	d.Description, _ = descriptionKey.Get(op)
	d.Tags, _ = tagsKey.Get(op)
	d.Deprecated, _ = deprecatedKey.Get(op)
	d.Errors, _ = errorsKey.Get(op)
	d.Examples, _ = examplesKey.Get(op)
	return d
}

// checkExamples reports what's wrong with the examples of op, if anything,
// as described at Contract.Example. validation is that of Req, if it has
// any. Only Contract.Example adds examples, so they have the types of op.
func checkExamples[Req any](op *Operation, validate func(req any) (Violations, error)) error {
	examples, _ := examplesKey.Get(op)
	names := make(map[string]bool, len(examples))
	for _, ex := range examples {
		if names[ex.Name] {
			return fmt.Errorf("example %q: another example has the name", ex.Name)
		}
		names[ex.Name] = true

		req := ex.Req.(Req)
		if validate != nil {
			vs, err := validate(&req)
			if err != nil {
				return fmt.Errorf("example %q: %w", ex.Name, err)
			}
			if len(vs) > 0 {
				return fmt.Errorf("example %q: the request fails validation: %s: %s", ex.Name, vs[0].Pointer, vs[0].Detail)
			}
		}
		if v, ok := any(&req).(Validator); ok {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("example %q: the request fails validation: %s", ex.Name, failure(err))
			}
		}
		if _, err := json.Marshal(ex.Req); err != nil {
			return fmt.Errorf("example %q: encoding the request: %w", ex.Name, err)
		}
		if _, err := json.Marshal(ex.Res); err != nil {
			return fmt.Errorf("example %q: encoding the result: %w", ex.Name, err)
		}
	}
	return nil
}

// failure describes an error of Validate: by its first violation, if it
// has violations, or else by its text.
func failure(err error) string {
	if e, ok := errors.AsType[*Error](err); ok && e != nil {
		if v, ok := e.Details.(Violations); ok && len(v) > 0 {
			return v[0].Pointer + ": " + v[0].Detail
		}
	}
	return err.Error()
}
