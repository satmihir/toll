package toll

import (
	"encoding/binary"
	"testing"

	"github.com/satmihir/grudge/grudgetest"
)

// Benchmarks separate the admitted path (Query+Update) from the rejected path
// (Query only, when RejectCost=0): a naive single-key loop with a finite burst
// spends >99.9% of iterations on the cheaper rejected path and flatters the
// headline number.

// admittedLimiter never rejects within a benchmark run: Burst is far larger
// than any b.N, so every iteration pays the full Query+Update admitted path.
func admittedLimiter(b *testing.B, strict, trusted bool) *Limiter {
	b.Helper()
	l, err := New(Config{
		Rate: 1e9, Burst: 1e12, Strict: strict, TrustedKeys: trusted,
		CellsPerLevel: 100_000,
		Ticker:        grudgetest.NewFakeTicker(),
	})
	if err != nil {
		b.Fatal(err)
	}
	return l
}

// rejectedLimiter always rejects: burst 1, prefilled, frozen clock (no refill).
func rejectedLimiter(b *testing.B) *Limiter {
	b.Helper()
	l, err := New(Config{
		Rate: 1, Burst: 1,
		CellsPerLevel: 100_000,
		Clock:         grudgetest.NewFakeClock(), Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		b.Fatal(err)
	}
	if !l.Allow([]byte("hot")) {
		b.Fatal("prefill failed")
	}
	return l
}

func BenchmarkAllowAdmitted(b *testing.B) {
	l := admittedLimiter(b, false, false)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !l.Allow(key) {
			b.Fatal("unexpected reject")
		}
	}
}

func BenchmarkAllowAdmittedTrustedKeys(b *testing.B) {
	l := admittedLimiter(b, false, true) // murmur3 instead of SipHash
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !l.Allow(key) {
			b.Fatal("unexpected reject")
		}
	}
}

func BenchmarkAllowAdmittedStrict(b *testing.B) {
	l := admittedLimiter(b, true, false)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !l.Allow(key) {
			b.Fatal("unexpected reject")
		}
	}
}

func BenchmarkAllowRejected(b *testing.B) {
	l := rejectedLimiter(b)
	defer l.Close()
	key := []byte("hot")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if l.Allow(key) {
			b.Fatal("unexpected admit")
		}
	}
}

func BenchmarkAllowDetailedAdmitted(b *testing.B) {
	l := admittedLimiter(b, false, false)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.AllowDetailed(key, 1)
	}
}

// BenchmarkAllowHighCardinality cycles through 64k distinct keys — closer to a
// real per-client workload than one hot key (different cells every call).
func BenchmarkAllowHighCardinality(b *testing.B) {
	l := admittedLimiter(b, false, false)
	defer l.Close()
	const nKeys = 1 << 16
	keys := make([][]byte, nKeys)
	for i := range keys {
		keys[i] = make([]byte, 8)
		binary.LittleEndian.PutUint64(keys[i], uint64(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Allow(keys[i&(nKeys-1)])
	}
}

// BenchmarkAllowParallel spreads goroutines over distinct keys (uncontended
// cells, contended rotator RLock).
func BenchmarkAllowParallel(b *testing.B) {
	l := admittedLimiter(b, false, false)
	defer l.Close()
	const nKeys = 1 << 16
	keys := make([][]byte, nKeys)
	for i := range keys {
		keys[i] = make([]byte, 8)
		binary.LittleEndian.PutUint64(keys[i], uint64(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			l.Allow(keys[i&(nKeys-1)])
			i++
		}
	})
}

// BenchmarkAllowParallelSameKey is the worst case: every goroutine hammers the
// same key's L cells.
func BenchmarkAllowParallelSameKey(b *testing.B) {
	l := admittedLimiter(b, false, false)
	defer l.Close()
	key := []byte("hot")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.Allow(key)
		}
	})
}

func BenchmarkAllowParallelSameKeyStrict(b *testing.B) {
	l := admittedLimiter(b, true, false)
	defer l.Close()
	key := []byte("hot")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.Allow(key)
		}
	})
}
