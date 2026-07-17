// Command sim runs the deterministic simulations behind demo/visual and writes
// the recorded trace to demo/visual/trace.js. The visualization only replays
// this trace; it never recomputes the algorithm. Sanity checks panic if the
// headline numbers drift, so the video cannot silently diverge from the truth.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/satmihir/grudge/grudgetest"
	"github.com/satmihir/toll"
)

const (
	durationS = 60
	tickMs    = 100
	ticks     = durationS * 1000 / tickMs

	// Act A: one API, 12 clients.
	capacityPerSec = 400.0
	nCompliant     = 9
	compliantWant  = 20.0 // req/s each
	nGreedy        = 3
	greedyWant     = 300.0 // req/s each (15x a compliant client)
	perKeyRate     = 40.0
	perKeyBurst    = 60.0

	// Act B: key-rotation attack.
	attackPerSec = 5000
	atkRate      = 2.0
	atkBurst     = 5.0
	// MaxDebt >> Burst so admitted attack debt accumulates instead of being
	// discarded by the clamp — that accumulation is what makes the aggregate
	// ceiling (CellsPerLevel x Rate) bind. With MaxDebt = Burst the limiter is
	// still bounded under rotation, but looser (~65% of this attack).
	atkMaxDebt  = 50.0
	atkLevels   = 4
	atkCells    = 1000
	generations = 2
)

type client struct {
	ID   string  `json:"id"`
	Kind string  `json:"kind"` // "compliant" | "greedy"
	Want float64 `json:"want"` // req/s
}

type actA struct {
	Clients  []client    `json:"clients"`
	Baseline [][]float64 `json:"baseline"` // [tick][client] admitted req/s
	Toll     [][]float64 `json:"toll"`     // [tick][client] admitted req/s
}

type actBSample struct {
	MapAdmitted  float64 `json:"mapAdmitted"`  // req/s this tick
	TollAdmitted float64 `json:"tollAdmitted"` // req/s this tick
	MapBytes     int64   `json:"mapBytes"`
	TollBytes    int64   `json:"tollBytes"`
}

type trace struct {
	Meta struct {
		TickMs     int     `json:"tickMs"`
		DurationS  int     `json:"durationS"`
		Capacity   float64 `json:"capacity"`
		PerKeyRate float64 `json:"perKeyRate"`
		PerKeyBurst float64 `json:"perKeyBurst"`
		AttackRate float64 `json:"attackRate"`
		Ceiling    float64 `json:"ceiling"`
	} `json:"meta"`
	ActA  actA         `json:"actA"`
	ActB  []actBSample `json:"actB"`
	Stats struct {
		GreedySharePct     float64 `json:"greedySharePct"`
		CompliantBaseline  float64 `json:"compliantBaselineRate"`
		CompliantRejects   int     `json:"compliantRejectsWithToll"`
		GreedySteadyRate   float64 `json:"greedySteadyRate"`
		TollBytes          int64   `json:"tollBytes"`
		MapBytesEnd        int64   `json:"mapBytesEnd"`
		TollAdmittedSteady float64 `json:"tollAdmittedSteady"`
		MapAdmittedSteady  float64 `json:"mapAdmittedSteady"`
	} `json:"stats"`
}

func clients() []client {
	var cs []client
	for i := 0; i < nCompliant; i++ {
		cs = append(cs, client{ID: fmt.Sprintf("client-%d", i+1), Kind: "compliant", Want: compliantWant})
	}
	for i := 0; i < nGreedy; i++ {
		cs = append(cs, client{ID: fmt.Sprintf("greedy-%d", i+1), Kind: "greedy", Want: greedyWant})
	}
	return cs
}

// simBaseline models the unlimited API: admission is proportional-share of
// capacity (an FCFS approximation — everyone gets capacity * want/totalWant
// when demand exceeds capacity).
func simBaseline(cs []client) [][]float64 {
	totalWant := 0.0
	for _, c := range cs {
		totalWant += c.Want
	}
	share := capacityPerSec / totalWant // < 1: oversubscribed
	out := make([][]float64, ticks)
	for t := range out {
		row := make([]float64, len(cs))
		for i, c := range cs {
			row[i] = c.Want * share
		}
		out[t] = row
	}
	return out
}

// simToll drives the same demand through one toll.Limiter with per-key limits,
// issuing individual unit-cost requests against a fake clock. Fractional
// requests per tick are carried between ticks so demand is exact over time.
func simToll(cs []client) (series [][]float64, compliantRejects int, greedySteady float64) {
	clock := grudgetest.NewFakeClock()
	lim, err := toll.New(toll.Config{
		Rate: perKeyRate, Burst: perKeyBurst,
		Clock: clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		panic(err)
	}
	defer lim.Close()

	carry := make([]float64, len(cs))
	series = make([][]float64, ticks)
	greedySum, greedyN := 0.0, 0

	for t := 0; t < ticks; t++ {
		clock.Advance(tickMs * time.Millisecond)
		row := make([]float64, len(cs))
		for i, c := range cs {
			carry[i] += c.Want * tickMs / 1000.0
			n := int(carry[i])
			carry[i] -= float64(n)
			admitted := 0
			for r := 0; r < n; r++ {
				if lim.Allow([]byte(c.ID)) {
					admitted++
				} else if c.Kind == "compliant" {
					compliantRejects++
				}
			}
			row[i] = float64(admitted) * 1000.0 / tickMs // req/s
		}
		series[t] = row

		if t >= ticks/2 { // steady-state window: last 30s
			for i, c := range cs {
				if c.Kind == "greedy" {
					greedySum += series[t][i]
					greedyN++
					_ = i
				}
			}
		}
	}
	greedySteady = greedySum / float64(greedyN)
	return series, compliantRejects, greedySteady
}

// simAttack floods both limiters with a fresh key per request.
func simAttack() (samples []actBSample, tollSteady, mapSteady float64) {
	clock := grudgetest.NewFakeClock()
	lim, err := toll.New(toll.Config{
		Rate: atkRate, Burst: atkBurst, MaxDebt: atkMaxDebt,
		Levels: atkLevels, CellsPerLevel: atkCells,
		Clock: clock, Ticker: grudgetest.NewFakeTicker(),
	})
	if err != nil {
		panic(err)
	}
	defer lim.Close()
	ml := newMapLimiter(atkRate, atkBurst)

	perTick := attackPerSec * tickMs / 1000 // fresh keys per tick
	keyCounter := 0
	samples = make([]actBSample, ticks)
	tollSum, mapSum, steadyN := 0.0, 0.0, 0

	for t := 0; t < ticks; t++ {
		clock.Advance(tickMs * time.Millisecond)
		now := clock.Now().UnixMilli()
		tollAdmit, mapAdmit := 0, 0
		for r := 0; r < perTick; r++ {
			key := fmt.Sprintf("atk-%d", keyCounter)
			keyCounter++
			if lim.Allow([]byte(key)) {
				tollAdmit++
			}
			if ml.allow(key, now, 1) {
				mapAdmit++
			}
		}
		samples[t] = actBSample{
			MapAdmitted:  float64(mapAdmit) * 1000.0 / tickMs,
			TollAdmitted: float64(tollAdmit) * 1000.0 / tickMs,
			MapBytes:     ml.stateBytes(),
			TollBytes:    tollStateBytes(),
		}
		if t >= ticks*5/6 { // steady window: last 10s
			tollSum += samples[t].TollAdmitted
			mapSum += samples[t].MapAdmitted
			steadyN++
		}
	}
	return samples, tollSum / float64(steadyN), mapSum / float64(steadyN)
}

func tollStateBytes() int64 {
	// 24 bytes per cell on 64-bit Go: float64 score + int64 timestamp +
	// sync.Mutex — the real footprint, not just the 16-byte payload.
	return int64(atkLevels) * atkCells * 24 * generations
}

func main() {
	cs := clients()
	tr := trace{}
	tr.Meta.TickMs = tickMs
	tr.Meta.DurationS = durationS
	tr.Meta.Capacity = capacityPerSec
	tr.Meta.PerKeyRate = perKeyRate
	tr.Meta.PerKeyBurst = perKeyBurst
	tr.Meta.AttackRate = attackPerSec
	tr.Meta.Ceiling = atkCells * atkRate

	tr.ActA.Clients = cs
	tr.ActA.Baseline = simBaseline(cs)
	var greedySteady float64
	tr.ActA.Toll, tr.Stats.CompliantRejects, greedySteady = simToll(cs)
	tr.Stats.GreedySteadyRate = greedySteady

	greedyBase := 0.0
	for i, c := range cs {
		if c.Kind == "greedy" {
			greedyBase += tr.ActA.Baseline[0][i]
		} else {
			tr.Stats.CompliantBaseline = tr.ActA.Baseline[0][i]
		}
	}
	tr.Stats.GreedySharePct = 100 * greedyBase / capacityPerSec

	var tollSteady, mapSteady float64
	tr.ActB, tollSteady, mapSteady = simAttack()
	tr.Stats.TollAdmittedSteady = tollSteady
	tr.Stats.MapAdmittedSteady = mapSteady
	tr.Stats.TollBytes = tollStateBytes()
	tr.Stats.MapBytesEnd = tr.ActB[len(tr.ActB)-1].MapBytes

	sanity(tr)

	blob, err := json.Marshal(tr)
	if err != nil {
		panic(err)
	}
	out := "window.TOLL_TRACE = " + string(blob) + ";\n"
	if err := os.WriteFile("demo/visual/trace.js", []byte(out), 0o644); err != nil {
		panic(err)
	}

	fmt.Printf("trace written: %d KB\n", len(out)/1024)
	fmt.Printf("actA  greedy share (baseline):   %.1f%%\n", tr.Stats.GreedySharePct)
	fmt.Printf("actA  compliant baseline rate:   %.1f/s of %.0f/s wanted\n", tr.Stats.CompliantBaseline, compliantWant)
	fmt.Printf("actA  compliant rejects w/ toll: %d\n", tr.Stats.CompliantRejects)
	fmt.Printf("actA  greedy steady w/ toll:     %.1f/s (limit %.0f)\n", tr.Stats.GreedySteadyRate, perKeyRate)
	fmt.Printf("actB  toll admitted steady:      %.0f/s (ceiling %.0f)\n", tr.Stats.TollAdmittedSteady, tr.Meta.Ceiling)
	fmt.Printf("actB  map admitted steady:       %.0f/s (attack %d)\n", tr.Stats.MapAdmittedSteady, attackPerSec)
	fmt.Printf("actB  state: toll %d KB flat, map %.1f MB and growing\n",
		tr.Stats.TollBytes/1024, float64(tr.Stats.MapBytesEnd)/1e6)
}

// sanity panics if any headline number drifts from what the visual claims.
func sanity(tr trace) {
	if tr.Stats.CompliantRejects != 0 {
		panic(fmt.Sprintf("sanity: compliant rejects with toll = %d, want 0", tr.Stats.CompliantRejects))
	}
	if tr.Stats.GreedySteadyRate < perKeyRate-2 || tr.Stats.GreedySteadyRate > perKeyRate+2 {
		panic(fmt.Sprintf("sanity: greedy steady rate %.2f not within ±2 of limit %.0f", tr.Stats.GreedySteadyRate, perKeyRate))
	}
	if tr.Stats.GreedySharePct < 80 {
		panic(fmt.Sprintf("sanity: baseline greedy share %.1f%% < 80%%", tr.Stats.GreedySharePct))
	}
	ceiling := tr.Meta.Ceiling
	if tr.Stats.TollAdmittedSteady > ceiling*1.15 {
		panic(fmt.Sprintf("sanity: toll steady admitted %.0f exceeds ceiling %.0f by >15%%", tr.Stats.TollAdmittedSteady, ceiling))
	}
	if tr.Stats.MapAdmittedSteady < float64(attackPerSec)*0.99 {
		panic(fmt.Sprintf("sanity: map limiter should admit ~all of the attack, got %.0f/s", tr.Stats.MapAdmittedSteady))
	}
	last := tr.ActB[len(tr.ActB)-1]
	mid := tr.ActB[len(tr.ActB)/2]
	if last.MapBytes <= mid.MapBytes {
		panic("sanity: map state size should be strictly growing")
	}
	if last.TollBytes != tr.ActB[0].TollBytes {
		panic("sanity: toll state size should be constant")
	}
}
