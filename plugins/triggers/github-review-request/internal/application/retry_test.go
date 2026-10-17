package application

import (
	"testing"
	"time"
)

func TestRetryDelayAt(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		interval time.Duration
		failure  *Failure
		level    uint
		want     time.Duration
	}{
		{name: "success interval", interval: 30 * time.Second, want: 30 * time.Second},
		{name: "first network backoff", interval: 500 * time.Millisecond, failure: &Failure{Kind: FailureNetwork}, want: time.Second},
		{name: "exponential backoff", interval: time.Second, failure: &Failure{Kind: FailureService}, level: 3, want: 8 * time.Second},
		{name: "backoff cap", interval: time.Second, failure: &Failure{Kind: FailureNetwork}, level: 99, want: maxBackoff},
		{name: "incomplete uses interval", interval: 3 * time.Minute, failure: &Failure{Kind: FailureIncomplete}, want: 3 * time.Minute},
		{name: "rate limit follows latest guidance", interval: 30 * time.Second, failure: &Failure{Kind: FailureRateLimit, RetryAt: now.Add(90 * time.Second)}, want: 90 * time.Second},
		{name: "short rate guidance is honored", interval: time.Second, failure: &Failure{Kind: FailureRateLimit, RetryAt: now.Add(2 * time.Second)}, want: 2 * time.Second},
		{name: "rate limit respects larger interval", interval: 2 * time.Minute, failure: &Failure{Kind: FailureRateLimit, RetryAt: now.Add(90 * time.Second)}, want: 2 * time.Minute},
		{name: "rate limit without guidance", interval: 30 * time.Second, failure: &Failure{Kind: FailureRateLimit}, want: minRateDelay},
		{name: "authentication probe bound", interval: time.Minute, failure: &Failure{Kind: FailureAuthentication}, want: 5 * time.Minute},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := RetryDelayAt(test.interval, test.failure, test.level, now); got != test.want {
				t.Fatalf("RetryDelayAt() = %s, want %s", got, test.want)
			}
		})
	}
}
