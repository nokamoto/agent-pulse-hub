package application

import "time"

const (
	initialBackoff = time.Second
	maxBackoff     = 5 * time.Minute
	minRateDelay   = time.Minute
)

// RetryDelay computes the delay after one completed attempt. backoffLevel is
// zero for the first transient failure and is capped without duration overflow.
func RetryDelay(interval time.Duration, failure *Failure, backoffLevel uint) time.Duration {
	return RetryDelayAt(interval, failure, backoffLevel, time.Now())
}

func RetryDelayAt(interval time.Duration, failure *Failure, backoffLevel uint, now time.Time) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	if failure == nil {
		return interval
	}
	delay := interval
	switch failure.Kind {
	case FailureNetwork, FailureService:
		backoff := exponentialBackoff(backoffLevel)
		if backoff > delay {
			delay = backoff
		}
	case FailureRateLimit:
		guidance := minRateDelay
		if !failure.RetryAt.IsZero() {
			guidance = 0
			until := failure.RetryAt.Sub(now)
			if until > guidance {
				guidance = until
			}
		}
		if guidance > delay {
			delay = guidance
		}
		backoff := exponentialBackoff(backoffLevel)
		if backoff > delay {
			delay = backoff
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
