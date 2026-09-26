package mcr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
)

func (r Rule) Inspect(raw rulesdk.Snapshot) (rulesdk.Flow, error) {
	s, err := load(raw)
	if err != nil {
		return rulesdk.Flow{}, err
	}
	f := rulesdk.Flow{Phase: s.Phase, HandIndex: s.HandIndex, WindowID: s.windowID(), WindowKind: s.Phase, HandEnded: s.Phase == "intermission" || s.Phase == "ended", MatchEnded: s.Phase == "ended", Assignments: []rulesdk.Assignment{}, Decisions: []rulesdk.Decision{}, Scores: s.scores()}
	f.PreserveDeadline = s.Phase == "self" && s.LastAction.Type == "replace_flower"
	if f.HandEnded && s.Result != nil {
		f.HandResult = &rulesdk.HandResult{HandIndex: s.HandIndex, Scores: s.Result.Scores, Winners: []rulesdk.Winner{}}
		if s.Result.Winner >= 0 {
			points := s.Result.NonFlower
			f.HandResult.Winners = append(f.HandResult.Winners, rulesdk.Winner{ParticipantID: s.Seats[s.Result.Winner].ParticipantID, SelfDraw: s.Result.Method == "self_draw", NonFlowerPoints: &points})
			if s.Result.Method != "self_draw" {
				f.HandResult.DiscarderID = s.Seats[s.Result.Source].ParticipantID
			}
		}
	}
	if f.MatchEnded {
		for _, standing := range s.Standings {
			ranking := rulesdk.Ranking{ParticipantID: standing.ParticipantID, Rank: standing.Rank, RawScore: standing.Score}
			if standing.StandardPoints != nil {
				ranking.StandardPoints = &rulesdk.Rational{Numerator: standing.StandardPoints.Numerator, Denominator: standing.StandardPoints.Denominator}
			}
			f.Rankings = append(f.Rankings, ranking)
		}
	}
	for i, p := range s.Seats {
		f.Assignments = append(f.Assignments, rulesdk.Assignment{ParticipantID: p.ParticipantID, Seat: i})
	}
	if s.Phase == "self" {
		f.Decisions = append(f.Decisions, s.decision(s.Active, s.selfOptions()))
	}
	if s.Phase == "reaction" {
		for offset := 1; offset < 4; offset++ {
			seat := (s.Pending.Seat + offset) % 4
			f.Decisions = append(f.Decisions, s.decision(seat, s.reactionOptions(seat)))
		}
	}
	return f, nil
}
func (s *State) decision(seat int, options []rulesdk.Option) rulesdk.Decision {
	return rulesdk.Decision{ID: fmt.Sprintf("%s-s%d", s.windowID(), seat), ParticipantID: s.Seats[seat].ParticipantID, Seat: seat, Options: options}
}
func (s *State) option(seat int, typ string, tile Tile, consume []string) rulesdk.Option {
	data, _ := json.Marshal([]any{s.windowID(), seat, typ, tile.ID, consume})
	hash := sha256.Sum256(data)
	return rulesdk.Option{ID: hex.EncodeToString(hash[:12]), Type: typ, TileID: tile.ID, Kind: tile.Kind, Consume: consume}
}
func (s *State) selfOptions() []rulesdk.Option {
	seat := s.Active
	hand := s.Seats[seat].Hand
	opts := []rulesdk.Option{}
	if tile, ok := findTile(hand, s.DrawnID); ok {
		if result, ok := s.evaluate(seat, tile, true, false); ok {
			o := s.option(seat, "hu", tile, nil)
			o.Details = fanSummary(result)
			opts = append(opts, o)
		}
	}
	if s.remaining() > 0 {
		for _, tile := range hand {
			if isFlower(tile.Kind) {
				opts = append(opts, s.option(seat, "replace_flower", tile, nil))
			}
		}
		if s.CanKong {
			for _, group := range grouped(hand) {
				if len(group) == 4 && !isFlower(group[0].Kind) {
					opts = append(opts, s.option(seat, "kan_closed", group[0], tileIDs(group)))
				}
			}
			for _, meld := range s.Seats[seat].Melds {
				if meld.Type == "pon" {
					for _, tile := range hand {
						if tile.Kind == meld.Tiles[0].Kind {
							opts = append(opts, s.option(seat, "kan_added", tile, nil))
						}
					}
				}
			}
		}
	}
	for _, tile := range hand {
		opts = append(opts, s.option(seat, "discard", tile, nil))
	}
	return opts
}
func (s *State) reactionOptions(seat int) []rulesdk.Option {
	p := s.Pending
	opts := []rulesdk.Option{s.option(seat, "pass", Tile{}, nil)}
	if isFlower(p.Tile.Kind) {
		return opts
	}
	if result, ok := s.evaluate(seat, p.Tile, false, p.Type == "rob_kong"); ok {
		o := s.option(seat, "hu", p.Tile, nil)
		o.Details = fanSummary(result)
		opts = append(opts, o)
	}
	if p.Type == "rob_kong" || s.remaining() == 0 {
		return opts
	}
	hand := s.Seats[seat].Hand
	matching := []Tile{}
	for _, t := range hand {
		if t.Kind == p.Tile.Kind {
			matching = append(matching, t)
		}
	}
	if len(matching) >= 2 {
		opts = append(opts, s.option(seat, "pon", p.Tile, tileIDs(matching[:2])))
	}
	if len(matching) >= 3 {
		opts = append(opts, s.option(seat, "kan_open", p.Tile, tileIDs(matching[:3])))
	}
	if seat == (p.Seat+1)%4 && len(p.Tile.Kind) == 2 && p.Tile.Kind[1] != 'z' {
		rank := int(p.Tile.Kind[0] - '0')
		suit := p.Tile.Kind[1]
		for start := rank - 2; start <= rank; start++ {
			if start < 1 || start > 7 {
				continue
			}
			consumed := []string{}
			valid := true
			for n := start; n < start+3; n++ {
				if n == rank {
					continue
				}
				id := ""
				for _, t := range hand {
					if t.Kind == fmt.Sprintf("%d%c", n, suit) {
						id = t.ID
						break
					}
				}
				if id == "" {
					valid = false
					break
				}
				consumed = append(consumed, id)
			}
			if valid {
				opts = append(opts, s.option(seat, "chi", p.Tile, consumed))
			}
		}
	}
	return opts
}
func fanSummary(r scoring.Result) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"non_flower_fan": r.NonFlower, "flower_points": r.Flower, "total_fan": r.Total, "fan_items": r.Fans, "winning_form": r.WinningForm, "decomposition": r.Decomposition, "explanations": r.Explanations, "explanation_coverage": r.ExplanationCoverage})
	return b
}
func tileIDs(tiles []Tile) []string {
	ids := make([]string, len(tiles))
	for i, t := range tiles {
		ids[i] = t.ID
	}
	return ids
}
func grouped(hand []Tile) [][]Tile {
	out := [][]Tile{}
	for _, t := range hand {
		if len(out) == 0 || out[len(out)-1][0].Kind != t.Kind {
			out = append(out, []Tile{t})
		} else {
			out[len(out)-1] = append(out[len(out)-1], t)
		}
	}
	return out
}

func (s *State) evaluate(seat int, win Tile, self, rob bool) (scoring.Result, bool) {
	if isFlower(win.Kind) {
		return scoring.Result{}, false
	}
	p := s.Seats[seat]
	hand := make([]string, 0, len(p.Hand))
	found := !self
	for _, t := range p.Hand {
		if isFlower(t.Kind) {
			return scoring.Result{}, false
		}
		if self && t.ID == win.ID {
			found = true
			continue
		}
		hand = append(hand, t.Kind)
	}
	if !found {
		return scoring.Result{}, false
	}
	input := scoring.Input{Hand: hand, WinTile: win.Kind, FlowerCount: len(p.Flowers), SelfDraw: self, FourthTile: s.publicOtherCopies(win) >= 3, KongRelated: rob || (self && s.DrawSource == "kong_replacement"), WallLast: s.remaining() == 0 && s.DrawSource != "deal", SeatWind: (seat - s.Dealer + 4) % 4, RoundWind: (s.HandIndex - 1) / 4}
	for _, m := range p.Melds {
		kind := m.Type
		if kind == "pon" {
			kind = "peng"
		}
		if kind == "kan_open" || kind == "kan_added" || kind == "kan_closed" {
			kind = "gang"
		}
		tiles := []string{}
		for _, t := range m.Tiles {
			tiles = append(tiles, t.Kind)
		}
		input.Melds = append(input.Melds, scoring.Meld{Kind: kind, Tiles: tiles, FromSeat: (seat - m.FromSeat + 4) % 4, Concealed: m.Concealed})
	}
	result, err := scoring.Evaluate(input)
	return result, err == nil && result.NonFlower >= 8
}
func (s *State) publicOtherCopies(win Tile) int {
	seen := map[string]bool{}
	for _, d := range s.Discards {
		if d.Tile.Kind == win.Kind && d.Tile.ID != win.ID {
			seen[d.Tile.ID] = true
		}
	}
	for _, seat := range s.Seats {
		for _, m := range seat.Melds {
			if m.Concealed {
				continue
			}
			for _, t := range m.Tiles {
				if t.Kind == win.Kind && t.ID != win.ID {
					seen[t.ID] = true
				}
			}
		}
	}
	return len(seen)
}

func (r Rule) Apply(raw rulesdk.Snapshot, input rulesdk.Input) (rulesdk.Transition, error) {
	s, err := load(raw)
	if err != nil {
		return rulesdk.Transition{}, err
	}
	switch input.Type {
	case "next_hand":
		if s.Phase != "intermission" {
			return rulesdk.Transition{}, errors.New("next_hand requires intermission")
		}
		s.HandIndex++
		s.Step++
		err = s.startHand()
	case "action":
		if s.Phase != "self" || s.Seats[s.Active].ParticipantID != input.ParticipantID {
			return rulesdk.Transition{}, errors.New("not the active participant")
		}
		var option *rulesdk.Option
		for _, o := range s.selfOptions() {
			if o.ID == input.OptionID {
				v := o
				option = &v
				break
			}
		}
		if option == nil {
			return rulesdk.Transition{}, errors.New("illegal or expired option")
		}
		s.Step++
		err = s.applySelf(*option)
	case "timeout":
		if s.Phase == "self" {
			opts := s.selfOptions()
			if len(opts) == 0 {
				return rulesdk.Transition{}, errors.New("no timeout option")
			}
			o := s.fallback(opts)
			s.Step++
			err = s.applySelf(o)
		} else if s.Phase == "reaction" {
			err = s.resolve(input.Choices)
			if err == nil {
				s.Step++
			}
		} else {
			return rulesdk.Transition{}, errors.New("no active decision")
		}
	case "resolve":
		if s.Phase != "reaction" {
			return rulesdk.Transition{}, errors.New("no reaction window")
		}
		// Choices reference the pre-transition window, so resolve before advancing its ID.
		err = s.resolve(input.Choices)
		if err == nil {
			s.Step++
		}
	default:
		return rulesdk.Transition{}, errors.New("unsupported MCR input")
	}
	if err != nil {
		return rulesdk.Transition{}, err
	}
	encoded, err := marshal(s)
	if err != nil {
		return rulesdk.Transition{}, err
	}
	data, _ := json.Marshal(s.LastAction)
	return rulesdk.Transition{State: encoded, Events: []rulesdk.Event{{Type: s.LastAction.Type, Data: data}}}, nil
}
func (s *State) fallback(opts []rulesdk.Option) rulesdk.Option {
	for _, typ := range []string{"hu", "replace_flower"} {
		for _, o := range opts {
			if o.Type == typ {
				return o
			}
		}
	}
	for _, o := range opts {
		if o.Type == "discard" && o.TileID == s.DrawnID {
			return o
		}
	}
	for _, o := range opts {
		if o.Type == "discard" {
			return o
		}
	}
	return opts[0]
}
func (s *State) applySelf(o rulesdk.Option) error {
	seat := s.Active
	p := &s.Seats[seat]
	tile, ok := findTile(p.Hand, o.TileID)
	if !ok {
		return errors.New("tile not held")
	}
	switch o.Type {
	case "hu":
		return s.finishWin(seat, seat, tile, true, false)
	case "discard":
		hand, t, err := removeTile(p.Hand, o.TileID)
		if err != nil {
			return err
		}
		p.Hand = hand
		d := Discard{ID: fmt.Sprintf("d%d-%d", s.HandIndex, len(s.Discards)+1), Seat: seat, Tile: t}
		s.Discards = append(s.Discards, d)
		s.Pending = &Pending{Type: "discard", Seat: seat, Tile: t, DiscardIndex: len(s.Discards) - 1}
		s.Phase = "reaction"
		s.DrawnID = ""
		s.CanKong = false
		s.LastAction = LastAction{Type: "discard", Seat: seat, DiscardID: d.ID}
	case "replace_flower":
		hand, t, err := removeTile(p.Hand, o.TileID)
		if err != nil {
			return err
		}
		p.Hand = hand
		p.Flowers = append(p.Flowers, t)
		if err = s.draw(seat, "flower_replacement"); err != nil {
			return err
		}
		s.LastAction = LastAction{Type: "replace_flower", Seat: seat}
	case "kan_closed":
		tiles, err := s.consume(seat, o.Consume)
		if err != nil {
			return err
		}
		p.Melds = append(p.Melds, Meld{Type: "kan_closed", Tiles: tiles, FromSeat: seat, Concealed: true})
		if err = s.draw(seat, "kong_replacement"); err != nil {
			return err
		}
		s.LastAction = LastAction{Type: "kan_closed", Seat: seat}
	case "kan_added":
		index := -1
		for i, m := range p.Melds {
			if m.Type == "pon" && m.Tiles[0].Kind == tile.Kind {
				index = i
				break
			}
		}
		if index < 0 {
			return errors.New("no pung to extend")
		}
		s.Pending = &Pending{Type: "rob_kong", Seat: seat, Tile: tile, MeldIndex: index}
		s.Phase = "reaction"
		s.LastAction = LastAction{Type: "kan_added_pending", Seat: seat}
	default:
		return errors.New("invalid self action")
	}
	return nil
}
func (s *State) consume(seat int, ids []string) ([]Tile, error) {
	tiles := []Tile{}
	for _, id := range ids {
		hand, t, err := removeTile(s.Seats[seat].Hand, id)
		if err != nil {
			return nil, err
		}
		s.Seats[seat].Hand = hand
		tiles = append(tiles, t)
	}
	return tiles, nil
}
func (s *State) resolve(choices map[string]string) error {
	p := *s.Pending
	type candidate struct {
		seat   int
		option rulesdk.Option
	}
	candidates := []candidate{}
	for participant, id := range choices {
		seat := s.seatOf(participant)
		if seat < 0 || seat == p.Seat {
			return errors.New("invalid response participant")
		}
		valid := false
		for _, o := range s.reactionOptions(seat) {
			if o.ID == id {
				valid = true
				if o.Type != "pass" {
					candidates = append(candidates, candidate{seat, o})
				}
				break
			}
		}
		if !valid {
			return errors.New("illegal or expired reaction option")
		}
	}
	priority := func(typ string) int {
		switch typ {
		case "hu":
			return 3
		case "pon", "kan_open":
			return 2
		case "chi":
			return 1
		}
		return 0
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if priority(a.option.Type) != priority(b.option.Type) {
			return priority(a.option.Type) > priority(b.option.Type)
		}
		return (a.seat-p.Seat+4)%4 < (b.seat-p.Seat+4)%4
	})
	if len(candidates) > 0 {
		winner := candidates[0]
		seat, o := winner.seat, winner.option
		if o.Type == "hu" {
			return s.finishWin(seat, p.Seat, p.Tile, false, p.Type == "rob_kong")
		}
		tiles, err := s.consume(seat, o.Consume)
		if err != nil {
			return err
		}
		tiles = append(tiles, p.Tile)
		sortHand(tiles)
		s.Seats[seat].Melds = append(s.Seats[seat].Melds, Meld{Type: o.Type, Tiles: tiles, FromSeat: p.Seat})
		s.Discards[p.DiscardIndex].Claimed = true
		s.Active = seat
		s.Pending = nil
		s.DrawnID = ""
		s.DrawSource = "claim"
		s.CanKong = false
		s.Phase = "self"
		if o.Type == "kan_open" {
			if err = s.draw(seat, "kong_replacement"); err != nil {
				return err
			}
		}
		s.LastAction = LastAction{Type: o.Type, Seat: seat, DiscardID: s.Discards[p.DiscardIndex].ID}
		return nil
	}
	if p.Type == "rob_kong" {
		hand, t, err := removeTile(s.Seats[p.Seat].Hand, p.Tile.ID)
		if err != nil {
			return err
		}
		s.Seats[p.Seat].Hand = hand
		m := &s.Seats[p.Seat].Melds[p.MeldIndex]
		m.Type = "kan_added"
		m.Tiles = append(m.Tiles, t)
		if err = s.draw(p.Seat, "kong_replacement"); err != nil {
			return err
		}
		s.LastAction = LastAction{Type: "kan_added", Seat: p.Seat}
		return nil
	}
	s.Pending = nil
	return s.draw((p.Seat+1)%4, "normal")
}
