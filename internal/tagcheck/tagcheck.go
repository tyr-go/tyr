// Package tagcheck gives the documents of the transports the validator of
// an API, which says what members of the requests they describe are
// required.
package tagcheck

import (
	"reflect"

	"github.com/tyr-go/tyr"
)

// Failing returns the function of plan.Schemas.SetFailing for v: the paths
// of the values in a zero value of a struct type that fail its validate
// tags as v checks them.
func Failing(v tyr.TagValidator) func(t reflect.Type) ([][]string, error) {
	return func(t reflect.Type) ([][]string, error) {
		check, err := v.Plan(t)
		if err != nil || check == nil {
			return nil, err
		}
		var paths [][]string
		for _, f := range check(reflect.New(t).Interface()) {
			paths = append(paths, f.Path)
		}
		return paths, nil
	}
}
