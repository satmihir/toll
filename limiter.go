package toll

import (
	"math"

	"github.com/satmihir/grudge"
)

// Limiter is a per-key token-bucket rate limiter. It is safe for concurrent
// use. It holds only the underlying grudge rotator and the resolved scalar
// parameters — all policy is expressed through grudge operations.
type Limiter struct {
	rot *grudge.Rotator

	rate       float64
	burst      float64
	maxDebt    float64
	rejectCost float64
	strict     bool
}

// New builds a Limiter from cfg, applying defaults and starting the underlying
// rotation. Call Close to release it.
func New(cfg Config) (*Limiter, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	levels, cells, gens, maxDebt, period, hasher := cfg.resolved()

	rot, err := grudge.NewRotator(grudge.RotatorConfig{
		Sketch: grudge.Config{
			Levels:        levels,
			CellsPerLevel: cells,
			Decay:         grudge.Linear(cfg.Rate), // debt drains at the refill rate
			Lo:            0,
			Hi:            maxDebt,
			Aggregator:    grudge.Min,
			Hasher:        hasher,
			Clock:         cfg.Clock,
		},
		Generations: gens,
		Period:      period,
		Ticker:      cfg.Ticker,
	})
	if err != nil {
		return nil, err
	}

	return &Limiter{
		rot:        rot,
		rate:       cfg.Rate,
		burst:      cfg.Burst,
		maxDebt:    maxDebt,
		rejectCost: cfg.RejectCost,
		strict:     cfg.Strict,
	}, nil
}

// Allow reports whether a unit-cost request for key is admitted, debiting it if
// so. It is AllowN(key, 1).
func (l *Limiter) Allow(key []byte) bool { return l.AllowN(key, 1) }

// AllowN reports whether a request of the given cost is admitted for key,
// debiting cost if so. cost must be positive and finite (NaN, ±Inf, or cost <=
// 0 panics). A cost greater than Burst can never be admitted and is rejected
// (see AllowDetailed for the NeverRetry signal); it is legal input, not a
// programming error.
func (l *Limiter) AllowN(key []byte, cost float64) bool {
	checkCost(cost)
	if cost > l.burst {
		l.penalize(key)
		return false
	}
	if l.strict {
		if l.rot.TryUpdate(key, cost, l.burst) {
			return true
		}
		l.penalize(key)
		return false
	}
	if l.rot.Query(key)+cost <= l.burst {
		l.rot.Update(key, cost)
		return true
	}
	l.penalize(key)
	return false
}

// Spent returns the current debt estimate for key (observability). It reflects
// decay to the current time.
func (l *Limiter) Spent(key []byte) float64 { return l.rot.Query(key) }

// Close stops the underlying rotation. It is idempotent.
func (l *Limiter) Close() { l.rot.Close() }

// penalize debits the reject cost when configured. Inert (a no-op) otherwise.
func (l *Limiter) penalize(key []byte) {
	if l.rejectCost > 0 {
		l.rot.Update(key, l.rejectCost)
	}
}

func checkCost(cost float64) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 {
		panic("toll: cost must be positive and finite")
	}
}
