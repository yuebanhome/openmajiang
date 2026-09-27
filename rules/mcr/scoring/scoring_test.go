package scoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

type fanCase struct {
	ID       int    `json:"fan_id"`
	Notation string `json:"notation"`
	Input    Input  `json:"input"`
}

func loadFanCases(t testing.TB) []fanCase {
	t.Helper()
	data, err := os.ReadFile("testdata/fan_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []fanCase
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func fanCount(result Result, id int) int {
	for _, f := range result.Fans {
		if f.ID == id {
			return f.Count
		}
	}
	return 0
}

func checkSum(t testing.TB, result Result) {
	t.Helper()
	total := 0
	for _, fan := range result.Fans {
		total += fan.Points * fan.Count
	}
	if total != result.Total || result.NonFlower+result.Flower != result.Total {
		t.Fatalf("inconsistent total %+v", result)
	}
}

func TestAll81FanTypes(t *testing.T) {
	cases := loadFanCases(t)
	if len(cases) != 81 {
		t.Fatalf("need 81 coverage cases, got %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%02d", tc.ID), func(t *testing.T) {
			got, err := Evaluate(tc.Input)
			if err != nil {
				t.Fatalf("%s: %v", tc.Notation, err)
			}
			checkSum(t, got)
			if fanCount(got, tc.ID) == 0 {
				t.Fatalf("%s: missing fan %d, got %+v", tc.Notation, tc.ID, got)
			}
		})
	}
}

func TestCatalogWMO2014(t *testing.T) {
	catalog := Catalog()
	if len(catalog) != 81 {
		t.Fatal(len(catalog))
	}
	for i, fan := range catalog {
		if fan.ID != i+1 || fan.Name == "" || fan.Points <= 0 || fan.Count != 0 {
			t.Fatalf("bad definition %+v", fan)
		}
	}
	for id, want := range map[int]int{1: 88, 12: 64, 19: 24, 35: 12, 43: 8, 48: 8, 49: 6, 53: 6, 54: 6, 57: 4, 67: 2, 80: 1, 81: 1} {
		if catalog[id-1].Points != want {
			t.Errorf("fan %d points=%d want %d", id, catalog[id-1].Points, want)
		}
	}
}

func TestRejectMalformedInputs(t *testing.T) {
	base := loadFanCases(t)[42].Input
	bad := []Input{{}, {Hand: make([]string, 13), WinTile: "1m"}}
	for _, modify := range []func(*Input){
		func(i *Input) { i.FlowerCount = 9 }, func(i *Input) { i.FlowerCount = -1 },
		func(i *Input) { i.SeatWind = 4 }, func(i *Input) { i.RoundWind = -1 },
		func(i *Input) { i.WinTile = "8z" }, func(i *Input) { i.WinTile = "1h" },
		func(i *Input) { i.Melds[0].Concealed = true },
		func(i *Input) { i.Melds[0].Tiles = []string{"1m", "2m", "4m"} },
		func(i *Input) { i.Melds[0].Kind = "wildcard" },
		func(i *Input) { i.Melds[0].FromSeat = 4 },
		func(i *Input) { i.Hand = []string{"8s", "8s", "2z", "2z"} },
	} {
		b, _ := json.Marshal(base)
		var c Input
		_ = json.Unmarshal(b, &c)
		modify(&c)
		bad = append(bad, c)
	}
	for i, in := range bad {
		if _, err := Evaluate(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d err=%v", i, err)
		}
	}
	base.Hand = []string{"1s", "4s", "2z", "2z"}
	if _, err := Evaluate(base); !errors.Is(err, ErrNotWinning) {
		t.Fatalf("nonwinning err=%v", err)
	}
}

func cloneInput(in Input) Input {
	b, _ := json.Marshal(in)
	var out Input
	_ = json.Unmarshal(b, &out)
	return out
}

func TestFlowerThresholdIsNotEvaluatorsDecision(t *testing.T) {
	cases := loadFanCases(t)
	seven := cloneInput(cases[52].Input)
	seven.Hand = []string{"5p"}
	seven.WinTile = "5p"
	for _, nonFlower := range []int{7, 8} {
		in := cloneInput(seven)
		if nonFlower == 8 {
			in = cloneInput(cases[42].Input)
		}
		for _, flowers := range []int{0, 1, 2, 8} {
			in.FlowerCount = flowers
			got, err := Evaluate(in)
			if err != nil {
				t.Fatal(err)
			}
			if got.NonFlower != nonFlower || got.Flower != flowers || got.Total != nonFlower+flowers {
				t.Fatalf("nonflower=%d flower=%d: %+v", nonFlower, flowers, got)
			}
			checkSum(t, got)
		}
	}
}

func TestWMO2014PrimarySourceExamples(t *testing.T) {
	cases := loadFanCases(t)
	// The pinned primary book expressly allows Fully Concealed Hand (4),
	// rather than Self Drawn (1), with these special hands.
	for _, id := range []int{6, 7, 12, 19, 20, 34} {
		in := cloneInput(cases[id-1].Input)
		in.SelfDraw = true
		got, err := Evaluate(in)
		if err != nil {
			t.Fatal(err)
		}
		if fanCount(got, 56) != 1 || fanCount(got, 80) != 0 || fanCount(got, 62) != 0 {
			t.Errorf("special fan %d self draw: %+v", id, got)
		}
	}
	for _, tc := range []struct {
		id    int
		total int
	}{{7, 92}, {19, 28}, {20, 28}, {34, 28}} {
		in := cloneInput(cases[tc.id-1].Input)
		in.SelfDraw = true
		got, err := Evaluate(in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Total != tc.total {
			t.Errorf("special %d: got %d want %d", tc.id, got.Total, tc.total)
		}
	}
	// Appendix fan 4 example: nine gates winning 9, pure straight, tile hog,
	// and fully concealed = 88 + 16 + 2 + 4.
	nine := cloneInput(cases[3].Input)
	nine.WinTile = "9m"
	got, err := Evaluate(nine)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 110 {
		t.Errorf("nine gates = %+v; want 110", got)
	}
	// Appendix fan 3 expressly permits Half Flush with All Green.
	green, err := Evaluate(cases[2].Input)
	if err != nil {
		t.Fatal(err)
	}
	if fanCount(green, 50) != 1 {
		t.Errorf("all green half flush missing: %+v", green)
	}
	// Appendix fan 8 includes an example with two Double Pungs.
	term, err := Evaluate(cases[7].Input)
	if err != nil {
		t.Fatal(err)
	}
	if fanCount(term, 65) != 2 || term.Total != 74 {
		t.Errorf("all terminals double pungs: %+v", term)
	}
	// WMO2014 fan 48 = 8. One exposed + one concealed is fan 57 plus 67 = 6.
	two := cloneInput(cases[47].Input)
	got, err = Evaluate(two)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 10 || fanCount(got, 48) != 1 || fanCount(got, 66) != 0 {
		t.Errorf("two concealed kongs: %+v", got)
	}
	two.Melds[1].Concealed = false
	two.Melds[1].FromSeat = 1
	got, err = Evaluate(two)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 8 || fanCount(got, 57) != 1 || fanCount(got, 67) != 1 || fanCount(got, 74) != 0 {
		t.Errorf("mixed kongs: %+v", got)
	}
}

func TestExclusionsAndConsistentDecomposition(t *testing.T) {
	cases := loadFanCases(t)
	checks := []struct {
		id        int
		forbidden []int
	}{
		{1, []int{38, 49, 60, 61, 73}},
		{2, []int{54, 59}},
		{6, []int{19, 22, 62, 79}},
		{7, []int{52, 62, 79}},
		{8, []int{18, 49, 55, 73, 76}},
		{13, []int{19, 22, 63, 69, 72, 76}},
		{14, []int{23, 24, 64, 69}},
		{15, []int{23, 24, 49}},
		{16, []int{30, 71, 72}},
		{19, []int{62, 63, 69, 70, 71, 72, 79}},
		{20, []int{34, 52, 62}},
		{21, []int{49, 68, 76}},
		{23, []int{24, 69}},
		{29, []int{63, 70, 72, 76}},
		{40, []int{75}},
		{44, []int{80}},
		{46, []int{80}},
		{48, []int{66, 67}},
		{53, []int{79}},
		{54, []int{59}},
		{56, []int{62, 80}},
		{63, []int{76}},
		{68, []int{76}},
	}
	for _, tc := range checks {
		got, err := Evaluate(cases[tc.id-1].Input)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range tc.forbidden {
			if fanCount(got, id) != 0 {
				t.Errorf("fan %d has excluded %d: %+v", tc.id, id, got)
			}
		}
	}
	// Robbing an added kong excludes the four-point Last Tile award.
	rob := cloneInput(cases[46].Input)
	rob.FourthTile = true
	got, err := Evaluate(rob)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 8 || fanCount(got, 58) != 0 {
		t.Errorf("robbing kong: %+v", got)
	}
	// The same wind pung can be both seat and round wind, but not a third
	// one-point terminal/honor pung.
	wind := cloneInput(cases[59].Input)
	wind.SeatWind = 0
	got, err = Evaluate(wind)
	if err != nil {
		t.Fatal(err)
	}
	if fanCount(got, 60) != 1 || fanCount(got, 61) != 1 || fanCount(got, 73) != 0 {
		t.Errorf("double wind: %+v", got)
	}
	// Four identical tiles count as two pairs, with one Tile Hog.
	pairs := cloneInput(cases[63].Input)
	got, err = Evaluate(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if fanCount(got, 19) != 1 || fanCount(got, 64) != 1 {
		t.Errorf("seven pairs with four identical: %+v", got)
	}
}

func TestOMMCR1ConcealedKongAdjudication(t *testing.T) {
	cases := loadFanCases(t)
	for _, baseID := range []int{5, 17} {
		for hidden := 0; hidden <= len(cases[baseID-1].Input.Melds); hidden++ {
			in := cloneInput(cases[baseID-1].Input)
			for i := 0; i < hidden; i++ {
				in.Melds[i].Concealed = true
				in.Melds[i].FromSeat = 0
			}
			got, err := Evaluate(in)
			if err != nil {
				t.Fatal(err)
			}
			if fanCount(got, baseID) != 1 {
				t.Fatalf("lost %d-kong award: %+v", baseID, got)
			}
			wantSingle, wantDouble := 0, 0
			if hidden == 1 {
				wantSingle = 1
			}
			if hidden >= 2 {
				wantDouble = 1
			}
			if fanCount(got, 67) != wantSingle || fanCount(got, 48) != wantDouble || (hidden >= 2 && fanCount(got, 66) != 0) {
				t.Errorf("fan %d hidden=%d: %+v", baseID, hidden, got)
			}
			if hidden == 3 && fanCount(got, 33) != 1 {
				t.Errorf("three concealed pungs missing: %+v", got)
			}
			if hidden == 4 && fanCount(got, 12) != 1 {
				t.Errorf("four concealed pungs missing: %+v", got)
			}
			checkSum(t, got)
		}
	}
}

func TestConcurrentDeterministicEvaluation(t *testing.T) {
	for _, tc := range loadFanCases(t) {
		tc := tc
		t.Run(fmt.Sprint(tc.ID), func(t *testing.T) {
			t.Parallel()
			want, err := Evaluate(tc.Input)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(want)
			for repeat := 0; repeat < 20; repeat++ {
				in := cloneInput(tc.Input)
				for lo, hi := 0, len(in.Hand)-1; lo < hi; lo, hi = lo+1, hi-1 {
					in.Hand[lo], in.Hand[hi] = in.Hand[hi], in.Hand[lo]
				}
				got, err := Evaluate(in)
				if err != nil {
					t.Fatal(err)
				}
				gotJSON, _ := json.Marshal(got)
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("hand order changed score: %s != %s", gotJSON, wantJSON)
				}
			}
		})
	}
}

func BenchmarkEvaluate(b *testing.B) {
	cases := loadFanCases(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Evaluate(cases[i%len(cases)].Input); err != nil {
			b.Fatal(err)
		}
	}
}

// These hands are taken from the pinned upstream unit_test.cpp, where they
// exercise historic decomposition, waiting-shape and exclusion regressions.
// They are structural/robustness tests, not independent WMO score oracles.
func TestUpstreamRegressionHands(t *testing.T) {
	data, err := os.ReadFile("testdata/upstream_hands.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		SourceLine int    `json:"source_line"`
		Notation   string `json:"notation"`
		Input      Input  `json:"input"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 150 {
		t.Fatalf("missing upstream regression corpus: %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.SourceLine), func(t *testing.T) {
			got, err := Evaluate(tc.Input)
			if err != nil {
				t.Fatalf("%s: %v", tc.Notation, err)
			}
			checkSum(t, got)
			if got.Total <= 0 {
				t.Fatalf("invalid score: %+v", got)
			}
		})
	}
}

func TestShantenAllWinningForms(t *testing.T) {
	for _, tc := range loadFanCases(t) {
		distance, useful, err := Shanten(tc.Input.Hand, len(tc.Input.Melds))
		if err != nil {
			t.Fatalf("fan %d: %v", tc.ID, err)
		}
		if distance != 0 {
			t.Errorf("fan %d ready hand distance=%d", tc.ID, distance)
		}
		found := false
		for _, tile := range useful {
			if tile == tc.Input.WinTile {
				found = true
			}
		}
		if !found {
			t.Errorf("fan %d winning tile %s missing from %v", tc.ID, tc.Input.WinTile, useful)
		}
	}
	orphans := loadFanCases(t)[6].Input
	distance, useful, err := Shanten(orphans.Hand, 0)
	if err != nil || distance != 0 || len(useful) != 13 {
		t.Fatalf("thirteen-sided orphans: %d %v %v", distance, useful, err)
	}
	// A declared set makes seven-pairs, orphans and unconnected impossible.
	pairs := []string{"1m", "1m", "2p", "2p", "3s", "3s", "4z", "4z", "5z", "5z"}
	distance, _, err = Shanten(pairs, 1)
	if err != nil || distance <= 0 {
		t.Fatalf("melded pairs incorrectly ready: %d %v", distance, err)
	}
	if _, _, err = Shanten(pairs, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad length accepted: %v", err)
	}
	bad := append([]string(nil), orphans.Hand...)
	bad[0] = "1h"
	if _, _, err = Shanten(bad, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("flower accepted: %v", err)
	}
}

func TestWinningDecompositionMatchesChosenMaximum(t *testing.T) {
	cases := loadFanCases(t)
	for _, tc := range cases {
		got, err := Evaluate(tc.Input)
		if err != nil {
			t.Fatal(err)
		}
		wantTiles := map[string]int{}
		gotTiles := map[string]int{}
		for _, tile := range tc.Input.Hand {
			wantTiles[tile]++
		}
		wantTiles[tc.Input.WinTile]++
		for _, meld := range tc.Input.Melds {
			for _, tile := range meld.Tiles {
				wantTiles[tile]++
			}
		}
		for id, group := range got.Decomposition {
			if group.ID != id || group.Kind == "" {
				t.Errorf("fan %d bad group %+v", tc.ID, group)
			}
			for _, tile := range group.Tiles {
				gotTiles[tile]++
			}
		}
		if !reflect.DeepEqual(wantTiles, gotTiles) {
			t.Errorf("fan %d decomposition invents or omits tiles: %+v", tc.ID, got)
		}
		if len(got.Explanations) != len(got.Fans) {
			t.Errorf("fan %d missing explanation entries", tc.ID)
		}
		for _, exp := range got.Explanations {
			for _, excluded := range exp.ExcludedFanIDs {
				if fanCount(got, excluded) > 0 {
					t.Errorf("claimed exclusion of awarded fan %d", excluded)
				}
			}
		}
	}
	// Both hands can be interpreted as seven pairs; the actual maximum is a
	// regular chow decomposition. Never attach the first arbitrary split.
	for _, id := range []int{13, 14} {
		got, err := Evaluate(cases[id-1].Input)
		if err != nil {
			t.Fatal(err)
		}
		if got.WinningForm != "regular" || len(got.Decomposition) != 5 {
			t.Fatalf("fan %d wrong selected form %+v", id, got)
		}
		chows := 0
		for _, g := range got.Decomposition {
			if g.Kind == "chi" {
				chows++
			}
		}
		if chows != 4 {
			t.Errorf("fan %d did not retain four-chow maximum: %+v", id, got)
		}
	}
	seven, err := Evaluate(cases[18].Input)
	if err != nil {
		t.Fatal(err)
	}
	if seven.WinningForm != "seven_pairs" || len(seven.Decomposition) != 7 {
		t.Fatalf("seven pairs split %+v", seven)
	}
	knitted, err := Evaluate(cases[34].Input)
	if err != nil {
		t.Fatal(err)
	}
	if knitted.WinningForm != "knitted_straight" || len(knitted.Decomposition) != 3 || len(knitted.Decomposition[0].Tiles) != 9 {
		t.Fatalf("knitted split %+v", knitted)
	}
}

func TestFourHonorCopiesCannotEnterNineGates(t *testing.T) {
	in := Input{Hand: []string{"1z", "1z", "1z", "1m", "2m", "4m", "5m", "6m", "7p", "8p", "9p", "2s", "3s"}, WinTile: "1z"}
	if _, err := Evaluate(in); !errors.Is(err, ErrNotWinning) {
		t.Fatalf("nonwinning honor hand: %v", err)
	}
}
