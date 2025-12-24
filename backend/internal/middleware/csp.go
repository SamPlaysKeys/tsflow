package middleware

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/gin-gonic/gin"
)

const cspNonceKey = "csp_nonce"

// CSPMiddleware generates a random nonce for each request and sets CSP headers.
func CSPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		nonce := generateNonce()
		c.Set(cspNonceKey, nonce)

		// Set CSP header with nonce for script-src
		csp := "default-src 'self'; " +
			"base-uri 'self'; " +
			"object-src 'none'; " +
			"frame-ancestors 'none'; " +
			"form-action 'self'; " +
			"img-src 'self' data: https:; " +
			"font-src 'self' https://fonts.gstatic.com; " +
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
			"script-src 'self' 'nonce-" + nonce + "'"

		c.Header("Content-Security-Policy", csp)
		c.Next()
	}
}

// GetCSPNonce retrieves the CSP nonce from the gin context.
// Returns empty string if nonce is not set.
func GetCSPNonce(c *gin.Context) string {
	if nonce, exists := c.Get(cspNonceKey); exists {
		if s, ok := nonce.(string); ok {
			return s
		}
	}
	return ""
}

// generateNonce creates a cryptographically random base64 nonce.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
