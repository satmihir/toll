package toll

import (
	"testing"

	"github.com/satmihir/grudge/grudgetest"
)

func benchLimiter(b *testing.B, strict bool) *Limiter {
	b.Helper()
	l, err := New(Config{Rate: 100, Burst: 1000, Strict: strict, CellsPerLevel: 100_000, Ticker: grudgetest.NewFakeTicker()})
	if err != nil {
		b.Fatal(err)
	}
	return l
}

func BenchmarkAllowNOptimistic(b *testing.B) {
	l := benchLimiter(b, false)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.AllowN(key, 1)
	}
}

func BenchmarkAllowNStrict(b *testing.B) {
	l := benchLimiter(b, true)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.AllowN(key, 1)
	}
}

func BenchmarkAllowDetailed(b *testing.B) {
	l := benchLimiter(b, false)
	defer l.Close()
	key := []byte("client-42")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.AllowDetailed(key, 1)
	}
}
