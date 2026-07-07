package toll

import (
	"testing"
	"time"

	"github.com/satmihir/grudge/grudgetest"
)

// TestRetryAfterHonorsRejectCost: RetryAfter must account for the reject
// penalty the limiter itself just applied — otherwise a compliant client that
// waits exactly RetryAfter is rejected again and re-penalized, forever.
//
// Setup: Rate=10, Burst=10, RejectCost=3, MaxDebt=100, bucket full (debt 10).
// A cost-5 request rejects and the penalty raises debt to 13, so the honest
// wait is (13+5-10)/10 = 800ms — not the pre-penalty 500ms.
func TestRetryAfterHonorsRejectCost(t *testing.T) {
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
		if !l.Allow(key) {
			t.Fatalf("setup admit %d denied", i)
		}
	}

	d := l.AllowDetailed(key, 5)
	if d.Allowed {
		t.Fatal("cost 5 on full bucket should reject")
	}
	if d.RetryAfter != 800*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 800ms (must include the just-applied RejectCost)", d.RetryAfter)
	}

	// A client honoring the header must be admitted, not re-penalized.
	clock.Advance(d.RetryAfter)
	if !l.AllowN(key, 5) {
		t.Fatal("client that waited exactly RetryAfter was rejected again")
	}
}

// TestRetryAfterPenaltyClampedAtMaxDebt: the post-penalty debt used for
// RetryAfter is clamped at MaxDebt, matching what the sketch actually stores.
func TestRetryAfterPenaltyClampedAtMaxDebt(t *testing.T) {
	clock := grudgetest.NewFakeClock()
	l, err := New(Config{
		Rate: 10, Burst: 10, RejectCost: 50, MaxDebt: 12,
		Clock: clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := []byte("k")
	for i := 0; i < 10; i++ {
		l.Allow(key)
	}

	// Penalty would push 10+50=60, clamped to MaxDebt=12.
	// Honest wait: (12+5-10)/10 = 700ms.
	d := l.AllowDetailed(key, 5)
	if d.RetryAfter != 700*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 700ms (penalty clamped at MaxDebt)", d.RetryAfter)
	}
	clock.Advance(d.RetryAfter)
	if !l.AllowN(key, 5) {
		t.Fatal("client that waited exactly RetryAfter was rejected")
	}
}
