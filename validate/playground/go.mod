module github.com/tyr-go/tyr/validate/playground

go 1.27

require (
	github.com/go-playground/validator/v10 v10.30.5
	github.com/tyr-go/tyr v0.8.0
)

require (
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// For development in the repository: users get the tyr that the version of
// this module requires, as a replace applies only to the main module.
replace github.com/tyr-go/tyr => ../..
