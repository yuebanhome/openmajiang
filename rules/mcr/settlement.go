package mcr

import (
	"errors"
	"sort"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func (s *State) scores() []rulesdk.Score {
	if s.Result != nil {
		return s.Result.Scores
	}
	scores := []rulesdk.Score{}
	for _, p := range s.Config.Participants {
		scores = append(scores, rulesdk.Score{ParticipantID: p.ID, Total: s.Totals[p.ID]})
	}
	return scores
}
func (s *State) finishWin(winner, source int, tile Tile, self, rob bool) error {
	fan, ok := s.evaluate(winner, tile, self, rob)
	if !ok {
		return errors.New("hand does not reach eight non-flower points")
	}
	if !self {
		if rob {
			hand, _, err := removeTile(s.Seats[source].Hand, tile.ID)
			if err != nil {
				return err
			}
			s.Seats[source].Hand = hand
		} else {
			s.Discards[s.Pending.DiscardIndex].Claimed = true
		}
		s.Seats[winner].Hand = append(s.Seats[winner].Hand, tile)
		sortHand(s.Seats[winner].Hand)
	}
	method := "discard_win"
	if self {
		method = "self_draw"
	}
	if rob {
		method = "rob_kong"
	}
	deltas := map[string]int{}
	winnerID := s.Seats[winner].ParticipantID
	for seat, p := range s.Seats {
		if seat == winner {
			continue
		}
		amount := 8
		if self || seat == source {
			amount += fan.Total
		}
		deltas[p.ParticipantID] = -amount
		deltas[winnerID] += amount
	}
	s.Result = &Result{Method: method, Winner: winner, Source: source, WinningTile: &tile, WinningHand: append([]Tile(nil), s.Seats[winner].Hand...), FanItems: fan.Fans, WinningForm: fan.WinningForm, Decomposition: fan.Decomposition, Explanations: fan.Explanations, ExplanationCoverage: fan.ExplanationCoverage, NonFlower: fan.NonFlower, Flower: fan.Flower, Total: fan.Total, Scores: []rulesdk.Score{}}
	for _, p := range s.Config.Participants {
		s.Totals[p.ID] += deltas[p.ID]
		s.Result.Scores = append(s.Result.Scores, rulesdk.Score{ParticipantID: p.ID, Delta: deltas[p.ID], Total: s.Totals[p.ID]})
	}
	s.Pending = nil
	s.LastAction = LastAction{Type: method, Seat: winner}
	s.finishHand()
	return nil
}
func (s *State) finishDraw() {
	s.Result = &Result{Method: "exhaustive_draw", Winner: -1, Source: -1, Scores: []rulesdk.Score{}}
	for _, p := range s.Config.Participants {
		s.Result.Scores = append(s.Result.Scores, rulesdk.Score{ParticipantID: p.ID, Total: s.Totals[p.ID]})
	}
	s.Pending = nil
	s.LastAction = LastAction{Type: "exhaustive_draw", Seat: s.Active}
	s.finishHand()
}
func (s *State) finishHand() {
	s.DrawnID = ""
	s.CanKong = false
	s.Phase = "intermission"
	if s.HandIndex >= s.handLimit() {
		s.Phase = "ended"
		s.computeStandings()
	}
}
func (s *State) computeStandings() {
	s.Standings = []Standing{}
	for _, p := range s.Config.Participants {
		s.Standings = append(s.Standings, Standing{ParticipantID: p.ID, Score: s.Totals[p.ID]})
	}
	sort.SliceStable(s.Standings, func(i, j int) bool { return s.Standings[i].Score > s.Standings[j].Score })
	points := []int{4, 2, 1, 0}
	for i := 0; i < len(s.Standings); {
		j := i + 1
		for j < len(s.Standings) && s.Standings[j].Score == s.Standings[i].Score {
			j++
		}
		sum := 0
		for k := i; k < j; k++ {
			sum += points[k]
		}
		for k := i; k < j; k++ {
			s.Standings[k].Rank = i + 1
			if s.Config.Format == "standard_16" {
				den := j - i
				g := gcd(sum, den)
				s.Standings[k].StandardPoints = &Rational{Numerator: sum / g, Denominator: den / g}
			}
		}
		i = j
	}
}
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
