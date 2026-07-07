# toll v1 Specification

**Status:** Approved for implementation
**Module:** `github.com/satmihir/toll`
**Depends on:** `github.com/satmihir/grudge` v0.1.0 (the only non-test dependency)
**Origin:** distilled from `satmihir/fair` `ideas/ratelimiter.md`

toll is a per-key token-bucket rate limiter for **unbounded, potentially
attacker-controlled key spaces in constant memory**: no per-key state, no
eviction, no background work, O(L) per decision. It is the first policy
package built on grudge and should read as a thin, opinionated wrapper — if a
feature needs new sketch behavior, amend grudge's spec first.

The words MUST / SHOULD / MAY are normative.

---

## 1. Core model (normative)

### 1.1 Debt inversion

A sketch cannot initialize per-key state, so 0 must mean "full allowance."
toll therefore tracks **tokens spent (debt)**, not tokens remaining. A bucket
`(capacity Burst, refill Rate/sec)` maps onto grudge as:

| grudge Config field | toll value |
|---|---|
| `Decay` | `Linear(Rate)` — debt drains at the refill rate |
| `Lo`, `Hi` | `0`, `MaxDebt` (default `Burst`) |
| `Aggregator` | `Min` (fixed; not exposed) |
| `Hasher` | `SipHash()` unless `TrustedKeys` (see §5) |
| wrapper | `Rotator{Generations, Period}` (§4 defaults) |

A fresh, never-seen key reads debt 0 = a full bucket, by construction.

**GCRA equivalence** (documentation claim, with qualifier): for an
uncontaminated key-cell pair, `(spent, lastUpdated)` under linear decay is
isomorphic to GCRA's theoretical arrival time. With collisions, a cell is a
*shared* GCRA debt register for all keys hashing there, and min-aggregation
selects the least-contaminated view.

### 1.2 Admission

```
headroom check:  spent + cost ≤ Burst   → admit and debit cost
otherwise        reject; debit RejectCost if configured (§3.3)
```

Rejected requests do not consume tokens (token-bucket semantics); RejectCost
is the explicit opt-in exception.

### 1.3 Error contract (conservative per key)

Every debit is positive, so a cell holds a key's own debt *plus* colliding
debt: `estimate = trueDebt + min over L cells(collision debt) ≥ trueDebt`.
Therefore, and this MUST be stated in the README:

- Collisions only make the limiter **stricter** for a stable key — exact
  whenever at least one of its L cells is clean, never more permissive. A
  known-heavy key never gets extra allowance from collision math.
- False-early-throttling probability for an innocent key is
  `(1 − (1 − 1/M)^H)^L` for H concurrently-heavy keys; rotation bounds any
  false positive's duration to `Generations × Period`.
- The **permissive** failure mode exists only across key *identities* (an
  adversary rotating keys evades per-key debt). Sustained rotation abuse
  saturates cells broadly and the limiter degrades into a coarse **aggregate**
  limiter with ceiling ~`M·Rate` tokens/sec per level — it fails closed, not
  open. Deployments MUST size `CellsPerLevel · Rate` at or above intended
  total capacity so this backstop engages only under abuse.

---

## 2. Public API (normative signatures)

```go
package toll

type Config struct {
    Rate  float64 // r: tokens per second; > 0, finite
    Burst float64 // B: bucket capacity in tokens; > 0, finite

    // Strict uses grudge.TryUpdate (atomic conditional-consume; holds all L
    // cell locks) instead of the optimistic Query+Update path, whose race can
    // over-admit by at most the number of concurrent callers on one key.
    Strict bool

    // RejectCost, if > 0, debits rejected attempts so hammering-while-limited
    // extends recovery. Most effective with MaxDebt > Burst (see §3.3).
    RejectCost float64

    // MaxDebt caps accumulated debt (grudge Hi). 0 → Burst. If set, MUST be
    // ≥ Burst. Worst-case recovery time = MaxDebt/Rate.
    MaxDebt float64

    // TrustedKeys opts into murmur3 (~2× faster hashing) when keys are NOT
    // attacker-controlled. Default false → SipHash: a public rate limiter is
    // the adversarial case, so the safe hasher is the default (§5).
    TrustedKeys bool

    // Sketch sizing; zero values take the defaults in §4.
    Levels        uint32
    CellsPerLevel uint32
    Generations   uint32
    RotationPeriod time.Duration

    // Test injection; nil → real implementations.
    Clock  grudge.Clock
    Ticker grudge.Ticker
}

type Limiter struct{ /* wraps a grudge.Rotator */ }

func New(cfg Config) (*Limiter, error)

// Allow is AllowN(key, 1).
func (l *Limiter) Allow(key []byte) bool

// AllowN admits iff the key has cost tokens of headroom, debiting cost if so.
func (l *Limiter) AllowN(key []byte, cost float64) bool

// Decision carries observability alongside the verdict. Only Allowed is
// authoritative; Spent and RetryAfter are advisory point-in-time estimates.
type Decision struct {
    Allowed    bool
    Spent      float64       // debt estimate at decision time (pre-debit)
    Limit      float64       // Burst
    RetryAfter time.Duration // see §3.2; 0 when Allowed; NeverRetry when cost can never fit
}

// NeverRetry signals cost > Burst: no amount of waiting admits this request.
const NeverRetry time.Duration = -1

func (l *Limiter) AllowDetailed(key []byte, cost float64) Decision

// Non-mutating check and unconditional debit, for composing limiters
// (multi-window, multi-dimension) without refund bookkeeping: WouldAllowN on
// every limiter, then DebitN on all iff every check passed (§6).
func (l *Limiter) WouldAllowN(key []byte, cost float64) bool
func (l *Limiter) DebitN(key []byte, cost float64)

// Spent returns the current debt estimate for a key (observability).
func (l *Limiter) Spent(key []byte) float64

// Close stops the underlying rotator. Idempotent.
func (l *Limiter) Close()
```

---

## 3. Operation semantics

### 3.1 AllowN

- **Optimistic (default)**: `spent := Query(key)`; if `spent + cost ≤ Burst`
  then `Update(key, +cost)` and admit, else reject. The check and debit are
  not atomic: concurrent callers on the same key can over-admit by at most
  the concurrency width. This MUST be documented, not hidden.
- **Strict**: `Rotator.TryUpdate(key, +cost, Burst)`. Exact within a
  generation epoch: grudge's TryUpdate sentinel guarantees N racing callers
  admit exactly the limit. Across a rotation boundary the promoted generation
  carries dual-written debt but is not bit-identical to the retired primary,
  so strictness is per-epoch, not global — document this.
- `cost` MUST be positive and finite; NaN, ±Inf, zero, or negative cost
  panics (programming error, same discipline as grudge deltas).
- `cost > Burst` is **legal input** (data-driven costs — e.g. LLM tokens —
  can exceed capacity): AllowN returns false without debiting (RejectCost
  still applies), AllowDetailed reports `RetryAfter = NeverRetry`. It MUST
  NOT panic: unlike a NaN, an oversized request is the traffic's fault, not
  the programmer's.

### 3.2 AllowDetailed and RetryAfter

Under linear decay, recovery time is closed-form — this is toll's
production hook (correct `Retry-After` headers, which windowed limiters can
only approximate):

```
RetryAfter = 0                                if admitted
           = NeverRetry                       if cost > Burst
           = (spent + cost − Burst) / Rate    otherwise (round up to ~ms)
```

In optimistic mode `spent` comes from the Query already performed. In strict
mode TryUpdate returns only a verdict, so `Spent` and `RetryAfter` come from
a follow-up `Query` — advisory and slightly racy, which is acceptable because
Decision fields are observability, never admission input. (If this proves
unsatisfying, the fix is a `TryUpdateDetailed` in grudge — amend that spec,
don't work around it here.)

RetryAfter is a lower bound in the presence of contention (other traffic may
add debt while the caller waits); callers retry, they don't reserve.

### 3.3 RejectCost and MaxDebt

- With `RejectCost > 0`, every rejection performs `Update(key, +RejectCost)`.
  In strict mode this debit is a separate operation after the failed
  TryUpdate (not atomic with the verdict; acceptable — it's a penalty, not
  accounting).
- With `MaxDebt = Burst` (default), reject penalties saturate at full-bucket
  debt: even a hammering key recovers in `Burst/Rate` seconds.
- With `MaxDebt > Burst`, penalties push debt past capacity ("leaky debt"):
  recovery extends up to `MaxDebt/Rate` seconds. Under the pure flow debt
  never exceeds Burst on its own — only RejectCost, DebitN, or optimistic
  races reach that range. The Config docs MUST say this so nobody wonders how
  debt exceeds Burst.

### 3.4 Hot path

`Allow`, `AllowN`, `WouldAllowN`, `DebitN`, and `Spent` MUST be
allocation-free (grudge's hot path already is; toll must not add any).
`AllowDetailed` returns a value struct — also allocation-free.

---

## 4. Defaults and validation

Zero-value Config fields resolve to:

| Field | Default | Rationale |
|---|---|---|
| `Levels` | 4 | with M=100k: false-positive ≈ 10⁻⁸ at H=1000 |
| `CellsPerLevel` | 100_000 | ~6.4 MB payload/generation; backstop `M·Rate` |
| `Generations` | 2 | FAIR-proven |
| `RotationPeriod` | `max(5min, 10 × Burst/Rate)` | Period ≫ drain time, so the debt horizon a fresh generation misses is noise |
| `MaxDebt` | `Burst` | standard token bucket |

`New` MUST reject (error, not panic): `Rate ≤ 0` or non-finite; `Burst ≤ 0`
or non-finite; `MaxDebt` non-zero and `< Burst`, or non-finite; `RejectCost`
negative or non-finite; `Generations == 1` (0 means default);
`RotationPeriod < 0`. Anything grudge's own validation rejects propagates.

Call-time panics (programming errors): NaN/±Inf/≤ 0 `cost` on any method
taking cost.

---

## 5. Adversarial posture (why SipHash is the default)

grudge defaults to murmur3 and documents the Kirsch–Mitzenmacher
amplification (one 64-bit collision collides at every level, bypassing the
min shield; murmur3 has seed-independent collision families). toll inverts
the default: **a rate limiter's keys are presumptively attacker-controlled**,
so it configures `grudge.SipHash()` unless the caller asserts `TrustedKeys:
true`. The field is named to make the caller state a fact about their keys,
not pick an algorithm. The ~2× hash cost is noise against the per-decision
work.

**Node-local caveat** (MUST appear in README): toll is a node-local limiter.
Behind N replicas, per-key limits and the aggregate backstop multiply by ~N
unless traffic is key-sharded or Rate/Burst are divided per replica.
Cross-instance convergence via grudge's merge law is vNext, after grudge
ships serialization.

---

## 6. Multi-window composition

Two windows (e.g. 10/s and 1000/h) are two Limiters; the composition recipe
is check-then-debit:

```go
if a.WouldAllowN(k, c) && b.WouldAllowN(k, c) {
    a.DebitN(k, c); b.DebitN(k, c)   // admit
}
```

Not atomic under concurrency (same race class as optimistic single-limiter);
avoids both the debit-then-refund leak and refund's clamp interactions. A
strict composite (locking multiple sketches in canonical order) is out of
scope until someone needs it. v1 MAY ship a `MultiLimiter` convenience
wrapping exactly this recipe; nothing more.

---

## 7. Testing requirements (definition of done)

1. **Rate-conformance sentinel**: single key, fake clock, scripted schedule —
   burst of `Burst/cost` admissions succeeds instantly; sustained admission
   rate thereafter equals `Rate/cost` per second; compare decisions
   one-for-one against a closed-form scalar token-bucket reference.
2. **Strict atomicity**: N goroutines racing `AllowN(k, 1)` with a fake clock
   (no refill during the race) admit exactly `Burst`. Leans on grudge's
   TryUpdate sentinel but MUST be re-proven through toll's wrapping.
3. **Conservative-direction property test**: random multi-key workload
   against a per-key exact reference; assert `admitted_toll(k) ≤
   admitted_reference(k)` for every stable key (collisions only stricten),
   with equality when L·M is sized so keys don't collide.
4. **RetryAfter correctness**: after a rejection, advancing the fake clock by
   exactly `RetryAfter` makes the same cost admissible; advancing by
   `RetryAfter − ε` does not. `NeverRetry` for `cost > Burst`.
5. **RejectCost**: hammering extends recovery, bounded by `MaxDebt/Rate`;
   without RejectCost, rejections leave debt untouched.
6. **Validation table** for §4; panic tests for bad cost.
7. **Alloc assertions**: hot-path methods 0 allocs/op
   (`testing.AllocsPerRun`), plus benchmarks recorded in BENCHMARKS.md.
8. Suite green under `-race`; goleak via TestMain (the Rotator goroutine).

## 8. Milestones

1. **M1 — core limiter**: Config/defaults/validation, optimistic AllowN/
   Allow, Spent, Close; tests 1, 3, 6, 7.
2. **M2 — strict mode**: TryUpdate wiring; test 2.
3. **M3 — Decision**: AllowDetailed, RetryAfter, NeverRetry; test 4.
4. **M4 — penalties + composition**: RejectCost/MaxDebt semantics,
   WouldAllowN/DebitN (+ optional MultiLimiter); test 5.
5. **M5 — docs**: README (error contract §1.3 verbatim, node-local caveat,
   sizing guidance `CellsPerLevel ≥ capacity/Rate`), BENCHMARKS.md.

## 9. Non-goals (v1)

- No blocking `Wait(ctx)` (closed-form wait makes it implementable later;
  it drags in context plumbing and wakeup-fairness questions).
- No HTTP/gRPC middleware in the core module (future `toll/http…`
  subpackages once the core is stable).
- No per-key rate overrides: heterogeneous tiers are one Limiter per tier.
- No cross-instance state sync (waits for grudge serialization/merge).
- No key enumeration, no metrics registry (callers wire Decision fields into
  their own metrics).

## 10. Design invariants

1. Debt representation is permanent: 0 ≡ full allowance; all debits positive.
2. Only `Decision.Allowed` is authoritative; other fields are advisory.
3. Collision error stays conservative: no code path may grant a stable key
   more than its bucket because of sketch error.
4. toll adds policy only — any need for new sketch behavior is a grudge spec
   amendment, not a workaround here.
5. Hot path stays allocation-free.
