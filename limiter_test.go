package toll

import (
	"fmt"
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
	"pgregory.net/rapid"
)

// TestRateConformance is the primary sentinel (spec 7.1): a single key must
// track a closed-form scalar token bucket decision-for-decision, including
// occasional cost>burst rejections.
func TestRateConformance(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		rate := rapid.Float64Range(1, 1000).Draw(rt, "rate")
		burst := rapid.Float64Range(1, 1000).Draw(rt, "burst")
		clock := grudgetest.NewFakeClock()

		l, err := New(Config{
			Rate: rate, Burst: burst,
			Levels: 4, CellsPerLevel: 4096,
			Clock: clock, Ticker: grudgetest.NewFakeTicker(),
		})
		if err != nil {
			rt.Fatal(err)
		}
		defer l.Close()

		ref := newRef(rate, burst)
		key := []byte("k")

		n := rapid.IntRange(1, 120).Draw(rt, "n")
		for i := 0; i < n; i++ {
			clock.Advance(time.Duration(rapid.Int64Range(0, 3000).Draw(rt, "dt")) * time.Millisecond)
			now := clock.Now().UnixMilli()
			cost := rapid.Float64Range(0.01, burst*1.5).Draw(rt, "cost") // sometimes > burst

			got := l.AllowN(key, cost)
			want := ref.allow(now, cost)
			if got != want {
				rt.Fatalf("op %d: toll=%v ref=%v (cost=%v refSpent=%v)", i, got, want, cost, ref.spent)
			}
		}
	})
}

// TestConservativeDirection is the error-direction sentinel (spec 7.3): with
// forced collisions, toll must admit no more than a per-key exact reference for
// any stable key. Collisions can only make the limiter stricter.
func TestConservativeDirection(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		const rate, burst = 10.0, 5.0
		const nKeys = 40
		clock := grudgetest.NewFakeClock()

		l, err := New(Config{
			Rate: rate, Burst: burst,
			Levels: 3, CellsPerLevel: 16, // tiny M forces collisions
			Clock: clock, Ticker: grudgetest.NewFakeTicker(),
		})
		if err != nil {
			rt.Fatal(err)
		}
		defer l.Close()

		refs := make([]*refBucket, nKeys)
		tollAdmits := make([]int, nKeys)
		refAdmits := make([]int, nKeys)
		for i := range refs {
			refs[i] = newRef(rate, burst)
		}

		n := rapid.IntRange(50, 400).Draw(rt, "n")
		for i := 0; i < n; i++ {
			clock.Advance(time.Duration(rapid.Int64Range(0, 300).Draw(rt, "dt")) * time.Millisecond)
			now := clock.Now().UnixMilli()
			k := rapid.IntRange(0, nKeys-1).Draw(rt, "k")
			key := []byte(fmt.Sprintf("key-%d", k))

			if l.AllowN(key, 1) {
				tollAdmits[k]++
			}
			if refs[k].allow(now, 1) {
				refAdmits[k]++
			}
		}

		for k := 0; k < nKeys; k++ {
			if tollAdmits[k] > refAdmits[k] {
				rt.Fatalf("key %d: toll admitted %d > reference %d (collisions must only stricten)",
					k, tollAdmits[k], refAdmits[k])
			}
		}
	})
}

func TestBurstThenSustained(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{Rate: 5, Burst: 10, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")

	// Full bucket: 10 immediate unit admits, 11th denied.
	for i := 0; i < 10; i++ {
		if !l.Allow(key) {
			t.Fatalf("burst admit %d denied", i)
		}
	}
	if l.Allow(key) {
		t.Fatal("11th admit should be denied (bucket empty)")
	}
	// Refill 5/s: after 200ms exactly one token returns.
	clock.Advance(200 * time.Millisecond)
	if !l.Allow(key) {
		t.Fatal("admit after 200ms refill should succeed")
	}
	if l.Allow(key) {
		t.Fatal("second admit should fail; only one token refilled")
	}
}

func TestCostGreaterThanBurstRejectsNoPanic(t *testing.T) {
	l, err := New(Config{Rate: 5, Burst: 10, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.AllowN([]byte("k"), 100) {
		t.Fatal("cost > burst must be rejected")
	}
	// It must not have debited: a normal request still fits.
	if !l.AllowN([]byte("k"), 10) {
		t.Fatal("bucket should be full after a rejected oversized request")
	}
}

func TestCostPanics(t *testing.T) {
	l, _ := New(Config{Rate: 5, Burst: 10, Ticker: grudgetest.NewFakeTicker()})
	defer l.Close()
	for _, bad := range []float64{0, -1, nan(), inf()} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for cost %v", bad)
				}
			}()
			l.AllowN([]byte("k"), bad)
		}()
	}
}

func TestHotPathAllocations(t *testing.T) {
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
	check("Allow", func() { l.Allow(key) })
	check("AllowN", func() { l.AllowN(key, 1) })
	check("Spent", func() { _ = l.Spent(key) })
}

func BenchmarkAllow(b *testing.B) {
	l, _ := New(Config{Rate: 100, Burst: 1000, CellsPerLevel: 100_000, Ticker: grudgetest.NewFakeTicker()})
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Allow(key)
	}
}
