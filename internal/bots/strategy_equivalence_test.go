// This frozen pre-optimization implementation is intentionally retained only
// in tests. It independently verifies every original option ranking when
// candidate evaluation is reused by kind; do not mechanically update it with
// cache changes. It uses the same unchanged fanPotential/scoring contracts.
package bots

import (
	"encoding/json"
	"errors"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
	"math/rand/v2"
	"sort"
	"testing"
)

func referenceChooseBeforeReuse(strategy string, view json.RawMessage, options []rulesdk.Option) (string, error) {
	if len(options) == 0 {
		return "", errors.New("no legal options")
	}
	if strategy != "random_legal" && strategy != "basic_heuristic" {
		return "", errors.New("unknown bot strategy")
	}
	var v observation
	if json.Unmarshal(view, &v) != nil || v.Policy != "participant_private@1" {
		return "", errors.New("bot requires a participant projection")
	}
	for _, o := range options {
		if o.Type == "hu" {
			return o.ID, nil
		}
	}
	for _, o := range options {
		if o.Type == "replace_flower" {
			return o.ID, nil
		}
	}
	if strategy == "random_legal" {
		return options[rand.IntN(len(options))].ID, nil
	}
	for _, o := range options {
		if o.Type == "pass" {
			return o.ID, nil
		}
	}
	var own seat
	visible := map[string]int{}
	for _, t := range v.Hand {
		visible[t.Kind]++
	}
	for _, s := range v.Seats {
		if s.ID == v.Seat {
			own = s
		}
		for _, m := range s.Melds {
			for _, t := range m.Tiles {
				visible[t.Kind]++
			}
		}
	}
	for _, d := range v.Discards {
		if !d.Claimed {
			visible[d.Tile.Kind]++
		}
	}
	packs := []scoring.Meld{}
	for _, m := range own.Melds {
		kind := "gang"
		if m.Type == "chi" {
			kind = "chi"
		}
		if m.Type == "pon" || m.Type == "peng" {
			kind = "peng"
		}
		p := scoring.Meld{Kind: kind, Concealed: m.Concealed}
		for _, t := range m.Tiles {
			p.Tiles = append(p.Tiles, t.Kind)
		}
		packs = append(packs, p)
	}
	type candidate struct {
		id                                 string
		distance, winning, outs, potential int
	}
	choices := []candidate{}
	for _, o := range options {
		if o.Type != "discard" {
			continue
		}
		hand := []string{}
		found := false
		for _, t := range v.Hand {
			if t.ID == o.TileID {
				found = true
				continue
			}
			hand = append(hand, t.Kind)
		}
		if !found {
			continue
		}
		d, improve, e := scoring.Shanten(hand, len(packs))
		if e != nil {
			continue
		}
		c := candidate{id: o.ID, distance: d, potential: fanPotential(hand, packs)}
		for _, kind := range improve {
			n := 4 - visible[kind]
			if n < 0 {
				n = 0
			}
			c.outs += n
			if d == 0 {
				r, e := scoring.Evaluate(scoring.Input{Hand: hand, Melds: packs, WinTile: kind, SelfDraw: true, SeatWind: own.Wind, RoundWind: v.RoundWind})
				if e == nil && r.NonFlower >= 8 {
					c.winning += n
				}
			}
		}
		choices = append(choices, c)
	}
	if len(choices) == 0 {
		return options[0].ID, nil
	}
	sort.SliceStable(choices, func(i, j int) bool {
		a, b := choices[i], choices[j]
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		if a.winning != b.winning {
			return a.winning > b.winning
		}
		if a.outs != b.outs {
			return a.outs > b.outs
		}
		if a.potential != b.potential {
			return a.potential > b.potential
		}
		return a.id < b.id
	})
	return choices[0].id, nil
}

func TestBasicHeuristicReusePreservesEveryOptionRanking(t *testing.T) {
	for _, c := range heuristicCorpus(t) {
		options := append([]rulesdk.Option(nil), c.Options...)
		// Repeatedly remove the winner to compare the full original ranking,
		// including each physical duplicate tile and its original option ID.
		for len(options) > 0 {
			want, err := referenceChooseBeforeReuse("basic_heuristic", c.View, options)
			if err != nil {
				t.Fatal(c.Name, err)
			}
			got, err := Choose("basic_heuristic", c.View, options)
			if err != nil || got != want {
				t.Fatalf("%s remaining=%d got=%q want=%q err=%v", c.Name, len(options), got, want, err)
			}
			found := false
			for i, o := range options {
				if o.ID == want {
					options = append(options[:i], options[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("reference chose invalid option", c.Name, want)
			}
		}
	}
}
