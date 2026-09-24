package ctxkey_test

import (
	"context"
	"fmt"

	"github.com/tyr-go/tyr/ctxkey"
)

// Keys are declared once, as package-level variables.
var tenantKey = ctxkey.New[string]("tenant")

func Example() {
	// Middleware stores the value...
	ctx := tenantKey.Set(context.Background(), "acme")

	// ...and handlers read it back, already typed.
	tenant, ok := tenantKey.Get(ctx)
	fmt.Println(tenant, ok)

	_, ok = tenantKey.Get(context.Background())
	fmt.Println(ok)
	// Output:
	// acme true
	// false
}
