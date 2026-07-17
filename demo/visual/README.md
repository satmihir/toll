# toll visual demo

A 36-second, trace-driven animation of toll under load, in three acts:

1. **Without a limiter** — 3 greedy clients (300 req/s wanted each) take 83% of a
   400 req/s API; nine compliant clients get 7 of their 20 req/s.
2. **With toll** (`Rate: 40, Burst: 60` per key) — greedy clients clamp to exactly
   40 req/s, compliant clients get 100% of demand, zero compliant rejections.
3. **Key-rotation attack** (5,000 req/s, fresh key per request) — a map of token
   buckets admits 100% with state growing forever; toll caps admission at the
   aggregate ceiling (`CellsPerLevel × Rate = 2,000/s`, with `MaxDebt` headroom so
   debt accumulates — without that headroom the bound is the looser
   `Levels × CellsPerLevel × Rate`; see the README's error contract) in 188 KB of
   flat state (24 B/cell: score, timestamp, lock). An LRU-capped map would bound
   the memory but still fail open: evicting an active key resets its bucket.

Everything on screen replays `trace.js`, recorded by a deterministic simulation
of the real toll API against a fake clock — the animation never recomputes the
algorithm, and the simulation panics if any headline number drifts.

## Regenerate

```bash
go run ./demo/sim                    # re-run the simulation -> trace.js
open demo/visual/index.html          # interactive playback (scrubber + pause)
node demo/visual/render_video.mjs    # deterministic MP4 + thumbnail (Chrome + ffmpeg)
```

Fixed-frame view for inspection: `index.html?recording=1&t=SECONDS`.

Output: `toll-demo.mp4` (1920×1080, 16fps, 36s, ~0.5 MB) and
`toll-demo-thumbnail.png` (the Act-2 clamp moment).
