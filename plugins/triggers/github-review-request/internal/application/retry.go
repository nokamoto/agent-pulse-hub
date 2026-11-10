package application

import "time"

const (
	initialBackoff = time.Second
	maxBackoff     = 5 * time.Minute
	minRateDelay   = time.Minute
)

// RetryDelayAt computes the delay from result processing. backoffLevel is
// zero for the first transient failure and is capped without duration overflow.
func RetryDelayAt(interval time.Duration, failure *Failure, backoffLevel uint, now time.Time) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	if failure == nil {
		return interval
	}
	delay := interval
	switch failure.Kind {
	case FailureNetwork, FailureService, FailureRateLimit:
		delay = max(delay, exponentialBackoff(backoffLevel))
		if (failure.Kind == FailureService || failure.Kind == FailureRateLimit) && !failure.RetryAt.IsZero() {
			delay = max(delay, failure.RetryAt.Sub(now))
		} else if failure.Kind == FailureRateLimit {
			delay = max(delay, minRateDelay)
		}
	case FailureAuthentication, FailurePermission, FailureUnsupported:
		if 5*time.Minute > delay {
			delay = 5 * time.Minute
		}
	}
	return delay
}

func exponentialBackoff(level uint) time.Duration {
	delay := initialBackoff
	for step := uint(0); step < level; step++ {
		if delay >= maxBackoff/2 {
			return maxBackoff
		}
		delay *= 2
	}
	return delay
}
