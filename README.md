# toll

[![CI](https://github.com/satmihir/toll/actions/workflows/ci.yml/badge.svg)](https://github.com/satmihir/toll/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/satmihir/toll.svg)](https://pkg.go.dev/github.com/satmihir/toll)
[![Go Report Card](https://goreportcard.com/badge/github.com/satmihir/toll)](https://goreportcard.com/report/github.com/satmihir/toll)

**A per-key token-bucket rate limiter for unbounded key spaces in constant memory.**

toll rate-limits an unbounded set of keys (client IDs, tenants, IPs, API keys…) in a few megabytes of fixed memory: no per-key map, no eviction, no background work, ~110ns zero-allocation decisions. It's built on [grudge](https://github.com/satmihir/grudge), a decaying-score sketch — toll stores each key's *spent tokens* in the sketch and lets grudge's linear decay refill them.

```go
import "github.com/satmihir/toll"

l, err := toll.New(toll.Config{Rate: 100, Burst: 200}) // 100 tokens/sec, burst of 200
defer l.Close()

if l.Allow(clientID) {
    serve()
} else {
    reject() // 429
}
```

## Why not a map of buckets?

The usual per-key limiter keeps a bucket per key in a hashmap — memory grows with cardinality, and you need LRU eviction, where eviction *is* the failure mode: evicting an active abuser's bucket resets their limit. Redis-backed limiters trade that for a network hop on every request. toll keeps a fixed-size sketch instead: a few MB covers millions of keys, with no eviction and no hop. The cost is that limits are *approximate* — see the error contract below.

## How it works

A key's tokens-spent (debt) lives in the sketch; a fresh, never-seen key reads debt 0 — a full bucket — for free. Each request checks headroom (`spent + cost ≤ Burst`) and, if it fits, debits `cost`. Between requests, grudge's `Linear(Rate)` decay drains the debt at the refill rate. That's a token bucket, in constant memory, over an unbounded key space. (A debt cell under linear decay is exactly GCRA's theoretical arrival time — the algorithm inside most production Redis limiters — run through a sketch instead of a per-key map.)

## The error contract — read this

toll's limits are approximate, and the approximation is **one-sided by construction**:

- **For a stable key, collisions only make the limiter stricter, never more permissive.** A key's debt estimate is its own debt plus any colliding keys' debt, so it can only be over-counted. A heavy key never gets *extra* allowance from collision math, and an innocent key is throttled early only if all of its hash cells collide with hot keys — probability `(1 − (1 − 1/M)^H)^L`, tiny at the defaults and time-bounded by rotation.
- **The only permissive gap is across key identities.** An adversary who rotates keys evades per-key debt — unavoidable for any per-key limiter. But every admitted request debits every level, so each level drains at most `M·Rate` tokens/sec: under key-rotation abuse the limiter degrades into a coarse *aggregate* limiter and **fails closed, not open**. Size `CellsPerLevel · Rate` at or above your intended total capacity so this backstop engages only under abuse.
- **toll is node-local.** Behind N replicas, per-key limits and the aggregate ceiling scale with N unless you shard traffic by key or divide `Rate` per replica. Cross-instance convergence is planned (grudge's update algebra is designed for mergeable replicas) but not in v1.

## Variable cost and Retry-After

Cost is per-request, so toll limits by whatever a request actually consumes — tokens for an LLM call, bytes for bandwidth, 1 for plain QPS. And because refill is linear, the wait until a rejected request would fit is closed-form, so toll can hand you a correct `Retry-After`:

```go
d := l.AllowDetailed(key, cost)
if !d.Allowed {
    if d.RetryAfter == toll.NeverRetry {
        // cost exceeds Burst; no wait ever admits it
    } else {
        w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Seconds())))
    }
}
```

A `cost` larger than `Burst` is legal traffic (rejected, `NeverRetry`); a NaN, infinite, or non-positive `cost` is a programming error and panics.

## Strict vs optimistic

By default admission is optimistic (check then debit) — under same-key concurrency it can over-admit by the number of racing callers, which is noise next to sketch error. When you need hard quotas, set `Strict: true` and admission becomes atomic (grudge's all-levels conditional-consume):

```go
l, _ := toll.New(toll.Config{Rate: 100, Burst: 200, Strict: true})
```

## Adversarial keys

A rate limiter's keys are usually attacker-controlled, so toll defaults to the **SipHash** keyed PRF — an attacker who doesn't know the per-process key can't manufacture the hash collisions that would let them grief a victim's limit. If your keys are trusted (internal IDs), opt into faster murmur3:

```go
l, _ := toll.New(toll.Config{Rate: 100, Burst: 200, TrustedKeys: true})
```

## Multiple limits at once

Compose per-second and per-hour, or per-user and per-IP, with a `MultiLimiter`: it admits only when every member would, and debits none of them when any rejects.

```go
perSec  := must(toll.New(toll.Config{Rate: 100, Burst: 200}))
perHour := must(toll.New(toll.Config{Rate: 10000.0 / 3600, Burst: 10000}))
m := toll.NewMulti(perSec, perHour)
defer m.Close()

if m.Allow(key) { serve() }
```

## Sizing

Defaults are `Levels=4, CellsPerLevel=100000` (~6.4 MB of sketch payload per generation, false-positive ≈ 10⁻⁸ at 1000 concurrently-hot keys). The one rule to remember: **`CellsPerLevel · Rate` bounds the aggregate backstop**, so keep it at or above your intended total admitted rate. `grudge.SuggestLevels` sizes `Levels` for a target false-positive probability.

## Performance

Single-key, uncontended (see [BENCHMARKS.md](BENCHMARKS.md)):

| Operation | ns/op | allocs/op |
|---|---|---|
| Allow / AllowN | ~114 | 0 |
| AllowN (strict) | ~110 | 0 |
| AllowDetailed | ~114 | 0 |

Zero allocations on the hot path is enforced by tests, not just measured.

## Scope

toll is a rate limiter and nothing more: no blocking `Wait`, no HTTP/gRPC middleware in the core (those belong in subpackages), no per-key rate overrides (use one limiter per tier), no cross-instance state sync (waits on grudge serialization). The normative specification is in [spec/SPEC.md](spec/SPEC.md).

## Development

```bash
go test ./... -race        # full suite; runs under goleak
go vet ./...
go test -bench . -benchmem # hot-path benchmarks
```

## License

MIT — see [LICENSE](LICENSE).
