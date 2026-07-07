# toll

A per-key token-bucket rate limiter for unbounded key spaces in constant
memory. Thin policy layer over
[grudge](https://github.com/satmihir/grudge) (a decaying-score sketch);
first of the grudge recipe packages.

## The spec is the authority

**`spec/SPEC.md` is normative.** Implement what it says, in its vocabulary
(debt, headroom, optimistic/strict, epoch, backstop). If the spec is
ambiguous or seems wrong, make the smallest reasonable choice and flag it
explicitly in your summary so it can be ratified into the spec. Spec changes
are their own commits, never buried in implementation commits.

Spec §10 lists the design invariants. Two are load-bearing enough to repeat:

- **Only `Decision.Allowed` is authoritative** — every other field is
  advisory observability and may be slightly stale under concurrency.
- **toll adds policy only.** If an implementation need points at new sketch
  behavior (e.g. a `TryUpdateDetailed`), that is a grudge spec amendment,
  not a workaround here. Keep the wrapper thin.

## Build on grudge — don't reimplement it

Dependency: `github.com/satmihir/grudge` v0.1.0 (local clone typically at
`../grudge`). Everything hard already lives there, tested:

| toll needs | grudge provides |
|---|---|
| Debt storage, lazy linear refill | `Sketch` + `Linear(rate)` decay |
| Strict admission | `TryUpdate(key, cost, burst)` — atomicity already sentinel-tested |
| Optimistic admission | `Query` + `Update` |
| Seed rotation, warm generations | `Rotator` (use it; never touch two Sketches manually) |
| Keyed hashing | `SipHash()` factory (toll's default; `Murmur3()` behind `TrustedKeys`) |
| Sizing math | `SuggestLevels` |
| Test clocks/tickers | `grudgetest.FakeClock`, `grudgetest.FakeTicker` |

The mapping is one table in spec §1.1: `Decay: Linear(Rate), Lo: 0,
Hi: MaxDebt, Aggregator: Min`, wrapped in a `Rotator`. If your Limiter
struct holds anything beyond the Rotator and resolved config, question it.

## Semantics that are easy to get backwards

- **Debt, not tokens**: 0 = full allowance. The score is tokens *spent*.
- **Error direction is conservative**: collisions over-count a stable key's
  debt, never under-count. The property test in spec §7.3
  (`admitted_toll(k) ≤ admitted_reference(k)`) encodes this permanently —
  do not weaken it to a tolerance in both directions.
- **`cost > Burst` is legal traffic** (reject + `NeverRetry`), while
  NaN/Inf/≤0 cost is a programmer error (panic). Don't unify these.
- **Rejections don't debit** unless `RejectCost > 0`.

## Workflow

- Implement milestone-by-milestone per spec §8 (M1 core → M2 strict →
  M3 Decision/RetryAfter → M4 penalties/composition → M5 docs). Each
  milestone lands with its spec §7 tests green; don't start M(n+1) with
  M(n) red.
- Write the three sentinels early: rate-conformance (§7.1), strict
  atomicity (§7.2), conservative direction (§7.3).
- Hot path (`Allow`, `AllowN`, `WouldAllowN`, `DebitN`, `Spent`,
  `AllowDetailed`) must be allocation-free; assert with
  `testing.AllocsPerRun`, record baselines in BENCHMARKS.md.
- Run everything under goleak via TestMain — the Limiter owns a Rotator
  goroutine; `Close` must be exercised and idempotent.

## Commands

```bash
go test ./... -race        # all tests; CI runs this
go vet ./...
go test -bench . -benchmem # hot-path benchmarks; 0 allocs/op
```

Module: `github.com/satmihir/toll`, Go ≥ 1.22. Dependencies: grudge, plus
test-only `pgregory.net/rapid` and `go.uber.org/goleak` (same budget as
grudge — justify anything new).
