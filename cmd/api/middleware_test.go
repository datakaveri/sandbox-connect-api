package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterConcurrency(t *testing.T) {
	app := &application{
		rateLimiter: NewIPRateLimiter(5, 10),
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := app.rateLimitMiddleware(nextHandler)

	concurrentRequests := 10

	var wg sync.WaitGroup
	wg.Add(concurrentRequests)

	results := make(chan int, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		go func() {
			defer wg.Done()

			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "192.168.1.1:12345"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			results <- rec.Code
		}()
	}

	wg.Wait()
	close(results)

	okCount := 0
	rejectedCount := 0

	for code := range results {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusTooManyRequests:
			rejectedCount++
		}
	}

	if okCount != 5 {
		t.Errorf("Expected 5 successful requests, got %d", okCount)
	}
	if rejectedCount != 5 {
		t.Errorf("Expected 5 rejected requests, got %d", rejectedCount)
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		rate           int
		window         int
		requestCount   int
		expectedStatus int
		waitBetween    time.Duration
	}{
		{
			name:           "Allow requests within limit",
			rate:           2,
			window:         10,
			requestCount:   2,
			expectedStatus: http.StatusOK,
			waitBetween:    time.Millisecond,
		},
		{
			name:           "Block requests over limit",
			rate:           2,
			window:         10,
			requestCount:   3,
			expectedStatus: http.StatusTooManyRequests,
			waitBetween:    time.Millisecond,
		},
		{
			name:           "Reset after window",
			rate:           1,
			window:         1,
			requestCount:   2,
			expectedStatus: http.StatusOK,
			waitBetween:    2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &application{
				rateLimiter: NewIPRateLimiter(tt.rate, tt.window),
			}
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := app.rateLimitMiddleware(nextHandler)

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = "192.168.1.1:12345"

				rec := httptest.NewRecorder()

				handler.ServeHTTP(rec, req)

				if i == tt.requestCount-1 {
					if rec.Code != tt.expectedStatus {
						t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
					}
				}
				time.Sleep(tt.waitBetween)
			}
		})
	}
}

func TestIPRateLimiter_RateLimit_TimeReset(t *testing.T) {
	limiter := NewIPRateLimiter(2, 5)
	ip := "192.168.1.1"

	if !limiter.GetLimiter(ip) {
		t.Error("Expected first request to be allowed")
	}

	if !limiter.GetLimiter(ip) {
		t.Error("Expected second request to be allowed")
	}

	if limiter.GetLimiter(ip) {
		t.Error("Expected third request to be blocked")
	}

	time.Sleep(time.Duration(5) * time.Second)

	if !limiter.GetLimiter(ip) {
		t.Error("Expected request to be allowed after time reset")
	}
}
