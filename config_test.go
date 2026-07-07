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
	if got := defaultPeriod(10, 1000); got != defaultPeriodFloor {
		t.Errorf("fast drain period = %v, want floor %v", got, defaultPeriodFloor)
	}
	// Pathologically slow rate -> ceilinged at 24h, no overflow.
	if got := defaultPeriod(1e12, 1e-9); got != defaultPeriodCeil {
		t.Errorf("slow drain period = %v, want ceil %v", got, defaultPeriodCeil)
	}
	// Moderate: 10 * (100/2) = 500s, between floor and ceil.
	if got := defaultPeriod(100, 2); got != 500*time.Second {
		t.Errorf("moderate period = %v, want 500s", got)
	}
}
