# toll

[![CI](https://github.com/satmihir/toll/actions/workflows/ci.yml/badge.svg)](https://github.com/satmihir/toll/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/satmihir/toll.svg)](https://pkg.go.dev/github.com/satmihir/toll)
[![Go Report Card](https://goreportcard.com/badge/github.com/satmihir/toll)](https://goreportcard.com/report/github.com/satmihir/toll)

**Token-bucket rate limiting for millions of keys in a few megabytes — no per-key state, no eviction, no network hop.**

toll rate-limits an unbounded set of keys (client IDs, tenants, IPs, API keys…) in fixed memory with ~110ns zero-allocation decisions. It is built on [grudge](https://github.com/satmihir/grudge), a constant-memory decaying-score sketch: toll stores each key's *spent tokens* as sketch debt and lets grudge's linear decay refill them.

```go
import "github.com/satmihir/toll"

l, err := toll.New(toll.Config{Rate: 100, Burst: 200}) // 100 tokens/sec, bursts to 200
defer l.Close()

if l.Allow(clientID) {
    serve()
} else {
    reject() // 429
}
```

## Why not a map of buckets?

The standard per-key limiter keeps one bucket per key in a hashmap. Memory grows with key cardinality, so you add LRU eviction — and eviction *is* the failure mode: evicting an active abuser's bucket hands them a fresh one. Redis-backed limiters fix the memory by adding a network hop to every request. toll keeps a fixed-size sketch instead: a few MB covers any number of keys, nothing is ever evicted, and decisions stay in-process.

The trade is that limits are **approximate** — but approximate in one direction only, which is the part worth reading carefully.

## The error contract

- **For any stable key, collisions only make the limiter stricter — never more permissive.** A key's debt estimate is its own debt plus (possibly) colliding keys' debt, so it can only be over-counted. A heavy key never gets extra allowance from sketch error, and an innocent key is throttled early only if *all* of its hash cells collide with hot keys: probability `(1 − (1 − 1/M)^H)^L`, about 10⁻⁸ at the defaults with a thousand concurrently-hot keys, and time-bounded because the sketch periodically re-hashes (rotation).
- **The only permissive gap is across key identities.** An adversary who rotates keys evades per-key debt — true of any per-key limiter ("what is a key?"). But every admitted request debits every sketch level, and each level drains at most `CellsPerLevel × Rate` tokens/sec, so under key-rotation abuse toll degrades into a coarse *aggregate* limiter. **It fails closed, not open.** Size `CellsPerLevel × Rate` at or above your intended total capacity so this backstop only engages under abuse.
- **toll is node-local.** Behind N replicas, effective limits multiply by ~N unless you shard traffic by key or divide `Rate` per replica. Cross-instance convergence is planned on top of grudge's mergeable update algebra, but is not in v1.

## Variable cost, honest Retry-After

Cost is per-request, so you can limit by whatever a request actually consumes — tokens for an LLM call, bytes for bandwidth, 1 for plain QPS:

```go
if l.AllowN(apiKey, float64(promptTokens)) { ... }
```

Because refill is linear, the wait until a rejected request fits is closed-form, so toll reports an exact `Retry-After` where windowed limiters guess — and it stays honest when reject penalties are configured (the reported wait includes the penalty the limiter just applied):

```go
d := l.AllowDetailed(key, cost)
if !d.Allowed {
    if d.RetryAfter == toll.NeverRetry {
        http.Error(w, "request exceeds capacity", http.StatusRequestEntityTooLarge)
    } else {
        w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Seconds())))
        http.Error(w, "rate limited", http.StatusTooManyRequests)
    }
}
```

A `cost` larger than `Burst` is legal traffic — rejected with `NeverRetry`, since no wait admits it. A NaN, infinite, or non-positive cost is a programming error and panics.

## Optimistic or strict

Default admission is check-then-debit: under same-key concurrency it can over-admit by the number of racing callers, which is noise next to sketch error and costs the least. When you need hard quotas, `Strict: true` makes admission atomic (grudge's conditional-consume holds all the key's cell locks):

```go
l, _ := toll.New(toll.Config{Rate: 100, Burst: 200, Strict: true})
```

## Punishing the hammer

Pure token buckets forgive hammering: rejected requests cost nothing, so clients that retry in a tight loop lose nothing by it. Opt into penalties when that matters:

```go
l, _ := toll.New(toll.Config{
    Rate: 100, Burst: 200,
    RejectCost: 10,   // each rejected attempt adds debt…
    MaxDebt:    1000, // …extending recovery up to MaxDebt/Rate seconds
})
```

With `RejectCost`, hammering while limited pushes recovery further out (bounded by `MaxDebt/Rate`); a client that backs off and honors `Retry-After` is admitted on its first retry.

## Adversarial keys

A public rate limiter's keys are attacker-controlled by definition, so toll defaults to the **SipHash** keyed PRF: without the per-process key, an attacker can't manufacture the hash collisions that would let them grief a victim's limit. If your keys are trusted (internal service names, tenant IDs you issue), opt into faster murmur3:

```go
l, _ := toll.New(toll.Config{Rate: 100, Burst: 200, TrustedKeys: true})
```

## Several limits at once

`MultiLimiter` composes buckets — per-second and per-hour, per-user and per-IP — admitting only when every member would, and debiting none when any rejects:

```go
perSec  := must(toll.New(toll.Config{Rate: 100, Burst: 200}))
perHour := must(toll.New(toll.Config{Rate: 10000.0 / 3600, Burst: 10000}))
m := toll.NewMulti(perSec, perHour)
defer m.Close()

if m.Allow(key) { serve() }
```

Composition uses non-mutating checks, so it is always optimistic (even over `Strict` members) and never applies members' `RejectCost`.

## When toll is the wrong tool

Honesty section. Reach for something else when:

- **You need exact per-key accounting** — billing, hard contractual quotas where over-*throttling* a colliding key is unacceptable. The sketch's conservative error is tiny but nonzero; a map or a database is exact.
- **You need one global limit across a fleet today.** toll is node-local in v1; a Redis/gateway limiter gives you global enforcement at the cost of the hop.
- **Your key cardinality is small and bounded** (dozens of tenants): a plain map of `golang.org/x/time/rate` limiters is simpler and exact — toll's advantage begins where per-key state stops being affordable.
- **You need blocking/reservation semantics** (`Wait(ctx)`): not in v1.

## Sizing

Defaults: `Levels=4, CellsPerLevel=100_000` — roughly 6.4 MB of cell payload per generation (two generations by default), false-positive ≈ 10⁻⁸ with a thousand concurrently-hot keys. The one rule to remember: **`CellsPerLevel × Rate` is the aggregate backstop**, keep it at or above your intended total admitted rate. `grudge.SuggestLevels` sizes `Levels` for a target false-positive probability.

## Performance

Single-key, uncontended, Apple Silicon (see [BENCHMARKS.md](BENCHMARKS.md)):

| Operation | ns/op | allocs/op |
|---|---|---|
| Allow / AllowN | ~114 | 0 |
| AllowN (strict) | ~110 | 0 |
| AllowDetailed | ~114 | 0 |

Zero allocations on the hot path is enforced by tests (`testing.AllocsPerRun`), not just observed on a good day.

## Scope and lineage

toll is a rate limiter and nothing more — no middleware in the core (subpackages later), no per-key rate overrides (run one limiter per tier), no cross-instance sync yet. The normative spec lives in [spec/SPEC.md](spec/SPEC.md). The underlying sketch, [grudge](https://github.com/satmihir/grudge), was extracted from [FAIR](https://github.com/satmihir/fair)'s Stochastic Fair BLUE core; a debt cell under linear decay is GCRA's theoretical arrival time run through a sketch, so toll is, in a precise sense, GCRA for unbounded key spaces.

## Development

```bash
go test ./... -race        # full suite; runs under goleak
go vet ./...
go test -bench . -benchmem # hot-path benchmarks
```

## License

MIT — see [LICENSE](LICENSE).
