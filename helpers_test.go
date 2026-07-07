package toll

import "math"

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }

func approxEqual(a, b float64) bool {
	if a == 0 || b == 0 {
		return a == b
	}
	diff := math.Abs(a - b)
	return diff/math.Abs(a) <= 1e-9
}

// refBucket is a closed-form scalar token bucket in debt form, mirroring toll's
// optimistic path exactly: linear drain clamped at zero, admit iff spent+cost
// stays within burst. It is the reference for the rate-conformance sentinel and
// the per-key reference for the conservative-direction property.
type refBucket struct {
	spent, rate, burst float64
	last               int64 // UnixMilli
}

func newRef(rate, burst float64) *refBucket { return &refBucket{rate: rate, burst: burst} }

func (r *refBucket) allow(now int64, cost float64) bool {
	if dt := now - r.last; dt > 0 {
		r.spent -= r.rate * float64(dt) / 1000.0
		if r.spent < 0 {
			r.spent = 0
		}
	}
	r.last = now
	if r.spent+cost <= r.burst {
		r.spent += cost
		return true
	}
	return false
}
