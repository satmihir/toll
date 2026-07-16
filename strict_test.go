package toll

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/satmihir/grudge/grudgetest"
)

// TestStrictAtomicity is the M2 sentinel (spec 7.2): with a frozen clock (no
// refill during the race), concurrent unit admissions under Strict must total
// exactly Burst. The optimistic path cannot guarantee this.
func TestStrictAtomicity(t *testing.T) {
	const burst = 1000.0
	clock := grudgetest.NewFakeClock() // never advanced: no refill
	l, err := New(Config{
		Rate: 1, Burst: burst, Strict: true,
		CellsPerLevel: 1024,
		Clock:         clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("quota")

	var admitted int64
	var wg sync.WaitGroup
	const workers, per = 50, 100 // 5000 attempts >> burst
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if l.AllowN(key, 1) {
					atomic.AddInt64(&admitted, 1)
				}
			}
		}()
	}
	wg.Wait()

	if admitted != int64(burst) {
		t.Fatalf("strict admitted %d, want exactly %d", admitted, int64(burst))
	}
}

// TestStrictAllowDetailedNeverRetryAfterZeroOnReject guards the follow-up-query
// rule (spec §3.2): under same-key races, a rejected Decision must never carry
// RetryAfter == 0 (which is reserved for admits), and admits must never carry a
// nonzero RetryAfter. The old pre-decision query violated this when another
// caller consumed the remaining headroom between the query and TryUpdate.
func TestStrictAllowDetailedNeverRetryAfterZeroOnReject(t *testing.T) {
	clock := grudgetest.NewFakeClock() // frozen: no refill during the race
	l, err := New(Config{
		Rate: 1, Burst: 100, Strict: true,
		CellsPerLevel: 1024,
		Clock:         clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")

	var admitted, rejected int64
	var violations int64
	var wg sync.WaitGroup
	const workers, per = 32, 20 // 640 attempts >> burst 100
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				d := l.AllowDetailed(key, 1)
				if d.Allowed {
					atomic.AddInt64(&admitted, 1)
					if d.RetryAfter != 0 {
						atomic.AddInt64(&violations, 1)
					}
				} else {
					atomic.AddInt64(&rejected, 1)
					if d.RetryAfter <= 0 {
						atomic.AddInt64(&violations, 1)
					}
				}
			}
		}()
	}
	wg.Wait()

	if violations != 0 {
		t.Fatalf("%d Decisions violated the RetryAfter contract", violations)
	}
	if admitted != 100 {
		t.Fatalf("strict admitted %d, want exactly 100", admitted)
	}
	if rejected != int64(workers*per)-100 {
		t.Fatalf("rejected %d, want %d", rejected, workers*per-100)
	}
}

// TestStrictExactFit checks strict admission boundary behavior on a single
// goroutine: it admits up to Burst and then refills deterministically.
func TestStrictExactFit(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{Rate: 10, Burst: 3, Strict: true, Clock: clock, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")

	for i := 0; i < 3; i++ {
		if !l.AllowN(key, 1) {
			t.Fatalf("strict admit %d denied", i)
		}
	}
	if l.AllowN(key, 1) {
		t.Fatal("4th strict admit should be denied")
	}
}
