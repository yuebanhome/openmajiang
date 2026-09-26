package mcr

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func config(format string) rulesdk.Config {
	return rulesdk.Config{Format: format, Profile: "om-mcr-1", Participants: []rulesdk.Participant{{ID: "A", Name: "甲", Kind: "human"}, {ID: "B", Name: "乙", Kind: "bot"}, {ID: "C", Name: "丙", Kind: "bot"}, {ID: "D", Name: "丁", Kind: "human"}}}
}
func initialized(t *testing.T, format string, seed byte) rulesdk.Snapshot {
	t.Helper()
	raw, err := (Rule{}).Init(config(format), bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func encode(t *testing.T, s *State) rulesdk.Snapshot {
	t.Helper()
	raw, err := marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func state(t *testing.T, raw rulesdk.Snapshot) *State {
	t.Helper()
	s, err := load(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func apply(t *testing.T, raw rulesdk.Snapshot, in rulesdk.Input) rulesdk.Snapshot {
	t.Helper()
	next, err := (Rule{}).Apply(raw, in)
	if err != nil {
		t.Fatalf("%s: %v", in.Type, err)
	}
	if _, err = load(next.State); err != nil {
		t.Fatalf("post-transition invariant: %v", err)
	}
	return next.State
}
func choose(t *testing.T, options []rulesdk.Option, typ string) rulesdk.Option {
	t.Helper()
	for _, o := range options {
		if o.Type == typ {
			return o
		}
	}
	t.Fatalf("no %s in %+v", typ, options)
	return rulesdk.Option{}
}

func TestInitialDealDeterminismAndConservation(t *testing.T) {
	r := Rule{}
	for seed := byte(0); seed < 20; seed++ {
		a := initialized(t, "standard_16", seed)
		b := initialized(t, "standard_16", seed)
		if !bytes.Equal(a, b) {
			t.Fatal("same entropy changed initial state")
		}
		s := state(t, a)
		if n := len(s.Seats[s.Dealer].Flowers); n > 0 {
			if s.DrawnID != s.Wall[144-n].ID || s.DrawSource != "flower_replacement" {
				t.Fatal("dealer winning tile did not follow final initial flower replacement")
			}
		}
		for seat, p := range s.Seats {
			want := 13
			if seat == s.Dealer {
				want = 14
			}
			if len(p.Hand) != want {
				t.Fatalf("seat %d has %d", seat, len(p.Hand))
			}
			for _, tile := range p.Hand {
				if isFlower(tile.Kind) {
					t.Fatal("initial flower not replaced")
				}
			}
		}
		flow, err := r.Inspect(a)
		if err != nil || len(flow.Decisions) != 1 {
			t.Fatalf("initial flow %v %v", flow, err)
		}
	}
	if _, err := r.Init(config("practice_1"), []byte("weak")); err == nil {
		t.Fatal("accepted insufficient entropy")
	}
}

// fixture rebuilds a complete 144-tile catalog while choosing only the hands
// relevant to a rule scenario. Unassigned physical tiles remain in the wall.
func fixture(t *testing.T, hands ...string) *State {
	t.Helper()
	s := state(t, initialized(t, "practice_1", 99))
	s.InitialOrder = []string{"A", "B", "C", "D"}
	s.Seats = make([]Seat, 4)
	s.Head = 0
	s.Tail = 144
	s.Dealer = 0
	s.Active = 0
	s.Step = 1
	s.Phase = "self"
	s.DrawnID = ""
	s.DrawSource = "normal"
	s.CanKong = true
	s.Pending = nil
	s.Discards = []Discard{}
	s.Result = nil
	pool := append([]Tile(nil), s.Wall...)
	used := map[string]bool{}
	for seat := 0; seat < 4; seat++ {
		s.Seats[seat] = Seat{ParticipantID: string(rune('A' + seat)), Hand: []Tile{}, Melds: []Meld{}, Flowers: []Tile{}}
		if seat >= len(hands) {
			continue
		}
		for _, kind := range strings.Fields(hands[seat]) {
			found := false
			for _, tile := range pool {
				if tile.Kind == kind && !used[tile.ID] {
					used[tile.ID] = true
					s.Seats[seat].Hand = append(s.Seats[seat].Hand, tile)
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("fixture has too many %s", kind)
			}
		}
		sortHand(s.Seats[seat].Hand)
	}
	s.Wall = nil
	for _, tile := range pool {
		if !used[tile.ID] {
			s.Wall = append(s.Wall, tile)
		}
	}
	s.Tail = len(s.Wall)
	for _, tile := range pool {
		if used[tile.ID] {
			s.Wall = append(s.Wall, tile)
		}
	}
	if len(s.Seats[0].Hand) > 0 {
		s.DrawnID = s.Seats[0].Hand[len(s.Seats[0].Hand)-1].ID
	}
	return s
}
func tileOf(t *testing.T, s *State, seat int, kind string) Tile {
	t.Helper()
	for _, tile := range s.Seats[seat].Hand {
		if tile.Kind == kind {
			return tile
		}
	}
	t.Fatalf("missing %s", kind)
	return Tile{}
}
func discard(t *testing.T, s *State, seat int, kind string) *State {
	t.Helper()
	s.Active = seat
	s.Phase = "self"
	tile := tileOf(t, s, seat, kind)
	o := s.option(seat, "discard", tile, nil)
	raw := apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: s.Seats[seat].ParticipantID, OptionID: o.ID})
	return state(t, raw)
}

func TestReactionPriorityAndFollowingSameTile(t *testing.T) {
	s := fixture(t, "3m", "1m 2m", "3m 3m 3m 5p 5p 5p 5p")
	s = discard(t, s, 0, "3m")
	chi := choose(t, s.reactionOptions(1), "chi")
	pon := choose(t, s.reactionOptions(2), "pon")
	next := apply(t, encode(t, s), rulesdk.Input{Type: "resolve", Choices: map[string]string{"B": chi.ID, "C": pon.ID}})
	s = state(t, next)
	if s.Active != 2 || s.Seats[2].Melds[0].Type != "pon" || !s.Discards[0].Claimed {
		t.Fatal("pung did not outrank chow")
	}
	follow := false
	for _, o := range s.selfOptions() {
		if o.Type == "kan_closed" || o.Type == "kan_added" {
			t.Fatal("kong immediately after pung")
		}
		if o.Type == "discard" && o.Kind == "3m" {
			follow = true
		}
	}
	if !follow {
		t.Fatal("following same tile was prohibited")
	}
}

func TestNearestSingleWinnerAndExactPayment(t *testing.T) {
	s := fixture(t, "1z", "1m 2m 3m 4m 5m 6m 7m 8m 9m 1p 1p 1p 1z", "1s 2s 3s 4s 5s 6s 7s 8s 9s 2p 2p 2p 1z")
	s = discard(t, s, 0, "1z")
	a := choose(t, s.reactionOptions(1), "hu")
	b := choose(t, s.reactionOptions(2), "hu")
	before := encode(t, s)
	choices := map[string]string{"C": b.ID, "B": a.ID}
	after := apply(t, before, rulesdk.Input{Type: "resolve", Choices: choices})
	s = state(t, after)
	if s.Result.Winner != 1 || s.Phase != "ended" {
		t.Fatal("nearest player must be sole winner")
	}
	fan := s.Result.Total
	want := map[string]int{"A": -(8 + fan), "B": 24 + fan, "C": -8, "D": -8}
	sum := 0
	for _, sc := range s.Result.Scores {
		if sc.Delta != want[sc.ParticipantID] {
			t.Fatalf("wrong payment %+v", sc)
		}
		sum += sc.Delta
	}
	if sum != 0 {
		t.Fatal("nonzero transfer")
	}
	again := apply(t, before, rulesdk.Input{Type: "resolve", Choices: map[string]string{"B": a.ID, "C": b.ID}})
	if !bytes.Equal(after, again) {
		t.Fatal("choice map order changed result")
	}
	if _, err := (Rule{}).Apply(after, rulesdk.Input{Type: "resolve", Choices: choices}); err == nil {
		t.Fatal("settled window reapplied")
	}
	assertSpectator(t, after)
}

func TestRobAddedKongLeavesPungAndNoReplacement(t *testing.T) {
	s := fixture(t, "5m 5m 5m 5m", "1p 2p 3p 4p 5p 6p 7p 8p 9p 1z 1z 4m 6m")
	pung := append([]Tile(nil), s.Seats[0].Hand[:3]...)
	s.Seats[0].Hand = s.Seats[0].Hand[3:]
	s.Seats[0].Melds = append(s.Seats[0].Melds, Meld{Type: "pon", Tiles: pung, FromSeat: 2})
	s.DrawnID = s.Seats[0].Hand[0].ID
	o := choose(t, s.selfOptions(), "kan_added")
	remaining := s.remaining()
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
	if s.Seats[0].Melds[0].Type != "pon" || s.remaining() != remaining {
		t.Fatal("added kong committed before robbery window")
	}
	hu := choose(t, s.reactionOptions(1), "hu")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve", Choices: map[string]string{"B": hu.ID}}))
	if s.Result.Method != "rob_kong" || s.Seats[0].Melds[0].Type != "pon" || len(s.Seats[0].Melds[0].Tiles) != 3 || s.remaining() != remaining {
		t.Fatal("robbed kong changed pung or drew a replacement")
	}
	if len(s.Seats[0].Hand) != 0 {
		t.Fatal("robbed physical tile remained with declarer")
	}
}

func TestSelfDrawPayment(t *testing.T) {
	s := fixture(t, "1m 1m 1m 2m 2m 2m 3m 3m 3m 4m 4m 4m 5m 5m")
	s.DrawnID = tileOf(t, s, 0, "5m").ID
	o := choose(t, s.selfOptions(), "hu")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
	for _, sc := range s.Result.Scores {
		want := -(8 + s.Result.Total)
		if sc.ParticipantID == "A" {
			want *= -3
		}
		if sc.Delta != want {
			t.Fatalf("wrong self draw transfer %+v", sc)
		}
	}
	assertSpectator(t, encode(t, s))
}

func takeWallKind(t *testing.T, s *State, kind string) Tile {
	t.Helper()
	for i := s.Head; i < s.Tail; i++ {
		if s.Wall[i].Kind == kind {
			s.Wall[s.Head], s.Wall[i] = s.Wall[i], s.Wall[s.Head]
			tile := s.Wall[s.Head]
			s.Head++
			return tile
		}
	}
	t.Fatalf("wall missing %s", kind)
	return Tile{}
}
func makeMeld(t *testing.T, s *State, seat int, typ, kinds string, concealed bool) {
	t.Helper()
	tiles := []Tile{}
	for _, kind := range strings.Fields(kinds) {
		tile := tileOf(t, s, seat, kind)
		hand, removed, err := removeTile(s.Seats[seat].Hand, tile.ID)
		if err != nil {
			t.Fatal(err)
		}
		s.Seats[seat].Hand = hand
		tiles = append(tiles, removed)
	}
	from := (seat + 3) % 4
	if concealed {
		from = seat
	}
	s.Seats[seat].Melds = append(s.Seats[seat].Melds, Meld{Type: typ, Tiles: tiles, FromSeat: from, Concealed: concealed})
}

func TestEightPointQualificationExcludesFlowers(t *testing.T) {
	for _, flowerCount := range []int{0, 1, 2} {
		s := fixture(t, "5p", "1m 2m 3m 4p 5p 6p 8s 8s 8s 2s 3s 4s 5p")
		makeMeld(t, s, 1, "chi", "1m 2m 3m", false)
		makeMeld(t, s, 1, "chi", "4p 5p 6p", false)
		makeMeld(t, s, 1, "pon", "8s 8s 8s", false)
		makeMeld(t, s, 1, "chi", "2s 3s 4s", false)
		for n := 1; n <= flowerCount; n++ {
			s.Seats[1].Flowers = append(s.Seats[1].Flowers, takeWallKind(t, s, fmt.Sprintf("h%d", n)))
		}
		s = discard(t, s, 0, "5p")
		result, qualified := s.evaluate(1, s.Pending.Tile, false, false)
		if qualified || result.NonFlower != 7 || result.Total != 7+flowerCount {
			t.Fatalf("seven plus flowers qualified: %+v %v", result, qualified)
		}
		for _, o := range s.reactionOptions(1) {
			if o.Type == "hu" {
				t.Fatal("illegal under-eight hu option")
			}
		}
	}
	s := fixture(t, "2s", "1m 2m 3m 4p 5p 6p 8s 8s 8s 3s 4s 2z 2z")
	makeMeld(t, s, 1, "chi", "1m 2m 3m", false)
	makeMeld(t, s, 1, "chi", "4p 5p 6p", false)
	makeMeld(t, s, 1, "pon", "8s 8s 8s", false)
	for _, kind := range []string{"h1", "h2"} {
		s.Seats[1].Flowers = append(s.Seats[1].Flowers, takeWallKind(t, s, kind))
	}
	// A prior discard of the same winning kind imposes no furiten in MCR.
	s.Discards = append(s.Discards, Discard{ID: "prior-2s", Seat: 1, Tile: takeWallKind(t, s, "2s")})
	s = discard(t, s, 0, "2s")
	o := choose(t, s.reactionOptions(1), "hu")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve", Choices: map[string]string{"B": o.ID}}))
	if s.Result.NonFlower != 8 || s.Result.Flower != 2 {
		t.Fatalf("eight point example changed: %+v", s.Result)
	}
	want := map[string]int{"A": -18, "B": 34, "C": -8, "D": -8}
	for _, sc := range s.Result.Scores {
		if sc.Delta != want[sc.ParticipantID] {
			t.Fatalf("8+2 flower transfer: %+v", sc)
		}
	}
}

func TestAddedKongCompletesOnlyAfterAllPass(t *testing.T) {
	s := fixture(t, "5m 5m 5m 5m")
	makeMeld(t, s, 0, "pon", "5m 5m 5m", false)
	s.DrawnID = s.Seats[0].Hand[0].ID
	o := choose(t, s.selfOptions(), "kan_added")
	remaining := s.remaining()
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
	if s.remaining() != remaining {
		t.Fatal("replacement before robbery arbitration")
	}
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve"}))
	if s.Seats[0].Melds[0].Type != "kan_added" || s.remaining() != remaining-1 || s.Active != 0 || s.DrawSource != "kong_replacement" {
		t.Fatal("added kong did not commit")
	}
}

func TestReactionTimeoutPreservesRecordedChoices(t *testing.T) {
	s := fixture(t, "3m", "3m 3m")
	s = discard(t, s, 0, "3m")
	o := choose(t, s.reactionOptions(1), "pon")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "timeout", Choices: map[string]string{"B": o.ID}}))
	if s.Active != 1 || len(s.Seats[1].Melds) != 1 {
		t.Fatal("timeout discarded previously recorded choice")
	}
}

func TestFourKongsRemainPlayable(t *testing.T) {
	s := fixture(t, "1m 1m 1m 1m 2m 2m 2m 2m 3m 3m 3m 3m 4m 4m 4m 4m 5m 5m")
	for n := 1; n <= 4; n++ {
		kind := fmt.Sprintf("%dm", n)
		makeMeld(t, s, 0, "kan_closed", strings.TrimSpace(strings.Repeat(kind+" ", 4)), true)
	}
	s.DrawnID = s.Seats[0].Hand[0].ID
	hu := choose(t, s.selfOptions(), "hu")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: hu.ID}))
	if s.Result.NonFlower < 88 {
		t.Fatal("four kong hand not scored")
	}
}

func TestFlowersRemainPrivateCanBeDiscardedAndChain(t *testing.T) {
	s := fixture(t, "h1 1m")
	// Make tail draws h2, h3, 5m without changing the identity catalog.
	for offset, kind := range []string{"h2", "h3", "5m"} {
		target := s.Tail - 1 - offset
		index := -1
		for i := s.Head; i < s.Tail; i++ {
			if s.Wall[i].Kind == kind {
				index = i
				break
			}
		}
		if index < 0 {
			t.Fatal("replacement missing")
		}
		s.Wall[target], s.Wall[index] = s.Wall[index], s.Wall[target]
	}
	for _, kind := range []string{"h1", "h2", "h3"} {
		assertSpectator(t, encode(t, s))
		o := choose(t, s.selfOptions(), "replace_flower")
		if o.Kind != kind {
			t.Fatalf("wanted %s, got %s", kind, o.Kind)
		}
		s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
	}
	if len(s.Seats[0].Flowers) != 3 || s.DrawSource != "flower_replacement" {
		t.Fatal("flower chain context lost")
	}
	if tile, _ := findTile(s.Seats[0].Hand, s.DrawnID); tile.Kind != "5m" {
		t.Fatal("wrong replacement")
	}
	s = fixture(t, "h4 1m")
	s = discard(t, s, 0, "h4")
	for seat := 1; seat < 4; seat++ {
		opts := s.reactionOptions(seat)
		if len(opts) != 1 || opts[0].Type != "pass" {
			t.Fatal("discarded flower was claimable")
		}
	}
	pub, err := (Rule{}).Project(encode(t, s), rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pub, []byte(`"kind":"h4"`)) {
		t.Fatal("discarded flower missing from public view")
	}
}

func TestConcealedKongPrivateUntilHandEnds(t *testing.T) {
	s := fixture(t, "7m 7m 7m 7m 1p")
	o := choose(t, s.selfOptions(), "kan_closed")
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
	if s.Seats[0].Melds[0].Type != "kan_closed" || s.DrawSource != "kong_replacement" {
		t.Fatal("concealed kong failed")
	}
	view := func(id string) ParticipantView {
		raw, err := (Rule{}).Project(encode(t, s), rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: id})
		if err != nil {
			t.Fatal(err)
		}
		var v ParticipantView
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if len(view("B").Seats[0].Melds[0].Tiles) != 0 {
		t.Fatal("concealed kong leaked during hand")
	}
	if len(view("A").Seats[0].Melds[0].Tiles) != 4 {
		t.Fatal("owner cannot see kong")
	}
	assertSpectator(t, encode(t, s))
	s.finishDraw()
	if len(view("B").Seats[0].Melds[0].Tiles) != 4 {
		t.Fatal("participant end-of-hand verification missing")
	}
	assertSpectator(t, encode(t, s))
}

func TestLastWallOnlyWinOrDiscardThenExhaustion(t *testing.T) {
	s := fixture(t, "2m 2m 2m 2m h1", "3m 3m")
	for s.remaining() > 1 {
		tile := s.Wall[s.Head]
		s.Head++
		s.Discards = append(s.Discards, Discard{ID: fmt.Sprintf("old%d", len(s.Discards)), Seat: 3, Tile: tile})
	}
	if err := s.draw(0, "normal"); err != nil {
		t.Fatal(err)
	}
	for _, o := range s.selfOptions() {
		if o.Type != "hu" && o.Type != "discard" {
			t.Fatalf("last draw action %s", o.Type)
		}
	}
	s = discard(t, s, 0, "2m")
	for seat := 1; seat < 4; seat++ {
		for _, o := range s.reactionOptions(seat) {
			if o.Type != "hu" && o.Type != "pass" {
				t.Fatal("last discard was claimable")
			}
		}
	}
	s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve"}))
	if s.Result == nil || s.Result.Method != "exhaustive_draw" {
		t.Fatal("missing final discard window/draw settlement")
	}
}

func assertSpectator(t *testing.T, raw rulesdk.Snapshot) {
	t.Helper()
	view, err := (Rule{}).Project(raw, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly, ParticipantID: "A"})
	if err != nil {
		t.Fatal(err)
	}
	var root any
	if err = json.Unmarshal(view, &root); err != nil {
		t.Fatal(err)
	}
	var walk func(any, string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				switch k {
				case "tile_id", "hand", "wall", "seed", "flowers", "melds", "fan_items", "winning_hand", "winning_tile", "drawn_tile_id", "legal_actions":
					t.Fatalf("spectator leaked %s at %s", k, path)
				case "kind":
					if !strings.HasPrefix(path, "$.discards[") {
						t.Fatalf("non-discard face at %s", path)
					}
				}
				walk(v, path+"."+k)
			}
		case []any:
			for i, v := range x {
				walk(v, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(root, "$")
}

func TestCompleteFormatsReplayAndRotations(t *testing.T) {
	for _, format := range []string{"practice_1", "practice_4", "standard_16"} {
		t.Run(format, func(t *testing.T) { simulate(t, format, 7) })
	}
}
func simulate(t *testing.T, format string, seed int) {
	t.Helper()
	r := Rule{}
	raw := initialized(t, format, byte(seed))
	rng := rand.New(rand.NewSource(int64(seed)))
	steps := 0
	hands := map[int]bool{}
	for {
		steps++
		if steps > 10000 {
			t.Fatal("match did not terminate")
		}
		s := state(t, raw)
		hands[s.HandIndex] = true
		for seat, original := range rotations[(s.HandIndex-1)/4] {
			if s.Seats[seat].ParticipantID != s.InitialOrder[original] {
				t.Fatal("wrong circle seat rotation")
			}
		}
		if s.Dealer != (s.HandIndex-1)%4 {
			t.Fatal("wrong dealer")
		}
		flow, err := r.Inspect(raw)
		if err != nil {
			t.Fatal(err)
		}
		if steps%43 == 0 || flow.HandEnded {
			assertSpectator(t, raw)
		}
		if flow.MatchEnded {
			if len(hands) != s.handLimit() {
				t.Fatal("wrong hand count")
			}
			break
		}
		in := rulesdk.Input{}
		switch flow.WindowKind {
		case "intermission":
			in.Type = "next_hand"
		case "reaction":
			in.Type = "resolve"
			in.Choices = map[string]string{}
			for _, d := range flow.Decisions {
				o := d.Options[rng.Intn(len(d.Options))]
				for _, candidate := range d.Options {
					if candidate.Type == "hu" {
						o = candidate
						break
					}
				}
				in.Choices[d.ParticipantID] = o.ID
			}
		case "self":
			d := flow.Decisions[0]
			o := d.Options[rng.Intn(len(d.Options))]
			for _, candidate := range d.Options {
				if candidate.Type == "hu" || candidate.Type == "replace_flower" {
					o = candidate
					break
				}
			}
			in = rulesdk.Input{Type: "action", ParticipantID: d.ParticipantID, OptionID: o.ID}
		default:
			t.Fatalf("bad phase %s", flow.WindowKind)
		}
		before := append([]byte(nil), raw...)
		next := apply(t, raw, in)
		replay := apply(t, before, in)
		if !bytes.Equal(next, replay) {
			t.Fatal("same state/input yielded different state")
		}
		raw = next
	}
}

func TestSoak(t *testing.T) {
	value := os.Getenv("MCR_SOAK_MATCHES")
	if value == "" {
		t.Skip("set MCR_SOAK_MATCHES for the full seeded simulation gate")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		t.Fatal("invalid MCR_SOAK_MATCHES")
	}
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		soakMatch(t, i, counts)
	}
	t.Logf("seeded matches=%d hands=%d actions=%v", n, n*16, counts)
}

// The release soak runs each transition through the same legal-option builders
// and transition functions as Rule.Apply, validates all physical ownership on
// every step, and crosses the serialized SDK boundary every 127 transitions.
// This keeps a 10,000-hand gate practical without relaxing rules or invariants.
func soakMatch(t *testing.T, seed int, counts map[string]int) {
	t.Helper()
	entropy := sha256.Sum256([]byte(fmt.Sprintf("openmajiang-soak-%d", seed)))
	raw, err := (Rule{}).Init(config("standard_16"), entropy[:])
	if err != nil {
		t.Fatal(err)
	}
	s := state(t, raw)
	rng := rand.New(rand.NewSource(int64(seed)))
	steps := 0
	finishedHands := 0
	for {
		steps++
		if steps > 10000 {
			t.Fatalf("seed %d did not terminate", seed)
		}
		if err = s.validateInventory(); err != nil {
			t.Fatalf("seed %d step %d: %v", seed, steps, err)
		}
		for seat, original := range rotations[(s.HandIndex-1)/4] {
			if s.Seats[seat].ParticipantID != s.InitialOrder[original] {
				t.Fatal("soak seat rotation mismatch")
			}
		}
		if s.Phase == "intermission" || s.Phase == "ended" {
			finishedHands++
			sum := 0
			for _, sc := range s.Result.Scores {
				sum += sc.Delta
			}
			if sum != 0 {
				t.Fatal("soak nonzero payment")
			}
			if s.Result.Method != "exhaustive_draw" && s.Result.NonFlower < 8 {
				t.Fatal("soak under-eight win")
			}
			counts[s.Result.Method]++
			if s.Phase == "ended" {
				if finishedHands != 16 {
					t.Fatal("soak incomplete match")
				}
				assertSpectator(t, encode(t, s))
				return
			}
			s.HandIndex++
			s.Step++
			if err = s.startHand(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var before rulesdk.Snapshot
		if steps%127 == 0 {
			before = encode(t, s)
			assertSpectator(t, before)
		}
		in := rulesdk.Input{}
		if s.Phase == "self" {
			opts := s.selfOptions()
			if len(opts) == 0 {
				t.Fatal("soak empty self options")
			}
			o := opts[rng.Intn(len(opts))]
			for _, candidate := range opts {
				if candidate.Type == "hu" || candidate.Type == "replace_flower" {
					o = candidate
					break
				}
			}
			in = rulesdk.Input{Type: "action", ParticipantID: s.Seats[s.Active].ParticipantID, OptionID: o.ID}
			counts[o.Type]++
			s.Step++
			err = s.applySelf(o)
		} else {
			in = rulesdk.Input{Type: "resolve", Choices: map[string]string{}}
			for offset := 1; offset < 4; offset++ {
				seat := (s.Pending.Seat + offset) % 4
				opts := s.reactionOptions(seat)
				o := opts[rng.Intn(len(opts))]
				for _, candidate := range opts {
					if candidate.Type == "hu" {
						o = candidate
						break
					}
				}
				in.Choices[s.Seats[seat].ParticipantID] = o.ID
			}
			err = s.resolve(in.Choices)
			if err == nil {
				s.Step++
			}
			counts[s.LastAction.Type]++
		}
		if err != nil {
			t.Fatalf("seed %d step %d: %v", seed, steps, err)
		}
		if before != nil {
			transition, err := (Rule{}).Apply(before, in)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(transition.State, encode(t, s)) {
				t.Fatal("soak SDK replay diverged")
			}
		}
	}
}

func TestInvalidAndPrivateAccess(t *testing.T) {
	r := Rule{}
	raw := initialized(t, "practice_1", 3)
	before := append([]byte(nil), raw...)
	if _, err := r.Apply(raw, rulesdk.Input{Type: "action", ParticipantID: "attacker", OptionID: "forged"}); err == nil {
		t.Fatal("forged action accepted")
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("rejected action mutated input")
	}
	if _, err := r.Project(raw, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: "attacker"}); err == nil {
		t.Fatal("nonparticipant got private view")
	}
	if _, err := r.Project(raw, rulesdk.Viewer{Audience: "omniscient", ParticipantID: "A"}); err == nil {
		t.Fatal("unknown audience accepted")
	}
	s := state(t, raw)
	s.Seats[0].Hand = append(s.Seats[0].Hand, s.Seats[0].Hand[0])
	if _, err := r.Inspect(encode(t, s)); err == nil {
		t.Fatal("duplicate tile snapshot accepted")
	}
}

func FuzzSnapshot(f *testing.F) {
	raw, _ := (Rule{}).Init(config("practice_1"), bytes.Repeat([]byte{1}, 32))
	f.Add([]byte(raw))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := Rule{}
		_, _ = r.Inspect(data)
		_, _ = r.Project(data, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
		_, _ = r.Apply(data, rulesdk.Input{Type: "timeout"})
	})
}
