package middleware

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// CacheConfig holds cache configuration
type CacheConfig struct {
	MaxAge       time.Duration
	Private      bool
	NoStore      bool
	MustRevalid  bool
}

// DefaultCacheConfig returns config for cacheable but frequently changing data
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		MaxAge:      30 * time.Second,
		Private:     true,
		MustRevalid: true,
	}
}

// NoCacheConfig returns config for data that should not be cached
func NoCacheConfig() CacheConfig {
	return CacheConfig{
		NoStore: true,
	}
}

// ShortCacheConfig returns config for short-lived cache (live data)
func ShortCacheConfig() CacheConfig {
	return CacheConfig{
		MaxAge:      10 * time.Second,
		Private:     true,
		MustRevalid: true,
	}
}

// LongCacheConfig returns config for longer-lived cache (historical data)
func LongCacheConfig() CacheConfig {
	return CacheConfig{
		MaxAge:      5 * time.Minute,
		Private:     true,
		MustRevalid: true,
	}
}

// CacheMiddleware adds cache control headers based on configuration
func CacheMiddleware(config CacheConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Only cache successful GET requests
		if c.Request.Method != "GET" || c.Writer.Status() >= 400 {
			return
		}

		setCacheHeaders(c, config)
	}
}

// setCacheHeaders sets appropriate cache control headers
func setCacheHeaders(c *gin.Context, config CacheConfig) {
	if config.NoStore {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		return
	}

	var cacheControl string
	if config.Private {
		cacheControl = "private"
	} else {
		cacheControl = "public"
	}

	if config.MaxAge > 0 {
		cacheControl += ", max-age=" + strconv.Itoa(int(config.MaxAge.Seconds()))
	}

	if config.MustRevalid {
		cacheControl += ", must-revalidate"
	}

	c.Header("Cache-Control", cacheControl)
}

// ETagMiddleware generates and validates ETags for responses
func ETagMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Only for successful GET requests
		if c.Request.Method != "GET" || c.Writer.Status() >= 400 {
			return
		}

		// Generate ETag from response size and current time bucket (5 second resolution)
		// This ensures cache invalidation every 5 seconds for live data
		timeBucket := time.Now().Unix() / 5
		hash := md5.Sum([]byte(strconv.FormatInt(timeBucket, 10) + c.Request.URL.String()))
		etag := `"` + hex.EncodeToString(hash[:8]) + `"`

		c.Header("ETag", etag)

		// Check If-None-Match
		if c.GetHeader("If-None-Match") == etag {
			c.Status(304)
			return
		}
	}
}

// ConditionalCacheHeaders adds time-based cache headers
// Useful for endpoints that return time-series data
func ConditionalCacheHeaders(isHistorical bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if c.Request.Method != "GET" || c.Writer.Status() >= 400 {
			return
		}

		if isHistorical {
			// Historical data can be cached longer since it doesn't change
			setCacheHeaders(c, LongCacheConfig())
		} else {
			// Live data needs shorter cache
			setCacheHeaders(c, ShortCacheConfig())
		}
	}
}
