package toll

import (
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
)

// TestRotationStaysConservative locks the rotation invariant's purpose: across
// generation promotions, a key's debt estimate never falls below the scalar
// reference model — rotation must not grant a maxed-out key fresh allowance.
// Config: Rate=1, Burst=MaxDebt=10 (recovery horizon 10s), Period=15s
// (invariant-compliant). The fake ticker fires exactly at period boundaries
// with the clock advanced to match, including writes placed just before a
// rotation (the worst case for generation warmth).
func TestRotationStaysConservative(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	ticker := grudgetest.NewFakeTicker()
	l, err := New(Config{
		Rate: 1, Burst: 10,
		RotationPeriod: 15 * time.Second,
		Clock:          clock, Ticker: ticker,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")

	// Scalar reference in debt form.
	refDebt, refAt := 0.0, int64(0)
	refNow := func() float64 {
		d := refDebt - float64(clock.Now().UnixMilli()-refAt)/1000.0 // Rate=1
		if d < 0 {
			d = 0
		}
		return d
	}
	refFill := func(n float64) { refDebt = refNow() + n; refAt = clock.Now().UnixMilli() }

	assertConservative := func(label string) {
		t.Helper()
		got, want := l.Spent(key), refNow()
		if got < want-1e-9 {
			t.Fatalf("%s: Spent = %v fell below reference %v — rotation granted amnesty", label, got, want)
		}
		if got > want+1e-9 {
			t.Fatalf("%s: Spent = %v above reference %v — unexpected over-count (single key, no collisions)", label, got, want)
		}
	}

	// Fill the bucket at t=0.
	for i := 0; i < 10; i++ {
		if !l.Allow(key) {
			t.Fatalf("fill admit %d denied", i)
		}
	}
	refFill(10)
	if l.Allow(key) {
		t.Fatal("bucket should be empty")
	}
	assertConservative("t=0 full")

	// Mid-period checkpoint.
	clock.Advance(5 * time.Second)
	assertConservative("t=5 mid-drain")

	// Top up just before the first rotation — the awkward write.
	clock.Advance(9 * time.Second) // t=14
	for i := 0; i < 9; i++ {       // ref debt ~1 here; refill most of the bucket
		if !l.Allow(key) {
			t.Fatalf("t=14 refill admit %d denied", i)
		}
	}
	refFill(9)
	clock.Advance(1 * time.Second) // t=15: rotation boundary
	ticker.Tick()
	waitForRotation(t, l, 1)
	assertConservative("t=15 post-rotation (write 1s before promotion)")

	// The promoted generation must still be enforcing: near-full debt remains.
	if l.AllowN(key, 5) {
		t.Fatal("rotation granted allowance it should not have")
	}

	// Second full cycle: drain out, rotate again, confirm exact agreement.
	clock.Advance(15 * time.Second) // t=30; ref debt 0
	ticker.Tick()
	waitForRotation(t, l, 2)
	assertConservative("t=30 post-second-rotation drained")
	if !l.AllowN(key, 10) {
		t.Fatal("fully drained bucket should admit a full burst")
	}
}

// waitForRotation blocks until the limiter's rotator has performed n rotations
// (the fake ticker hands off to the rotation goroutine asynchronously).
func waitForRotation(t *testing.T, l *Limiter, n int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.rot.Rotations() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("rotation %d did not happen within timeout", n)
}
