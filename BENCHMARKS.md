# Benchmarks

Apple M1 Pro (darwin/arm64), `CellsPerLevel=100_000`, `go test -bench . -benchmem`.
Recorded so regressions show up in review.

The suite deliberately separates the **admitted** path (Query + Update, the
number that matters for capacity planning) from the **rejected** path (Query
only when `RejectCost=0`) — a single-key loop with a finite burst spends
>99.9% of iterations on the cheaper rejected path and flatters the headline.
An earlier version of this file made exactly that mistake; the honest numbers
are below.

| Benchmark | ns/op | allocs/op |
|---|---|---|
| Allow, admitted (SipHash default) | ~322 | 0 |
| Allow, admitted (`TrustedKeys` / murmur3) | ~309 | 0 |
| Allow, admitted, strict | ~206 | 0 |
| Allow, rejected | ~69 | 0 |
| AllowDetailed, admitted | ~312 | 0 |
| Allow, admitted, 64k distinct keys | ~686 | 0 |
| Allow, parallel, distinct keys | ~291 | 0 |
| Allow, parallel, same key (optimistic) | ~748 | 0 |
| Allow, parallel, same key (strict) | ~273 | 0 |

Notes:

- **Headline: ~300ns per admitted decision, zero allocations** (enforced by
  `testing.AllocsPerRun` assertions, not just observed).
- **SipHash costs ~4% over murmur3** on this path — the safe default is nearly
  free, since hashing is small next to cell traffic.
- **Strict is *cheaper* than optimistic for admitted traffic** (~206 vs
  ~322ns single-threaded, ~273 vs ~748ns under same-key contention): strict is
  one all-locks pass over the key's L cells, optimistic is two sequential
  lock-per-cell passes (Query then Update). Optimistic's advantage is the
  rejected path (69ns Query-only) and finer-grained locking when levels are
  contended by *different* keys sharing cells. If your workload is mostly
  admitted traffic on hot keys, strict is both exact and faster.
- **High-cardinality (~686ns) is the realistic per-client number**: distinct
  keys touch different cells, so the sketch's 100k-cell arrays stop fitting in
  cache. That is the honest cost of "millions of keys in fixed memory."

## Memory

Measured (`runtime.ReadMemStats` around `New`, default config `Levels=4,
CellsPerLevel=100_000, Generations=2`): **19.2 MB**. That is 800,000 cells ×
24 bytes each (`float64` score + `int64` timestamp + `sync.Mutex`) plus slice
overhead — the cell *payload* alone is 12.8 MB, and docs that quote payload
only are undercounting. The number is **fixed** regardless of key count, and
scales linearly with `Levels × CellsPerLevel × Generations` if you tune it
down (e.g. `CellsPerLevel=10_000` → ~2 MB).
