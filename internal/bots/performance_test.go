package bots

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
)

type heuristicCase struct {
	Name    string
	View    json.RawMessage
	Options []rulesdk.Option
}

func heuristicConfig() rulesdk.Config {
	return rulesdk.Config{Format: "practice_1", Profile: "om-mcr-1", Participants: []rulesdk.Participant{{ID: "A", Name: "A", Kind: "builtin"}, {ID: "B", Name: "B", Kind: "builtin"}, {ID: "C", Name: "C", Kind: "builtin"}, {ID: "D", Name: "D", Kind: "builtin"}}}
}
func projectedHeuristicCase(t testing.TB, name string, raw json.RawMessage) heuristicCase {
	t.Helper()
	r := mcr.New()
	flow, err := r.Inspect(raw)
	if err != nil || len(flow.Decisions) != 1 {
		t.Fatalf("%s invalid fixture: %v", name, err)
	}
	d := flow.Decisions[0]
	view, err := r.Project(raw, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: d.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	return heuristicCase{Name: name, View: view, Options: d.Options}
}

// Rebuild a genuine 144-physical-tile position around one deliberately dense
// hand. Every other seat still holds thirteen real non-flower tiles; all unused
// tiles remain in the live wall. Inspect validates conservation and produces the
// actual legal options. Choose receives only Project's participant view.
func craftedHeuristicCase(t testing.TB, name, hand string) heuristicCase {
	t.Helper()
	r := mcr.New()
	seed := sha256.Sum256([]byte("heuristic-fixture"))
	raw, err := r.Init(heuristicConfig(), seed[:])
	if err != nil {
		t.Fatal(err)
	}
	var state mcr.State
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	pool := append([]mcr.Tile(nil), state.Wall...)
	used := map[string]bool{}
	state.Seats = make([]mcr.Seat, 4)
	state.InitialOrder = []string{"A", "B", "C", "D"}
	state.Active = 0
	state.Dealer = 0
	state.Step = 1
	state.Phase = "self"
	state.Head = 0
	state.Pending = nil
	state.Result = nil
	state.Discards = nil
	state.CanKong = true
	state.DrawSource = "normal"
	for i := range state.Seats {
		state.Seats[i] = mcr.Seat{ParticipantID: string(rune('A' + i)), Hand: []mcr.Tile{}, Melds: []mcr.Meld{}, Flowers: []mcr.Tile{}}
	}
	for _, kind := range strings.Fields(hand) {
		found := false
		for _, tile := range pool {
			if tile.Kind == kind && !used[tile.ID] {
				state.Seats[0].Hand = append(state.Seats[0].Hand, tile)
				used[tile.ID] = true
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("too many fixture tiles %s", kind)
		}
	}
	if len(state.Seats[0].Hand) != 14 {
		t.Fatal("fixture must have fourteen tiles")
	}
	for i := 1; i < 4; i++ {
		for _, tile := range pool {
			if !used[tile.ID] && !strings.HasPrefix(tile.Kind, "h") {
				state.Seats[i].Hand = append(state.Seats[i].Hand, tile)
				used[tile.ID] = true
				if len(state.Seats[i].Hand) == 13 {
					break
				}
			}
		}
	}
	state.Wall = nil
	for _, tile := range pool {
		if !used[tile.ID] {
			state.Wall = append(state.Wall, tile)
		}
	}
	state.Tail = len(state.Wall)
	for _, tile := range pool {
		if used[tile.ID] {
			state.Wall = append(state.Wall, tile)
		}
	}
	state.DrawnID = state.Seats[0].Hand[13].ID
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return projectedHeuristicCase(t, name, raw)
}

func heuristicCorpus(t testing.TB) []heuristicCase {
	t.Helper()
	r := mcr.New()
	cases := []heuristicCase{}
	for i := 0; i < 256; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("heuristic-initial-%d", i)))
		raw, err := r.Init(heuristicConfig(), seed[:])
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, projectedHeuristicCase(t, fmt.Sprintf("initial-%03d", i), raw))
	}
	for _, c := range []struct{ name, hand string }{
		{"dense-single-suit-a", "1m 1m 1m 2m 2m 2m 2m 3m 3m 3m 3m 4m 4m 5m"},
		{"dense-single-suit-b", "2m 2m 3m 3m 3m 4m 4m 4m 5m 5m 5m 6m 6m 8m"},
		{"near-seven-pairs", "1m 1m 2m 2m 3m 3m 4m 4m 5m 5m 6m 6m 7m 9p"},
		{"thirteen-orphans-plus-simple", "1m 9m 1p 9p 1s 9s 1z 2z 3z 4z 5z 6z 7z 2m"},
		{"knitted-ready-plus-honor", "1m 4m 7m 2s 5s 8s 3p 6p 9p 5z 5z 5z 1z 2z"},
		{"nine-gates-extra-simple", "1m 1m 1m 2m 3m 4m 5m 6m 7m 8m 9m 9m 9m 2p"},
		{"dense-complete-hu-short-circuit", "1m 1m 1m 2m 2m 2m 3m 3m 3m 4m 4m 4m 5m 5m"},
	} {
		cases = append(cases, craftedHeuristicCase(t, c.name, c.hand))
	}
	return cases
}

func TestHeuristicPerformanceFixturesUseLegalObservations(t *testing.T) {
	for _, c := range heuristicCorpus(t) {
		choice, err := Choose("basic_heuristic", c.View, c.Options)
		if err != nil {
			t.Fatal(c.Name, err)
		}
		valid := false
		for _, o := range c.Options {
			if o.ID == choice {
				valid = true
			}
		}
		if !valid {
			t.Fatal("chose outside legal options", c.Name, choice)
		}
	}
}

func BenchmarkBasicHeuristic(b *testing.B) {
	cases := heuristicCorpus(b)
	for _, name := range []string{"initial-000", "dense-single-suit-a", "nine-gates-extra-simple", "dense-complete-hu-short-circuit"} {
		for _, c := range cases {
			if c.Name != name {
				continue
			}
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := Choose("basic_heuristic", c.View, c.Options); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

type heuristicLatency struct {
	Name                 string   `json:"name"`
	Samples              int      `json:"samples"`
	MeanUS               float64  `json:"mean_us"`
	P95US                float64  `json:"p95_us"`
	MaxUS                float64  `json:"max_us"`
	DiscardOptions       int      `json:"discard_options"`
	DistinctDiscardKinds int      `json:"distinct_discard_kinds"`
	ShantenCalls         int      `json:"shanten_calls"`
	ScoreCalls           int      `json:"score_calls"`
	ShantenWorkMeanUS    float64  `json:"shanten_work_mean_us"`
	DecodeMeanUS         float64  `json:"decode_mean_us"`
	Hand                 []string `json:"fixture_hand"`
}

func profileHeuristic(t testing.TB, c heuristicCase, n int) heuristicLatency {
	t.Helper()
	values := make([]int64, n)
	var sum int64
	for i := range values {
		begin := time.Now()
		if _, err := Choose("basic_heuristic", c.View, c.Options); err != nil {
			t.Fatal(err)
		}
		values[i] = time.Since(begin).Nanoseconds()
		sum += values[i]
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out := heuristicLatency{Name: c.Name, Samples: n, MeanUS: float64(sum) / float64(n) / 1000, P95US: float64(values[(95*n+99)/100-1]) / 1000, MaxUS: float64(values[n-1]) / 1000}
	var v observation
	if err := json.Unmarshal(c.View, &v); err != nil {
		t.Fatal(err)
	}
	for _, tile := range v.Hand {
		out.Hand = append(out.Hand, tile.Kind)
	}
	short := false
	for _, o := range c.Options {
		if o.Type == "hu" || o.Type == "pass" || o.Type == "replace_flower" {
			short = true
		}
	}
	unique := map[string]bool{}
	evaluatedKinds := map[string]bool{}
	hands := [][]string{}
	for _, o := range c.Options {
		if o.Type != "discard" {
			continue
		}
		out.DiscardOptions++
		hand := []string{}
		discardKind := ""
		for _, tile := range v.Hand {
			if tile.ID == o.TileID {
				unique[tile.Kind] = true
				discardKind = tile.Kind
			} else {
				hand = append(hand, tile.Kind)
			}
		}
		if !short && !evaluatedKinds[discardKind] {
			evaluatedKinds[discardKind] = true
			hands = append(hands, hand)
			d, improve, err := scoring.Shanten(hand, 0)
			if err != nil {
				t.Fatal(err)
			}
			if d == 0 {
				out.ScoreCalls += len(improve)
			}
		}
	}
	out.DistinctDiscardKinds = len(unique)
	out.ShantenCalls = len(hands)
	begin := time.Now()
	for i := 0; i < n; i++ {
		var decoded observation
		if err := json.Unmarshal(c.View, &decoded); err != nil {
			t.Fatal(err)
		}
	}
	out.DecodeMeanUS = float64(time.Since(begin).Nanoseconds()) / float64(n) / 1000
	begin = time.Now()
	for i := 0; i < n; i++ {
		for _, hand := range hands {
			if _, _, err := scoring.Shanten(hand, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	out.ShantenWorkMeanUS = float64(time.Since(begin).Nanoseconds()) / float64(n) / 1000
	return out
}

func TestBasicHeuristicReuseLatencyComparison(t *testing.T) {
	raw := os.Getenv("BOT_COMPARISON_SAMPLES")
	if raw == "" {
		t.Skip("set BOT_COMPARISON_SAMPLES for paired before/after timing")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 100 || n > 20000 {
		t.Fatal("BOT_COMPARISON_SAMPLES must be 100..20000")
	}
	corpus := heuristicCorpus(t)
	selected := map[string]bool{"initial-000": true, "near-seven-pairs": true, "dense-single-suit-b": true, "nine-gates-extra-simple": true, "initial-093": true, "initial-206": true, "dense-complete-hu-short-circuit": true}
	results := []any{}
	for _, c := range corpus {
		if !selected[c.Name] {
			continue
		}
		before, after := make([]int64, n), make([]int64, n)
		measure := func(fn func(string, json.RawMessage, []rulesdk.Option) (string, error)) (string, int64) {
			start := time.Now()
			choice, err := fn("basic_heuristic", c.View, c.Options)
			duration := time.Since(start).Nanoseconds()
			if err != nil {
				t.Fatal(err)
			}
			return choice, duration
		}
		for i := 0; i < n; i++ {
			var a, b string
			if i%2 == 0 {
				a, before[i] = measure(referenceChooseBeforeReuse)
				b, after[i] = measure(Choose)
			} else {
				b, after[i] = measure(Choose)
				a, before[i] = measure(referenceChooseBeforeReuse)
			}
			if a != b {
				t.Fatalf("timed strategies differ %s: %s vs %s", c.Name, a, b)
			}
		}
		summarize := func(v []int64) map[string]any {
			sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
			var sum int64
			for _, value := range v {
				sum += value
			}
			return map[string]any{"samples": n, "mean_us": float64(sum) / float64(n) / 1000, "p95_us": float64(v[(95*n+99)/100-1]) / 1000, "max_us": float64(v[n-1]) / 1000}
		}
		results = append(results, map[string]any{"name": c.Name, "before": summarize(before), "after": summarize(after)})
	}
	if len(results) != len(selected) {
		t.Fatal("missing comparison case")
	}
	cpu := "unknown"
	if b, e := os.ReadFile("/proc/cpuinfo"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	report := map[string]any{"timestamp_utc": time.Now().UTC().Format(time.RFC3339), "cpu": cpu, "go_version": runtime.Version(), "arch": runtime.GOARCH, "os": runtime.GOOS, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "scope": "Paired Choose calls on identical real MCR observations, alternating before/after order. Before is frozen original test-only implementation; after reuses candidate score by discard kind. Excludes engine, database and network.", "selection": "fixed typical initial-000, five slowest in the original 263-observation screening, and legal hu fast path", "results": results}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("BOT_COMPARISON_REPORT"); path != "" {
		if err = os.WriteFile(path, append(encoded, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}

func TestBasicHeuristicLatencyProfile(t *testing.T) {
	raw := os.Getenv("BOT_LATENCY_SAMPLES")
	if raw == "" {
		t.Skip("set BOT_LATENCY_SAMPLES for measured baseline strategy latency")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 100 || n > 20000 {
		t.Fatal("BOT_LATENCY_SAMPLES must be 100..20000")
	}
	cases := heuristicCorpus(t)
	type probe struct {
		c  heuristicCase
		ns int64
	}
	probes := []probe{}
	for _, c := range cases {
		start := time.Now()
		for i := 0; i < 30; i++ {
			if _, err := Choose("basic_heuristic", c.View, c.Options); err != nil {
				t.Fatal(err)
			}
		}
		probes = append(probes, probe{c, time.Since(start).Nanoseconds()})
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].ns > probes[j].ns })
	results := []heuristicLatency{profileHeuristic(t, cases[0], n)}
	for i := 0; i < 5; i++ {
		results = append(results, profileHeuristic(t, probes[i].c, n))
	}
	results = append(results, profileHeuristic(t, cases[len(cases)-1], n))
	cpu := "unknown"
	if b, e := os.ReadFile("/proc/cpuinfo"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	report := map[string]any{"timestamp_utc": time.Now().UTC().Format(time.RFC3339), "cpu": cpu, "go_version": runtime.Version(), "arch": runtime.GOARCH, "os": runtime.GOOS, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "corpus_size": len(cases), "screening_repeats": 30, "scope": "Choose only, including participant JSON decode; valid engine-generated legal options; excludes engine projection, database and network. Shanten/decode component means are separately measured, not sampled spans of Choose.", "selection": "initial-000 plus five slowest observed screened cases plus winning-hand fast path; empirical worst in this corpus, not exhaustive worst case", "results": results}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("BOT_LATENCY_REPORT"); path != "" {
		if err = os.WriteFile(path, append(encoded, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}
