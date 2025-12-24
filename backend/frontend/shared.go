package frontend

import "errors"

// ErrFrontendNotIncluded is returned when the frontend is not embedded in the build.
// Use `go build -tags exclude_frontend` to build without the frontend.
var ErrFrontendNotIncluded = errors.New("frontend not included in build")
