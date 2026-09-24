package playgroundtest

import (
	"reflect"
	"slices"
	"testing"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/internal/plan"
	"github.com/tyr-go/tyr/internal/plan/plantest"
	"github.com/tyr-go/tyr/validate/playground"
)

func TestChecks(t *testing.T) {
	// On the corpus of the core, go-playground fails the same rules at the
	// same paths as the core, and so gets from tyr the violations that the
	// corpus expects of the core: the same pointers and details.
	adapter, core := playground.New(), tyr.New().Validator()
	for _, c := range plantest.Checks() {
		t.Run(c.Name, func(t *testing.T) {
			typ := reflect.TypeOf(c.Value)
			req := reflect.New(typ)
			req.Elem().Set(reflect.ValueOf(c.Value))

			got, want := failed(t, adapter, typ, req.Interface()), failed(t, core, typ, req.Interface())
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("go-playground fails\n%+v\nwhere the core fails\n%+v", got, want)
			}
			var vs []plantest.Violation
			for _, f := range got {
				p, vt, err := plan.Pointer(typ, f.Path)
				if err != nil {
					t.Fatalf("Pointer(%q) error = %v", f.Path, err)
				}
				d, ok := plan.Detail(f.Rule, f.Param, vt)
				if !ok {
					t.Fatalf("Detail(%q, %q, %v) is unknown", f.Rule, f.Param, vt)
				}
				vs = append(vs, plantest.Violation{Pointer: p, Detail: d})
			}
			if !slices.Equal(vs, c.Want) {
				t.Errorf("violations =\n%q\nwant\n%q", vs, c.Want)
			}
		})
	}
}

// failed returns the rules that req, a pointer to a value of typ, fails by
// v, nil if none.
func failed(t *testing.T, v tyr.TagValidator, typ reflect.Type, req any) []tyr.FailedRule {
	t.Helper()
	check, err := v.Plan(typ)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if check == nil {
		return nil
	}
	if out := check(req); len(out) > 0 {
		return out
	}
	return nil
}
