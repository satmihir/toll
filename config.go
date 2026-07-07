package toll

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/satmihir/grudge"
)

const (
	defaultLevels        = 4
	defaultCellsPerLevel = 100_000
	defaultGenerations   = 2
	defaultPeriodFloor   = 5 * time.Minute
	defaultPeriodCeil    = 24 * time.Hour
)

// Config parameterizes a Limiter. Only Rate and Burst are required.
type Config struct {
	// Rate is the refill rate in tokens per second; must be > 0 and finite.
	Rate float64
	// Burst is the bucket capacity in tokens; must be > 0 and finite.
	Burst float64

	// Strict uses grudge's atomic conditional-consume (all-L cell locks)
	// instead of the optimistic check-then-debit path, whose race can
	// over-admit by the number of concurrent callers on one key.
	Strict bool

	// RejectCost, if > 0, debits rejected attempts so hammering while limited
	// extends recovery. Most effective with MaxDebt > Burst.
	RejectCost float64
	// MaxDebt caps accumulated debt. 0 means Burst. If set, must be >= Burst.
	// Worst-case recovery time is MaxDebt/Rate.
	MaxDebt float64

	// TrustedKeys opts into the faster murmur3 hasher when keys are NOT
	// attacker-controlled. The default (false) uses SipHash, because a rate
	// limiter's key space is presumptively adversarial.
	TrustedKeys bool

	// Sketch sizing; zero values take documented defaults.
	Levels         uint32
	CellsPerLevel  uint32
	Generations    uint32
	RotationPeriod time.Duration

	// Test injection; nil uses real implementations.
	Clock  grudge.Clock
	Ticker grudge.Ticker
}

func finitePositive(x float64) bool { return x > 0 && !math.IsInf(x, 1) }

func (cfg Config) validate() error {
	if !finitePositive(cfg.Rate) {
		return fmt.Errorf("toll: Rate must be > 0 and finite, got %g", cfg.Rate)
	}
	if !finitePositive(cfg.Burst) {
		return fmt.Errorf("toll: Burst must be > 0 and finite, got %g", cfg.Burst)
	}
	if cfg.MaxDebt != 0 {
		if math.IsNaN(cfg.MaxDebt) || math.IsInf(cfg.MaxDebt, 0) {
			return errors.New("toll: MaxDebt must be finite")
		}
		if cfg.MaxDebt < cfg.Burst {
			return fmt.Errorf("toll: MaxDebt (%g) must be >= Burst (%g)", cfg.MaxDebt, cfg.Burst)
		}
	}
	if math.IsNaN(cfg.RejectCost) || math.IsInf(cfg.RejectCost, 0) || cfg.RejectCost < 0 {
		return fmt.Errorf("toll: RejectCost must be >= 0 and finite, got %g", cfg.RejectCost)
	}
	if cfg.Generations == 1 {
		return errors.New("toll: Generations must be 0 (default 2) or >= 2")
	}
	if cfg.RotationPeriod < 0 {
		return errors.New("toll: RotationPeriod must be >= 0")
	}
	return nil
}

// resolved returns the effective sketch dimensions, cap, generations, and
// rotation period after applying defaults. Assumes cfg has passed validate.
func (cfg Config) resolved() (levels, cells, gens uint32, maxDebt float64, period time.Duration, hasher grudge.HasherFactory) {
	levels = cfg.Levels
	if levels == 0 {
		levels = defaultLevels
	}
	cells = cfg.CellsPerLevel
	if cells == 0 {
		cells = defaultCellsPerLevel
	}
	gens = cfg.Generations
	if gens == 0 {
		gens = defaultGenerations
	}
	maxDebt = cfg.MaxDebt
	if maxDebt == 0 {
		maxDebt = cfg.Burst
	}
	period = cfg.RotationPeriod
	if period == 0 {
		period = defaultPeriod(cfg.Burst, cfg.Rate)
	}
	hasher = grudge.SipHash()
	if cfg.TrustedKeys {
		hasher = grudge.Murmur3()
	}
	return
}

// defaultPeriod picks a rotation period much longer than the bucket drain time
// (so a freshly promoted generation misses only a negligible debt horizon),
// floored at 5 minutes and ceilinged at 24 hours (the latter also guards the
// float->Duration conversion against overflow at a pathologically low Rate).
func defaultPeriod(burst, rate float64) time.Duration {
	seconds := 10 * burst / rate
	if seconds >= defaultPeriodCeil.Seconds() {
		return defaultPeriodCeil
	}
	p := time.Duration(seconds * float64(time.Second))
	if p < defaultPeriodFloor {
		return defaultPeriodFloor
	}
	return p
}
