// A module of its own, so that the root module has no dependencies: it only
// holds tests and is never published.
module github.com/tyr-go/tyr/internal/schematest

go 1.27

replace github.com/tyr-go/tyr => ../..

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/tyr-go/tyr v0.0.0-00010101000000-000000000000
)

require golang.org/x/text v0.14.0 // indirect
