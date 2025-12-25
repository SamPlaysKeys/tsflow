package config

import (
	"errors"
	"log"
	"os"
	"strings"
)

// Config holds the application configuration
type Config struct {
	TailscaleAPIKey            string
	TailscaleTailnet           string
	TailscaleAPIURL            string
	TailscaleOAuthClientID     string
	TailscaleOAuthClientSecret string
	TailscaleOAuthScopes       []string
	Port                       string
	Environment                string
}

// Load loads configuration from environment variables
// Supports both TAILSCALE_* and VITE_TAILSCALE_* prefixes for backwards compatibility
func Load() *Config {
	return &Config{
		TailscaleAPIKey:            getEnvWithFallback("TAILSCALE_API_KEY"),
		TailscaleTailnet:           getEnvWithDefault("TAILSCALE_TAILNET", "-"),
		TailscaleAPIURL:            getEnvWithDefault("TAILSCALE_API_URL", "https://api.tailscale.com"),
		TailscaleOAuthClientID:     getEnvWithFallback("TAILSCALE_OAUTH_CLIENT_ID"),
		TailscaleOAuthClientSecret: getEnvWithFallback("TAILSCALE_OAUTH_CLIENT_SECRET"),
		TailscaleOAuthScopes:       parseScopes(getEnvWithFallback("TAILSCALE_OAUTH_SCOPES")),
		Port:                       getEnvWithDefault("PORT", "8080"),
		Environment:                getEnvWithDefault("ENVIRONMENT", "development"),
	}
}

// Validate validates the configuration
func (c *Config) Validate() error {
	hasAPIKey := c.TailscaleAPIKey != ""
	hasOAuth := c.TailscaleOAuthClientID != "" && c.TailscaleOAuthClientSecret != ""

	if !hasAPIKey && !hasOAuth {
		return errors.New("either TAILSCALE_API_KEY or both TAILSCALE_OAUTH_CLIENT_ID and TAILSCALE_OAUTH_CLIENT_SECRET must be provided")
	}

	if hasAPIKey && hasOAuth {
		log.Println("Both API key and OAuth credentials provided. OAuth will take precedence.")
	}

	return nil
}

// getEnvWithDefault returns the environment variable value or a default value
func getEnvWithDefault(key, defaultValue string) string {
	if value := getEnvWithFallback(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvWithFallback checks both non-prefixed and VITE_ prefixed env vars for backwards compatibility
func getEnvWithFallback(key string) string {
	// First check without prefix
	if value := os.Getenv(key); value != "" {
		return value
	}
	// Fall back to VITE_ prefixed version
	if value := os.Getenv("VITE_" + key); value != "" {
		return value
	}
	return ""
}

// parseScopes parses a comma-separated string of OAuth scopes
func parseScopes(scopesStr string) []string {
	if scopesStr == "" {
		return []string{"all:read"}
	}
	scopes := strings.Split(scopesStr, ",")
	for i, scope := range scopes {
		scopes[i] = strings.TrimSpace(scope)
	}
	return scopes
}
