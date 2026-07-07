# Benchmarks

Single-key, uncontended, `Burst=1000, CellsPerLevel=100000`, Apple Silicon
(darwin/arm64), `go test -bench . -benchmem`. Recorded so regressions show up
in review.

| Operation | ns/op | allocs/op |
|---|---|---|
| Allow | ~114 | 0 |
| AllowN (optimistic) | ~115 | 0 |
| AllowN (strict) | ~110 | 0 |
| AllowDetailed | ~114 | 0 |

Notes:

- Strict is not slower here because the workload is uncontended: strict is a
  single locked pass (grudge `TryUpdate`), while optimistic is `Query` then
  `Update` (two rotator read-lock cycles). Under heavy same-key contention
  strict pays more, since it holds all L cell locks — that is the throughput
  cost you trade for exact admission.
- Zero allocations on the hot path is enforced by tests
  (`testing.AllocsPerRun`), not merely observed here. `AllowDetailed` returns
  a value struct and is also allocation-free.
