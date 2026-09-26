// Package bots implements baseline opponents using only a participant projection
// and the server's legal options. It never receives an authoritative snapshot.
package bots

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sort"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
)

type tile struct {
	ID   string `json:"tile_id"`
	Kind string `json:"kind"`
}
type meld struct {
	Type      string `json:"type"`
	Tiles     []tile `json:"tiles"`
	Concealed bool   `json:"concealed"`
}
type seat struct {
	ID      int    `json:"seat_id"`
	Wind    int    `json:"seat_wind"`
	Melds   []meld `json:"melds"`
	Flowers []tile `json:"flowers"`
}
type observation struct {
	Policy    string `json:"view_policy"`
	Hand      []tile `json:"hand"`
	Seats     []seat `json:"seats"`
	Seat      int    `json:"seat_id"`
	RoundWind int    `json:"round_wind"`
	Discards  []struct {
		Tile    tile `json:"tile"`
		Claimed bool `json:"claimed"`
	} `json:"discards"`
}

// Choose is deliberately a modest baseline, not a claim of expert play.
// random_legal is useful for protocol stress. basic_heuristic compares shanten,
// legal >=8-fan winning draws, visible remaining improvements, and fan potential.
func Choose(strategy string, view json.RawMessage, options []rulesdk.Option) (string, error) {
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

// This tie breaker is only an estimate. Win eligibility always comes from the
// official evaluator above and the authoritative legal action list.
func fanPotential(hand []string, packs []scoring.Meld) int {
	suits := map[byte]int{}
	counts := map[string]int{}
	honors := 0
	for _, k := range hand {
		if len(k) != 2 {
			continue
		}
		counts[k]++
		if k[1] == 'z' {
			honors++
		} else {
			suits[k[1]]++
		}
	}
	for _, p := range packs {
		for _, k := range p.Tiles {
			if len(k) == 2 {
				if k[1] == 'z' {
					honors++
				} else {
					suits[k[1]]++
				}
			}
		}
	}
	p := 0
	if len(suits) == 1 {
		p += 6
		if honors == 0 {
			p += 6
		}
	}
	pairs := 0
	for _, n := range counts {
		if n >= 2 {
			pairs++
		}
		if n >= 3 {
			p += 2
		}
	}
	if len(packs) == 0 {
		p += pairs
	}
	return p
}
