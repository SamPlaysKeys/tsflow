package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	
	errStr := err.Error()
	retryableErrors := []string{"status 429", "status 502", "status 503", "status 504", "timeout", "connection refused"}
	
	for _, retryErr := range retryableErrors {
		if strings.Contains(errStr, retryErr) {
			return true
		}
	}
	return false
}

func FormatTimeForAPI(t time.Time) string {
	return t.Format(time.RFC3339)
}

func HTTPError(status int, body string) error {
	switch status {
	case 401:
		return fmt.Errorf("status 401: bad auth - check your API key")
	case 403:
		return fmt.Errorf("status 403: missing permissions (need logs:network:read)")
	case 404:
		return fmt.Errorf("status 404: tailnet not found")
	case 429:
		return fmt.Errorf("status 429: rate limited - slow down")
	case 502:
		return fmt.Errorf("status 502: bad gateway")
	case 503:
		return fmt.Errorf("status 503: tailscale API down")
	case 504:
		return fmt.Errorf("status 504: timeout - try smaller time range")
	default:
		return fmt.Errorf("status %d: %s", status, body)
	}
}