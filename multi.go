package toll

// MultiLimiter enforces several Limiters at once — e.g. a per-second and a
// per-hour bucket, or per-user and per-IP — admitting only when every member
// would admit. It implements the check-then-debit recipe: consult all limiters,
// and debit all only if all pass, so a request rejected by one limiter does not
// consume tokens in the others. Not atomic under concurrency (same race class
// as a single limiter's optimistic path).
type MultiLimiter struct {
	limiters []*Limiter
}

// NewMulti groups limiters into a MultiLimiter. The composite does not take
// ownership beyond Close fan-out; the caller built each limiter.
func NewMulti(limiters ...*Limiter) *MultiLimiter {
	return &MultiLimiter{limiters: limiters}
}

// Allow is AllowN(key, 1).
func (m *MultiLimiter) Allow(key []byte) bool { return m.AllowN(key, 1) }

// AllowN admits iff every member limiter would admit cost for key, debiting
// cost in all of them on success and none of them on failure.
func (m *MultiLimiter) AllowN(key []byte, cost float64) bool {
	for _, l := range m.limiters {
		if !l.WouldAllowN(key, cost) {
			return false
		}
	}
	for _, l := range m.limiters {
		l.DebitN(key, cost)
	}
	return true
}

// Close closes every member limiter.
func (m *MultiLimiter) Close() {
	for _, l := range m.limiters {
		l.Close()
	}
}
