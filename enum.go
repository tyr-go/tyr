package tyr

// Enum is implemented by a named type whose values are a fixed list, such
// as a status:
//
//	type Status string
//
//	const (
//		StatusTodo  Status = "todo"
//		StatusDoing Status = "doing"
//		StatusDone  Status = "done"
//	)
//
//	func (Status) EnumValues() []Status { return []Status{StatusTodo, StatusDoing, StatusDone} }
//
// The API finds the method by its name and signature, on T or *T, wherever
// T is in a request or a result: in a field, a nested struct, behind a
// pointer, as an element of a slice or an array, or as a key or a value of
// a map. var _ tyr.Enum[Status] = StatusTodo makes the compiler check the
// method.
//
// [Operation.Call] checks the values of enums. A request with a value that
// isn't one of its type fails with [KindInvalidArgument], with a
// [Violation] at the value: must be one of: todo, doing, done. The check
// comes after the validate tags, a field that fails a tag adds no second
// violation, and before Validate, which may rely on it. A field that holds
// the zero value of its type isn't checked, as it was left out or means
// "not set": a field that the client must send needs validate:"required".
//
// A result with such a value is a bug of the server: the call fails with
// [KindInternal], and the log says where the value is. Every value that
// json/v2 writes is checked, the zero value of a field too, so a field of a
// result that may be unset needs the option omitzero, or a pointer.
//
// The documents describe an enum type once, in both directions, named as
// [SchemaNamer] has it: its values as json/v2 writes them, and their type,
// such as {"type":"string","enum":["todo","doing","done"]}. The values must
// write distinct JSON strings, or distinct JSON numbers. The API panics at
// registration on an enum type without values, with values that repeat or
// write neither strings nor numbers, on a type that isn't comparable, on a
// method EnumValues of another signature, and on an enum of numbers as the
// key of a map, as numbers can't name the members of an object.
type Enum[T comparable] interface {
	EnumValues() []T
}
