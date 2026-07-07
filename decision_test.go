package toll

import (
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
)

// filledLimiter returns a Rate=10, Burst=10 limiter with the bucket fully spent
// at t=0, its fake clock, and the key. Strict toggles the mode.
func filledLimiter(t *testing.T, strict bool) (*Limiter, *grudgetest.FakeClock, []byte) {
	t.Helper()
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{Rate: 10, Burst: 10, Strict: strict, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("k")
	for i := 0; i < 10; i++ {
		if !l.Allow(key) {
			t.Fatalf("setup admit %d denied", i)
		}
	}
	return l, clock, key
}

// TestRetryAfterExact is the M3 sentinel (spec 7.4). A rejected cost-5 request
// on a full Rate=10/Burst=10 bucket must report RetryAfter=500ms; advancing by
// exactly that admits, one millisecond short does not. Boundary is float-exact
// because 500ms → 0.5s and 10·0.5 = 5.0 exactly.
func TestRetryAfterExact(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "optimistic"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			// Report value.
			l, _, key := filledLimiter(t, strict)
			d := l.AllowDetailed(key, 5)
			l.Close()
			if d.Allowed {
				t.Fatal("cost 5 on full bucket should reject")
			}
			if d.RetryAfter != 500*time.Millisecond {
				t.Fatalf("RetryAfter = %v, want 500ms", d.RetryAfter)
			}
			if d.Limit != 10 || !approxEqual(d.Spent, 10) {
				t.Fatalf("Decision fields off: %+v", d)
			}

			// Exactly RetryAfter later: admits.
			l2, c2, k2 := filledLimiter(t, strict)
			defer l2.Close()
			l2.AllowDetailed(k2, 5)
			c2.Advance(500 * time.Millisecond)
			if !l2.AllowN(k2, 5) {
				t.Fatal("admit at exactly RetryAfter should succeed")
			}

			// One millisecond short: still rejected.
			l3, c3, k3 := filledLimiter(t, strict)
			defer l3.Close()
			l3.AllowDetailed(k3, 5)
			c3.Advance(499 * time.Millisecond)
			if l3.AllowN(k3, 5) {
				t.Fatal("admit one millisecond early should fail")
			}
		})
	}
}

func TestNeverRetry(t *testing.T) {
	l, err := New(Config{Rate: 5, Burst: 10, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := l.AllowDetailed([]byte("k"), 100) // cost > burst
	if d.Allowed {
		t.Fatal("cost > burst must reject")
	}
	if d.RetryAfter != NeverRetry {
		t.Fatalf("RetryAfter = %v, want NeverRetry (%v)", d.RetryAfter, NeverRetry)
	}
}

func TestAllowDetailedAdmit(t *testing.T) {
	l, err := New(Config{Rate: 5, Burst: 10, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := l.AllowDetailed([]byte("k"), 3)
	if !d.Allowed || d.RetryAfter != 0 {
		t.Fatalf("expected admit with RetryAfter 0, got %+v", d)
	}
	if !approxEqual(d.Spent, 0) {
		t.Fatalf("fresh key Spent = %v, want 0", d.Spent)
	}
}

func TestAllowDetailedNoAlloc(t *testing.T) {
	l, err := New(Config{Rate: 100, Burst: 1000, CellsPerLevel: 100_000, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("hot")
	if a := testing.AllocsPerRun(100, func() { _ = l.AllowDetailed(key, 1) }); a != 0 {
		t.Errorf("AllowDetailed: %v allocs/op, want 0", a)
	}
}
