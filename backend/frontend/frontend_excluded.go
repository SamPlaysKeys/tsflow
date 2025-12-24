//go:build exclude_frontend

package frontend

import "github.com/gin-gonic/gin"

// RegisterFrontend returns an error when frontend is not embedded.
// Build with `go build` (without -tags exclude_frontend) to include the frontend.
func RegisterFrontend(router *gin.Engine) error {
	return ErrFrontendNotIncluded
}
