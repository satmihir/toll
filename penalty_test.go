package toll

import (
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
)

// TestRejectDoesNotDebitByDefault: without RejectCost, rejected attempts leave
// debt untouched (clock frozen so no decay confounds the check).
func TestRejectDoesNotDebitByDefault(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{Rate: 10, Burst: 10, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")
	for i := 0; i < 10; i++ {
		l.Allow(key)
	}
	before := l.Spent(key)
	for i := 0; i < 20; i++ {
		if l.AllowN(key, 5) { // all rejected (bucket full)
			t.Fatal("unexpected admit")
		}
	}
	if after := l.Spent(key); !approxEqual(after, before) {
		t.Fatalf("debt changed on rejection without RejectCost: %v -> %v", before, after)
	}
}

// TestRejectCostExtendsRecovery: with RejectCost and MaxDebt > Burst, hammering
// while limited accumulates debt up to MaxDebt and lengthens recovery to
// MaxDebt/Rate.
func TestRejectCostExtendsRecovery(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{
		Rate: 10, Burst: 10, RejectCost: 3, MaxDebt: 100,
		Clock: clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")

	for i := 0; i < 10; i++ {
		l.Allow(key) // fill bucket to Burst=10
	}
	for i := 0; i < 50; i++ {
		l.AllowN(key, 5) // reject; +3 each, clamped at MaxDebt=100
	}
	if s := l.Spent(key); !approxEqual(s, 100) {
		t.Fatalf("debt after hammering = %v, want clamped 100", s)
	}

	// Recovery is now bounded by MaxDebt/Rate = 10s, far beyond Burst/Rate = 1s.
	clock.Advance(1 * time.Second) // 100 -> 90; still well over burst
	if l.AllowN(key, 5) {
		t.Fatal("still limited 1s after hammering (extended recovery)")
	}
	clock.Advance(9 * time.Second) // reach 10s total: 90 -> 0
	if !l.AllowN(key, 5) {
		t.Fatal("should recover by MaxDebt/Rate = 10s")
	}
}

func TestMultiLimiterBindingConstraintNoCrossDebit(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	fast, err := New(Config{Rate: 100, Burst: 2, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	slow, err := New(Config{Rate: 1, Burst: 10, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMulti(fast, slow)
	defer m.Close()
	key := []byte("k")

	if !m.Allow(key) || !m.Allow(key) {
		t.Fatal("first two admits should pass (fast burst = 2)")
	}
	if m.Allow(key) {
		t.Fatal("third should be rejected by the fast limiter")
	}
	// The rejected third request must NOT have debited the slow limiter.
	if s := slow.Spent(key); !approxEqual(s, 2) {
		t.Fatalf("slow limiter debited on cross-limiter rejection: Spent = %v, want 2", s)
	}
	if s := fast.Spent(key); !approxEqual(s, 2) {
		t.Fatalf("fast limiter Spent = %v, want 2", s)
	}
}

func TestWouldAllowNDoesNotDebit(t *testing.T) {
	l, err := New(Config{Rate: 5, Burst: 10, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")
	for i := 0; i < 20; i++ {
		if !l.WouldAllowN(key, 5) {
			t.Fatal("WouldAllowN should stay true; it must not debit")
		}
	}
	if s := l.Spent(key); !approxEqual(s, 0) {
		t.Fatalf("WouldAllowN debited: Spent = %v, want 0", s)
	}
}

func TestCompositeAllocations(t *testing.T) {
	l, err := New(Config{Rate: 100, Burst: 1000, CellsPerLevel: 100_000, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("hot")
	check := func(name string, fn func()) {
		if a := testing.AllocsPerRun(100, fn); a != 0 {
			t.Errorf("%s: %v allocs/op, want 0", name, a)
		}
	}
	check("WouldAllowN", func() { _ = l.WouldAllowN(key, 1) })
	check("DebitN", func() { l.DebitN(key, 0.0001) })
}
