package provider

import (
	"net/http"
	"strconv"
	"time"
)

const maxLLMRetries = 3

// isRetryableStatus returns true for HTTP status codes that warrant a retry.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 502 || code == 503 || code == 529
}

// retryDelay calculates the backoff duration for a retry attempt.
// It respects the Retry-After header if present, otherwise uses exponential backoff.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if sec, err := strconv.Atoi(ra); err == nil && sec > 0 && sec <= 60 {
				return time.Duration(sec) * time.Second
			}
		}
	}
	return time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s, 4s
}
