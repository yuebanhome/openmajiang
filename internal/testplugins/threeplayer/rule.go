// Package threeplayer is a contract fixture, never registered in production.
package threeplayer

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

//go:embed rule.go
var source []byte

type Rule struct{}
type state struct {
	Players []rulesdk.Participant `json:"players"`
	Hand    int                   `json:"hand"`
	Ended   bool                  `json:"ended"`
	Dealer  int                   `json:"dealer"`
	Scores  []int                 `json:"scores"`
	Secret  string                `json:"secret"`
}

func New() *Rule { return &Rule{} }
func (*Rule) Manifest() rulesdk.Manifest {
	return rulesdk.Manifest{ID: "test.threeplayer", Version: "1.0.0", Name: "Three-player contract fixture", ArtifactHash: fmt.Sprintf("sha256:%x", sha256.Sum256(source)), APIVersion: "1", StateSchema: "toy.state.v1", ViewSchema: "toy.view.v1", Renderer: "toy.v1", SeatCounts: []int{3}, Formats: []string{"two_hands"}, Capabilities: []string{"multi_winner", "repeat_dealer", "spectator_discard_only@1"}}
}
func (*Rule) ValidateConfig(c rulesdk.Config) error {
	if len(c.Participants) != 3 || c.Format != "two_hands" {
		return errors.New("fixture requires 3 participants and two_hands")
	}
	seen := map[string]bool{}
	for _, p := range c.Participants {
		if p.ID == "" || seen[p.ID] {
			return errors.New("invalid participant")
		}
		seen[p.ID] = true
	}
	return nil
}
func (r *Rule) Init(c rulesdk.Config, entropy []byte) (rulesdk.Snapshot, error) {
	if e := r.ValidateConfig(c); e != nil {
		return nil, e
	}
	return json.Marshal(state{Players: c.Participants, Hand: 1, Scores: []int{0, 0, 0}, Secret: fmt.Sprintf("%x", entropy)})
}
func load(s rulesdk.Snapshot) (state, error) {
	var x state
	e := json.Unmarshal(s, &x)
	if e == nil && (len(x.Players) != 3 || len(x.Scores) != 3 || x.Hand < 1 || x.Hand > 2 || x.Dealer != 0) {
		e = errors.New("invalid fixture state")
	}
	return x, e
}
func window(x state) string { return fmt.Sprintf("toy-%d-%t", x.Hand, x.Ended) }
func (*Rule) Inspect(s rulesdk.Snapshot) (rulesdk.Flow, error) {
	x, e := load(s)
	if e != nil {
		return rulesdk.Flow{}, e
	}
	f := rulesdk.Flow{Phase: "choose", HandIndex: x.Hand, WindowID: window(x), WindowKind: "self", HandEnded: x.Ended, MatchEnded: x.Ended && x.Hand == 2}
	for i, p := range x.Players {
		f.Assignments = append(f.Assignments, rulesdk.Assignment{ParticipantID: p.ID, Seat: i})
		f.Scores = append(f.Scores, rulesdk.Score{ParticipantID: p.ID, Total: x.Scores[i]})
	}
	if x.Ended {
		f.HandResult = &rulesdk.HandResult{HandIndex: x.Hand, Scores: []rulesdk.Score{}, Winners: []rulesdk.Winner{{ParticipantID: x.Players[0].ID}, {ParticipantID: x.Players[1].ID}}, DiscarderID: x.Players[2].ID}
		for i, p := range x.Players {
			delta := 5
			if i == 2 {
				delta = -10
			}
			f.HandResult.Scores = append(f.HandResult.Scores, rulesdk.Score{ParticipantID: p.ID, Delta: delta, Total: x.Scores[i]})
		}
		if f.MatchEnded {
			for i, p := range x.Players {
				rank := 1
				if i == 2 {
					rank = 3
				}
				f.Rankings = append(f.Rankings, rulesdk.Ranking{ParticipantID: p.ID, Rank: rank, RawScore: x.Scores[i]})
			}
		}
		f.Phase = "settled"
		f.WindowKind = "intermission"
		if f.MatchEnded {
			f.WindowKind = "ended"
		}
	} else {
		f.Decisions = []rulesdk.Decision{{ID: window(x) + "-decision", ParticipantID: x.Players[0].ID, Seat: 0, Options: []rulesdk.Option{{ID: window(x) + "-finish", Type: "toy.finish"}}}}
	}
	return f, nil
}
func (*Rule) Apply(s rulesdk.Snapshot, in rulesdk.Input) (rulesdk.Transition, error) {
	x, e := load(s)
	if e != nil {
		return rulesdk.Transition{}, e
	}
	switch in.Type {
	case "action":
		if x.Ended || in.ParticipantID != x.Players[0].ID || in.OptionID != window(x)+"-finish" {
			return rulesdk.Transition{}, errors.New("invalid fixture action")
		}
		x.Ended = true
		x.Scores[0] += 5
		x.Scores[1] += 5
		x.Scores[2] -= 10
	case "timeout":
		if x.Ended {
			return rulesdk.Transition{}, errors.New("closed")
		}
		x.Ended = true
		x.Scores[0] += 5
		x.Scores[1] += 5
		x.Scores[2] -= 10
	case "next_hand":
		if !x.Ended || x.Hand == 2 {
			return rulesdk.Transition{}, errors.New("cannot advance")
		}
		x.Hand++
		x.Ended = false
	default:
		return rulesdk.Transition{}, errors.New("unsupported fixture input")
	}
	b, e := json.Marshal(x)
	return rulesdk.Transition{State: b, Events: []rulesdk.Event{{Type: "toy." + in.Type}}}, e
}
func (*Rule) Project(s rulesdk.Snapshot, v rulesdk.Viewer) (json.RawMessage, error) {
	x, e := load(s)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"phase": "choose", "hand_index": x.Hand, "active_seat": x.Dealer, "discards": []any{}, "scores": []any{}}
	scores := []map[string]any{}
	for i, p := range x.Players {
		scores = append(scores, map[string]any{"participant_id": p.ID, "score": x.Scores[i]})
	}
	out["scores"] = scores
	if x.Ended {
		out["phase"] = "settled"
		out["winners"] = []string{x.Players[0].ID, x.Players[1].ID}
	}
	switch v.Audience {
	case rulesdk.SpectatorDiscardOnly:
		out["view_policy"] = "spectator_discard_only@1"
	case rulesdk.ParticipantPrivate:
		found := false
		for _, p := range x.Players {
			if p.ID == v.ParticipantID {
				found = true
			}
		}
		if !found {
			return nil, errors.New("not a participant")
		}
		out["private_marker"] = x.Secret
		out["view_policy"] = "participant_private@1"
	default:
		return nil, errors.New("unsupported audience")
	}
	return json.Marshal(out)
}
