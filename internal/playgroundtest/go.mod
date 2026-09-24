// A module of its own, so that the root module has no dependencies: it only
// holds tests and is never published.
module github.com/tyr-go/tyr/internal/playgroundtest

go 1.27

require (
	github.com/tyr-go/tyr v0.8.0
	github.com/tyr-go/tyr/validate/playground v0.0.0-00010101000000-000000000000
)

require (
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.5 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace (
	github.com/tyr-go/tyr => ../..
	github.com/tyr-go/tyr/validate/playground => ../../validate/playground
)
