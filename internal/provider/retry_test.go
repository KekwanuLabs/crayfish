package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestIsRetryableStatus(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{200, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{429, true},
		{500, false},
		{502, true},
		{503, true},
		{529, true},
	}

	for _, tt := range tests {
		if got := isRetryableStatus(tt.code); got != tt.want {
			t.Errorf("isRetryableStatus(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestRetryDelayExponential(t *testing.T) {
	// Without Retry-After header, should use exponential backoff.
	if got := retryDelay(0, nil); got != 1*time.Second {
		t.Errorf("attempt 0: got %v, want 1s", got)
	}
	if got := retryDelay(1, nil); got != 2*time.Second {
		t.Errorf("attempt 1: got %v, want 2s", got)
	}
	if got := retryDelay(2, nil); got != 4*time.Second {
		t.Errorf("attempt 2: got %v, want 4s", got)
	}
}

func TestRetryDelayRespectsRetryAfterHeader(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Retry-After": {"5"}},
	}
	if got := retryDelay(0, resp); got != 5*time.Second {
		t.Errorf("got %v, want 5s from Retry-After header", got)
	}
}

func TestRetryDelayIgnoresLargeRetryAfter(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Retry-After": {"120"}},
	}
	// > 60 seconds should fall back to exponential.
	if got := retryDelay(0, resp); got != 1*time.Second {
		t.Errorf("got %v, want 1s (large Retry-After should be ignored)", got)
	}
}

func TestRetryDelayIgnoresInvalidRetryAfter(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Retry-After": {"not-a-number"}},
	}
	if got := retryDelay(1, resp); got != 2*time.Second {
		t.Errorf("got %v, want 2s (invalid Retry-After should be ignored)", got)
	}
}
