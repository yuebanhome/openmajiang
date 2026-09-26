package mcr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// This records a real simultaneous three-winner response workload. It does not
// include the configured human/Bot response waiting time, HTTP or PostgreSQL;
// those end-to-end latencies are measured by TestCapacityOneHour separately.
func TestThreeWayWinWindowLatency(t *testing.T) {
	s := fixture(t,
		"1z 2z 4z 4z 4z 5z 5z 5z 6z 6z 6z 7z 7z 7z",
		"1m 2m 3m 4m 5m 6m 7m 8m 9m 1p 1p 1p 1z",
		"1s 2s 3s 4s 5s 6s 7s 8s 9s 2p 2p 2p 1z",
		"1p 2p 3p 4p 5p 6p 7p 8p 9p 3m 3m 3m 1z")
	s = discard(t, s, 0, "1z")
	raw := encode(t, s)
	rule := Rule{}
	choices := map[string]string{}
	for seat := 1; seat < 4; seat++ {
		if len(s.Seats[seat].Hand) != 13 {
			t.Fatal("invalid thirteen-tile winner fixture")
		}
		hu := choose(t, s.reactionOptions(seat), "hu")
		choices[s.Seats[seat].ParticipantID] = hu.ID
	}
	values := make([]int64, 1000)
	for i := range values {
		start := time.Now()
		flow, err := rule.Inspect(raw)
		if err != nil || len(flow.Decisions) != 3 {
			t.Fatalf("three-way inspect failed: %v", err)
		}
		next, err := rule.Apply(raw, rulesdk.Input{Type: "resolve", Choices: choices})
		if err != nil {
			t.Fatal(err)
		}
		for _, viewer := range []rulesdk.Viewer{{Audience: rulesdk.SpectatorDiscardOnly}, {Audience: rulesdk.ParticipantPrivate, ParticipantID: "A"}, {Audience: rulesdk.ParticipantPrivate, ParticipantID: "B"}, {Audience: rulesdk.ParticipantPrivate, ParticipantID: "C"}, {Audience: rulesdk.ParticipantPrivate, ParticipantID: "D"}} {
			if _, err = rule.Project(next.State, viewer); err != nil {
				t.Fatal(err)
			}
		}
		values[i] = int64(time.Since(start))
		if i == 0 {
			settled := state(t, next.State)
			if settled.Result.Winner != 1 {
				t.Fatal("nearest player did not win simultaneous three-way window")
			}
			assertSpectator(t, next.State)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	cpu := "unavailable"
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	report := map[string]any{"scope": "Inspect all three legal-win decisions, arbitrate all three hu choices, project four participant views and one discard-only spectator view; excludes network/database/waiting time", "samples": len(values), "go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "cpu_model": cpu, "p95_ms": float64(values[949]) / float64(time.Millisecond), "max_ms": float64(values[len(values)-1]) / float64(time.Millisecond)}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("three-way MCR window measurement: %s", encoded)
	if path := os.Getenv("OMJ_MCR_WINDOW_REPORT"); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
