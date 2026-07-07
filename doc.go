// Package toll is a per-key token-bucket rate limiter for unbounded,
// potentially attacker-controlled key spaces in constant memory: no per-key
// state, no eviction, no background work, O(L) per decision.
//
// It is a thin policy layer over github.com/satmihir/grudge, a decaying-score
// sketch. toll stores each key's spent tokens (debt) in the sketch, drains it
// at the refill rate via grudge's linear decay, and admits a request when the
// key has headroom. A fresh, never-seen key reads debt zero — a full bucket —
// for free.
//
// The error is one-sided: because collisions can only over-count a stable
// key's debt, the limiter may throttle such a key early but never grants it
// more than its bucket. The permissive failure mode exists only across key
// identities (an adversary rotating keys), which the aggregate refill ceiling
// bounds. See spec/SPEC.md for the normative specification.
//
// toll is node-local. Behind N replicas, per-key limits scale with the number
// of serving replicas unless traffic is key-sharded or the rate is divided per
// replica.
package toll
