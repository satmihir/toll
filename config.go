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
		period = defaultPeriod(maxDebt, cfg.Rate, gens)
	}
	hasher = grudge.SipHash()
	if cfg.TrustedKeys {
		hasher = grudge.Murmur3()
	}
	return
}

// maxRecoveryHorizon bounds MaxDebt/Rate: beyond a year the config is
// unusable (and derived periods would stop meaning anything).
const maxRecoveryHorizon = 365 * 24 * time.Hour

// validateResolved checks constraints that need the post-default values.
func (cfg Config) validateResolved(gens uint32, maxDebt float64, period time.Duration) error {
	if maxDebt/cfg.Rate > maxRecoveryHorizon.Seconds() {
		return fmt.Errorf("toll: MaxDebt/Rate recovery horizon is %.3gs (over a year); raise Rate or lower MaxDebt", maxDebt/cfg.Rate)
	}
	return checkRotationInvariant(period, maxDebt, cfg.Rate, gens)
}

// rotationHorizonSeconds is the recovery window each pre-primary generation
// must cover so that debt cannot vanish at promotion: MaxDebt/(Rate·(gens−1)).
func rotationHorizonSeconds(maxDebt, rate float64, gens uint32) float64 {
	return maxDebt / rate / float64(gens-1)
}

// checkRotationInvariant enforces the conservative contract under rotation:
// grudge's rotator only dual-writes to generations that exist, so a fresh
// generation knows nothing written before its creation. Debt older than
// (Generations−1)×RotationPeriod vanishes at promotion; if that window is
// shorter than the worst-case recovery time MaxDebt/Rate, a maxed-out stable
// key gets fresh allowance at rotation — the one thing this limiter promises
// never happens. See spec §4.
func checkRotationInvariant(period time.Duration, maxDebt, rate float64, gens uint32) error {
	if period.Seconds() < rotationHorizonSeconds(maxDebt, rate, gens) {
		return fmt.Errorf(
			"toll: rotation invariant violated: (Generations-1)*RotationPeriod = %v must be >= MaxDebt/Rate = %vs, "+
				"or debt vanishes at generation promotion and a maxed-out key regains allowance; "+
				"lengthen RotationPeriod, add Generations, or shrink MaxDebt",
			time.Duration(float64(gens-1)*period.Seconds())*time.Second, maxDebt/rate)
	}
	return nil
}

// defaultPeriod picks a rotation period with 10x margin over the per-generation
// recovery horizon, floored at 5 minutes and capped at 24 hours — except that
// the cap is lifted back to the horizon when honoring it would break the
// rotation invariant (a long-recovery config gets a long period, not a broken
// contract).
func defaultPeriod(maxDebt, rate float64, gens uint32) time.Duration {
	horizon := rotationHorizonSeconds(maxDebt, rate, gens)
	p := time.Duration(10 * horizon * float64(time.Second))
	if p < defaultPeriodFloor {
		p = defaultPeriodFloor
	}
	if p > defaultPeriodCeil {
		p = defaultPeriodCeil
		if p.Seconds() < horizon {
			p = time.Duration(horizon * float64(time.Second))
		}
	}
	return p
}
