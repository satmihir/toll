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
