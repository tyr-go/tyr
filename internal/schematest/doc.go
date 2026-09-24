// Package schematest checks the JSON Schemas that tyr makes with a JSON
// Schema validator, santhosh-tekuri/jsonschema: the schemas of types
// against the JSON that encoding/json/v2 writes and reads, as JSON Schema
// Draft 7 and 2020-12 alike. It is a module of its own, so that the root
// module has no dependencies, holds only tests and is never published. Run
// them from its directory:
//
//	cd internal/schematest && go test ./...
package schematest
