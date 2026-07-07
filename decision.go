package toll

import (
	"math"
	"time"
)

// NeverRetry is the RetryAfter value when a request can never be admitted
// because its cost exceeds Burst — no amount of waiting helps.
const NeverRetry time.Duration = -1

// Decision is the detailed outcome of an admission check. Only Allowed is
// authoritative; Spent and RetryAfter are point-in-time advisory estimates
// (and, in strict mode, come from a follow-up query and may be slightly stale
// under concurrency). They are meant for metrics and Retry-After headers, never
// as admission input.
type Decision struct {
	Allowed    bool
	Spent      float64       // debt estimate at decision time
	Limit      float64       // Burst
	RetryAfter time.Duration // 0 when Allowed; NeverRetry when cost > Burst
}

// AllowDetailed is AllowN with observability: it returns the verdict plus the
// key's debt estimate and, on rejection, how long until the request would fit.
// Under linear refill that wait is closed-form, so RetryAfter is exact enough
// to drive a Retry-After header (a lower bound under contention — callers retry,
// they do not reserve).
func (l *Limiter) AllowDetailed(key []byte, cost float64) Decision {
	checkCost(cost)
	if cost > l.burst {
		spent := l.rot.Query(key)
		l.penalize(key)
		return Decision{Allowed: false, Spent: spent, Limit: l.burst, RetryAfter: NeverRetry}
	}

	if l.strict {
		spent := l.rot.Query(key) // advisory, pre-decision
		if l.rot.TryUpdate(key, cost, l.burst) {
			return Decision{Allowed: true, Spent: spent, Limit: l.burst}
		}
		l.penalize(key)
		return Decision{Allowed: false, Spent: spent, Limit: l.burst, RetryAfter: l.retryAfter(spent, cost)}
	}

	spent := l.rot.Query(key)
	if spent+cost <= l.burst {
		l.rot.Update(key, cost)
		return Decision{Allowed: true, Spent: spent, Limit: l.burst}
	}
	l.penalize(key)
	return Decision{Allowed: false, Spent: spent, Limit: l.burst, RetryAfter: l.retryAfter(spent, cost)}
}

// retryAfter returns the time for enough debt to drain that cost would fit,
// rounded up to the millisecond. Callers reach it only on rejection, after
// penalize has run — so the wait is computed from the post-penalty debt
// min(spent+RejectCost, MaxDebt); otherwise the header would be systematically
// short and a client honoring it would be rejected and re-penalized on return.
func (l *Limiter) retryAfter(spent, cost float64) time.Duration {
	debt := spent
	if l.rejectCost > 0 {
		debt = math.Min(spent+l.rejectCost, l.maxDebt)
	}
	excess := debt + cost - l.burst
	if excess <= 0 {
		return 0
	}
	millis := math.Ceil((excess / l.rate) * 1000)
	return time.Duration(millis) * time.Millisecond
}
