package scoring

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type decompositionKey struct {
	Counts       [9]uint8
	MinimumGroup uint8
}

// An independent combinatorial enumeration, not the vendored fan evaluator.
// Nondecreasing group codes eliminate permutation duplicates: 111+123 and
// 123+111 are one decomposition. Pairs are separately enumerated.
func countSuitMelds(c [9]uint8, minimum uint8, memo map[decompositionKey]int) int {
	key := decompositionKey{c, minimum}
	if n, ok := memo[key]; ok {
		return n
	}
	i := 0
	for i < 9 && c[i] == 0 {
		i++
	}
	if i == 9 {
		return 1
	}
	n := 0
	if c[i] >= 3 && uint8(2*i) >= minimum {
		d := c
		d[i] -= 3
		n += countSuitMelds(d, uint8(2*i), memo)
	}
	if i < 7 && c[i+1] > 0 && c[i+2] > 0 && uint8(2*i+1) >= minimum {
		d := c
		d[i]--
		d[i+1]--
		d[i+2]--
		n += countSuitMelds(d, uint8(2*i+1), memo)
	}
	memo[key] = n
	return n
}
func suitDecompositionMaximum(total int) (int, [][9]uint8, int) {
	memo := map[decompositionKey]int{}
	maximum, count := 0, 0
	best := [][9]uint8{}
	var visit func([9]uint8, int, int)
	visit = func(c [9]uint8, pos, left int) {
		if pos == 9 {
			if left != 0 {
				return
			}
			count++
			n := 0
			if total%3 == 0 {
				n = countSuitMelds(c, 0, memo)
			} else {
				for i, v := range c {
					if v >= 2 {
						d := c
						d[i] -= 2
						n += countSuitMelds(d, 0, memo)
					}
				}
			}
			if n > maximum {
				maximum = n
				best = [][9]uint8{c}
			} else if n == maximum {
				best = append(best, c)
			}
			return
		}
		for v := 0; v <= 4 && v <= left; v++ {
			if left-v <= 4*(8-pos) {
				d := c
				d[pos] = uint8(v)
				visit(d, pos+1, left-v)
			}
		}
	}
	visit([9]uint8{}, 0, total)
	return maximum, best, count
}

func TestIndependentMaximumStandardDecompositions(t *testing.T) {
	// A closed regular hand has exactly one pair-bearing suit/honor component.
	// Honors have one partition. For all partitions of four melds across suits,
	// these per-component maxima also bound a mixed-suit regular hand at four.
	for total, want := range map[int]int{0: 1, 2: 1, 3: 1, 5: 1, 6: 1, 8: 2, 9: 2, 11: 3, 12: 3, 14: 4} {
		got, _, count := suitDecompositionMaximum(total)
		if got != want {
			t.Errorf("%d tiles: maximum %d want %d", total, got, want)
		}
		if total == 14 && count != 118800 {
			t.Errorf("enumerated %d fourteen-tile count vectors", count)
		}
	}
	pairMax := []int{1, 1, 2, 3, 4}
	setMax := []int{1, 1, 1, 2, 3}
	// Distribute the four melds among the pair-bearing numbered suit, the
	// other two numbered suits, and honors (unique decomposition). Pairing in
	// honors similarly leaves at most the no-pair twelve-tile bound of three.
	for a := 0; a <= 4; a++ {
		for b := 0; b <= 4-a; b++ {
			for c := 0; c <= 4-a-b; c++ {
				if pairMax[a]*setMax[b]*setMax[c] > 4 {
					t.Fatal("mixed-suit partition exceeds four")
				}
			}
		}
	}
}

type latencyCase struct {
	Name                    string
	Input                   Input
	CanonicalDecompositions int
}

func ambiguityCases(t testing.TB) []latencyCase {
	t.Helper()
	maximum, patterns, _ := suitDecompositionMaximum(14)
	if maximum != 4 || len(patterns) != 16 {
		t.Fatalf("unexpected maximum corpus: %d / %d", maximum, len(patterns))
	}
	out := []latencyCase{}
	for _, counts := range patterns {
		tiles := []string{}
		notation := ""
		for i, n := range counts {
			for j := uint8(0); j < n; j++ {
				tiles = append(tiles, fmt.Sprintf("%dm", i+1))
				notation += strconv.Itoa(i + 1)
			}
		}
		for win, n := range counts {
			if n == 0 {
				continue
			}
			winning := fmt.Sprintf("%dm", win+1)
			hand := append([]string(nil), tiles...)
			for i, tile := range hand {
				if tile == winning {
					hand = append(hand[:i], hand[i+1:]...)
					break
				}
			}
			for _, draw := range []bool{false, true} {
				out = append(out, latencyCase{Name: fmt.Sprintf("max4/%sm/win-%s/selfdraw-%t", notation, winning, draw), Input: Input{Hand: hand, WinTile: winning, SelfDraw: draw}, CanonicalDecompositions: maximum})
			}
		}
	}
	return out
}

func BenchmarkEvaluateMultiDecomposition(b *testing.B) {
	cases := ambiguityCases(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Evaluate(cases[i%len(cases)].Input); err != nil {
			b.Fatal(err)
		}
	}
}

type latencySummary struct {
	Name                    string  `json:"name"`
	Samples                 int     `json:"samples"`
	CanonicalDecompositions int     `json:"canonical_decompositions,omitempty"`
	MeanUS                  float64 `json:"mean_us"`
	P50US                   float64 `json:"p50_us"`
	P95US                   float64 `json:"p95_us"`
	MaxUS                   float64 `json:"max_us"`
}

func latencySummarize(name string, values []int64, divisions int) latencySummary {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var total int64
	for _, v := range values {
		total += v
	}
	n := len(values)
	return latencySummary{Name: name, Samples: n, CanonicalDecompositions: divisions, MeanUS: float64(total) / float64(n) / 1000, P50US: float64(values[(n-1)/2]) / 1000, P95US: float64(values[(95*n+99)/100-1]) / 1000, MaxUS: float64(values[n-1]) / 1000}
}

// Explicit opt-in measurement: no artificial assertion on noisy shared-runner
// wall time. Includes the exported Go API, input validation, CGO, native scoring,
// exact decomposition extraction and explanation construction.
func TestScoringLatencyProfile(t *testing.T) {
	raw := os.Getenv("SCORING_LATENCY_SAMPLES")
	if raw == "" {
		t.Skip("set SCORING_LATENCY_SAMPLES for the real latency profile")
	}
	samples, err := strconv.Atoi(raw)
	if err != nil || samples < 100 || samples > 100000 {
		t.Fatal("SCORING_LATENCY_SAMPLES must be 100..100000")
	}
	cases := ambiguityCases(t)
	ambiguousCount := len(cases)
	for _, c := range loadFanCases(t) {
		cases = append(cases, latencyCase{Name: fmt.Sprintf("81-fan/%02d", c.ID), Input: c.Input})
	}
	data, err := os.ReadFile("testdata/upstream_hands.json")
	if err != nil {
		t.Fatal(err)
	}
	var upstream []struct {
		SourceLine int   `json:"source_line"`
		Input      Input `json:"input"`
	}
	if err = json.Unmarshal(data, &upstream); err != nil {
		t.Fatal(err)
	}
	for _, c := range upstream {
		cases = append(cases, latencyCase{Name: fmt.Sprintf("upstream/line-%d", c.SourceLine), Input: c.Input})
	}
	for _, c := range cases {
		for warm := 0; warm < 100; warm++ {
			if _, err = Evaluate(c.Input); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
		}
	}
	values := make([][]int64, len(cases))
	for i := range values {
		values[i] = make([]int64, 0, samples)
	}
	start := time.Now()
	// Interleave cases to avoid putting one hand entirely before a scheduler/GC
	// event. The benchmark includes ordinary Go GC and process scheduling.
	for sample := 0; sample < samples; sample++ {
		for i, c := range cases {
			before := time.Now()
			_, err := Evaluate(c.Input)
			elapsed := time.Since(before).Nanoseconds()
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			values[i] = append(values[i], elapsed)
		}
	}
	report := struct {
		Timestamp      string           `json:"timestamp_utc"`
		GOOS           string           `json:"goos"`
		GOARCH         string           `json:"goarch"`
		GoVersion      string           `json:"go_version"`
		CPU            string           `json:"cpu"`
		CPUs           int              `json:"logical_cpus"`
		GOMAXPROCS     int              `json:"gomaxprocs"`
		ElapsedSeconds float64          `json:"elapsed_seconds"`
		SamplesPerCase int              `json:"samples_per_case"`
		AmbiguousCases int              `json:"ambiguous_cases"`
		FixtureCases   int              `json:"fixture_cases"`
		Evaluator      string           `json:"evaluator"`
		Aggregate      latencySummary   `json:"aggregate"`
		Ambiguous      latencySummary   `json:"ambiguous_aggregate"`
		Cases          []latencySummary `json:"cases"`
	}{Timestamp: time.Now().UTC().Format(time.RFC3339), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), ElapsedSeconds: time.Since(start).Seconds(), SamplesPerCase: samples, AmbiguousCases: ambiguousCount, FixtureCases: len(cases) - ambiguousCount, Evaluator: Profile}
	if info, e := os.ReadFile("/proc/cpuinfo"); e == nil {
		for _, line := range strings.Split(string(info), "\n") {
			if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Hardware") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					report.CPU = strings.TrimSpace(parts[1])
					break
				}
			}
		}
	}
	all := make([]int64, 0, samples*len(cases))
	ambiguous := make([]int64, 0, samples*ambiguousCount)
	for i, c := range cases {
		all = append(all, values[i]...)
		if i < ambiguousCount {
			ambiguous = append(ambiguous, values[i]...)
		}
		report.Cases = append(report.Cases, latencySummarize(c.Name, values[i], c.CanonicalDecompositions))
	}
	report.Aggregate = latencySummarize("all-cases", all, 0)
	report.Ambiguous = latencySummarize("maximum-standard-decompositions", ambiguous, 4)
	sort.Slice(report.Cases, func(i, j int) bool { return report.Cases[i].P95US > report.Cases[j].P95US })
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("SCORING_LATENCY_REPORT"); path != "" {
		if err = os.WriteFile(path, append(encoded, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("CPU=%s arch=%s samples=%d cases=%d maximum-standard-shapes=%d; all mean=%.3fus p95=%.3fus max=%.3fus; ambiguous p95=%.3fus max=%.3fus; slowest-p95=%s %.3fus", report.CPU, report.GOARCH, samples, len(cases), ambiguousCount, report.Aggregate.MeanUS, report.Aggregate.P95US, report.Aggregate.MaxUS, report.Ambiguous.P95US, report.Ambiguous.MaxUS, report.Cases[0].Name, report.Cases[0].P95US)
}
