package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"log"

	"github.com/gin-gonic/gin"
)

const cspNonceKey = "csp_nonce"

// CSPMiddleware generates a random nonce for each request and sets CSP headers.
func CSPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		nonce, err := generateNonce()
		if err != nil {
			log.Printf("WARNING: failed to generate CSP nonce: %v", err)
			// Fall back to CSP without nonce
			csp := "default-src 'self'; " +
				"base-uri 'self'; " +
				"object-src 'none'; " +
				"frame-ancestors 'none'; " +
				"form-action 'self'; " +
				"img-src 'self' data: https:; " +
				"font-src 'self' https://fonts.gstatic.com; " +
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
				"script-src 'self'"
			c.Header("Content-Security-Policy", csp)
			c.Next()
			return
		}

		c.Set(cspNonceKey, nonce)

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
func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
