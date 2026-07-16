package toll

import (
	"math"
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"minimal valid", Config{Rate: 5, Burst: 10}, false},
		{"valid MaxDebt > Burst", Config{Rate: 5, Burst: 10, MaxDebt: 20}, false},
		{"valid MaxDebt == Burst", Config{Rate: 5, Burst: 10, MaxDebt: 10}, false},
		{"valid RejectCost", Config{Rate: 5, Burst: 10, RejectCost: 2}, false},
		{"valid Generations 3", Config{Rate: 5, Burst: 10, Generations: 3}, false},
		{"rate zero", Config{Rate: 0, Burst: 10}, true},
		{"rate negative", Config{Rate: -1, Burst: 10}, true},
		{"rate NaN", Config{Rate: math.NaN(), Burst: 10}, true},
		{"rate Inf", Config{Rate: math.Inf(1), Burst: 10}, true},
		{"burst zero", Config{Rate: 5, Burst: 0}, true},
		{"burst negative", Config{Rate: 5, Burst: -1}, true},
		{"burst NaN", Config{Rate: 5, Burst: math.NaN()}, true},
		{"MaxDebt < Burst", Config{Rate: 5, Burst: 10, MaxDebt: 5}, true},
		{"MaxDebt NaN", Config{Rate: 5, Burst: 10, MaxDebt: math.NaN()}, true},
		{"RejectCost negative", Config{Rate: 5, Burst: 10, RejectCost: -1}, true},
		{"RejectCost NaN", Config{Rate: 5, Burst: 10, RejectCost: math.NaN()}, true},
		{"Generations 1", Config{Rate: 5, Burst: 10, Generations: 1}, true},
		{"RotationPeriod negative", Config{Rate: 5, Burst: 10, RotationPeriod: -time.Second}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.Ticker = grudgetest.NewFakeTicker()
			l, err := New(cfg)
			if l != nil {
				l.Close()
			}
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

func TestDefaultPeriod(t *testing.T) {
	// Fast drain -> floored at 5 minutes.
	if got := defaultPeriod(10, 1000, 2); got != defaultPeriodFloor {
		t.Errorf("fast drain period = %v, want floor %v", got, defaultPeriodFloor)
	}
	// Moderate: horizon = 100/2 = 50s, 10x = 500s, between floor and ceil.
	if got := defaultPeriod(100, 2, 2); got != 500*time.Second {
		t.Errorf("moderate period = %v, want 500s", got)
	}
	// Long recovery: horizon 1e6s (~11.6 days) exceeds the 24h cap, so the cap
	// is lifted back to the horizon to preserve the rotation invariant.
	if got := defaultPeriod(1e6, 1, 2); got != time.Duration(1e6*float64(time.Second)) {
		t.Errorf("long-recovery period = %v, want horizon 1e6s", got)
	}
	// More generations shrink the per-generation horizon:
	// maxDebt=600, rate=1, gens=3 -> horizon 300s -> 10x = 3000s.
	if got := defaultPeriod(600, 1, 3); got != 3000*time.Second {
		t.Errorf("gens=3 period = %v, want 3000s", got)
	}
}

func TestRotationInvariantValidation(t *testing.T) {
	// Violating period: horizon = MaxDebt/Rate = 600s, period 60s < 600s.
	_, err := New(Config{Rate: 1, Burst: 10, MaxDebt: 600, RotationPeriod: time.Minute,
		Ticker: grudgetest.NewFakeTicker()})
	if err == nil {
		t.Fatal("expected rotation-invariant error for short period")
	}
	// Compliant period: exactly the horizon.
	l, err := New(Config{Rate: 1, Burst: 10, MaxDebt: 600, RotationPeriod: 600 * time.Second,
		Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatalf("compliant period rejected: %v", err)
	}
	l.Close()
	// More generations divide the requirement: 3 gens, 300s period covers 600s.
	l2, err := New(Config{Rate: 1, Burst: 10, MaxDebt: 600, Generations: 3,
		RotationPeriod: 300 * time.Second, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatalf("3-generation compliant period rejected: %v", err)
	}
	l2.Close()
	// Horizon over a year rejects outright, even with defaults.
	_, err = New(Config{Rate: 1e-9, Burst: 1, Ticker: grudgetest.NewFakeTicker()})
	if err == nil {
		t.Fatal("expected error for over-a-year recovery horizon")
	}
}
