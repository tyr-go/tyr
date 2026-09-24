package playground_test

import "reflect"

// reflectType returns the type T.
func reflectType[T any]() reflect.Type {
	return reflect.TypeFor[T]()
}
