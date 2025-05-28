package main

import (
	"testing"
	"time"
)

func TestNewIPRateLimiter(t *testing.T) {
	r := 60

	limiter := NewIPRateLimiter(r)

	if limiter == nil {
		t.Error("Expected non-nil limiter")
	}

	if limiter.ratePerMinute != r {
		t.Errorf("Expected rate %v, got %v", r, limiter.ratePerMinute)
	}

	if limiter.ips == nil {
		t.Error("Expected non-nil ips map")
	}
}

func TestIPRateLimiter_GetLimiter(t *testing.T) {
	limiter := NewIPRateLimiter(2) // 2 requests per minute

	ip := "192.168.1.1"

	// First request should be allowed
	if !limiter.GetLimiter(ip) {
		t.Error("Expected first request to be allowed")
	}

	// Second request should be allowed
	if !limiter.GetLimiter(ip) {
		t.Error("Expected second request to be allowed")
	}

	// Third request should be blocked
	if limiter.GetLimiter(ip) {
		t.Error("Expected third request to be blocked")
	}
}

func TestIPRateLimiter_RateLimit_TimeReset(t *testing.T) {
	limiter := NewIPRateLimiter(2) // 2 requests per minute
	ip := "192.168.1.1"

	// First request should be allowed
	if !limiter.GetLimiter(ip) {
		t.Error("Expected first request to be allowed")
	}

	// Second request should be allowed
	if !limiter.GetLimiter(ip) {
		t.Error("Expected second request to be allowed")
	}

	// Third request should be blocked
	if limiter.GetLimiter(ip) {
		t.Error("Expected third request to be blocked")
	}

	// Wait for more than a minute
	time.Sleep(time.Minute + time.Second)

	// After a minute, first request should be allowed again
	if !limiter.GetLimiter(ip) {
		t.Error("Expected request to be allowed after time reset")
	}
}
