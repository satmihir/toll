package main

// mapLimiter is the baseline everyone builds first: one lazy token bucket per
// key in a map. Exact per key — but state grows with every key ever seen, and
// a fresh key always gets a fresh bucket, so key-rotation admits everything.
// (An LRU-capped variant bounds the memory but still fails open: evicting an
// active key's bucket resets its allowance.)
type mapLimiter struct {
	rate, burst float64
	buckets     map[string]*mapBucket
}

type mapBucket struct {
	spent float64
	last  int64 // millis
}

func newMapLimiter(rate, burst float64) *mapLimiter {
	return &mapLimiter{rate: rate, burst: burst, buckets: make(map[string]*mapBucket)}
}

func (m *mapLimiter) allow(key string, nowMillis int64, cost float64) bool {
	b, ok := m.buckets[key]
	if !ok {
		b = &mapBucket{last: nowMillis}
		m.buckets[key] = b
	}
	if dt := nowMillis - b.last; dt > 0 {
		b.spent -= m.rate * float64(dt) / 1000.0
		if b.spent < 0 {
			b.spent = 0
		}
	}
	b.last = nowMillis
	if b.spent+cost <= m.burst {
		b.spent += cost
		return true
	}
	return false
}

// stateBytes is an analytic estimate of per-entry footprint: map bucket slot +
// string key header and bytes + *mapBucket + struct. Labeled "state size (est.)"
// in the visual; the point is the slope, not the constant.
func (m *mapLimiter) stateBytes() int64 {
	const perEntry = 96
	return int64(len(m.buckets)) * perEntry
}
