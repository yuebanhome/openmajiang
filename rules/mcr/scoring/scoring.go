// Package scoring evaluates all 81 fan types in the pinned WMO 2014 MCR
// profile. It does not decide whether a player may declare a win: the rule
// engine must apply the eight non-flower point minimum to Result.NonFlower.
package scoring

/*
#cgo CXXFLAGS: -std=c++11 -O2
#cgo LDFLAGS: -lstdc++
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sort"
)

const (
	// Profile identifies both the primary rules source and local interpretations.
	Profile        = "wmo-2014-77220856-openmajiang-1"
	UpstreamCommit = "44a178af08bf11f82a8993fddbe2fe8876ddd8f3"
)

var (
	ErrInvalidInput = errors.New("invalid MCR scoring input")
	ErrNotWinning   = errors.New("tiles do not form a winning MCR hand")
	ErrEvaluator    = errors.New("MCR evaluator failure")
)

// Meld contains a declared set, including a concealed kong. Tiles has three
// elements for chi/peng and four for gang. FromSeat is a relative offer
// direction (1 upstream, 2 opposite, 3 downstream), not an absolute seat.
// The direction does not change fan evaluation; zero defaults to upstream for
// an exposed meld. A concealed set must be a kong and have FromSeat zero.
type Meld struct {
	Kind      string   `json:"kind"`
	Tiles     []string `json:"tiles"`
	FromSeat  int      `json:"from_seat"`
	Concealed bool     `json:"concealed"`
}

// Input.Hand excludes WinTile and declared Melds. Tile codes are 1m..9m,
// 1p..9p, 1s..9s and 1z..7z (east/south/west/north/red/green/white).
// All context flags must come from the authoritative rule state. In particular
// KongRelated means immediate kong replacement for a self draw or robbing an
// added kong for a discard win; a subsequent flower replacement clears it.
type Input struct {
	Hand        []string `json:"hand"`
	Melds       []Meld   `json:"melds"`
	WinTile     string   `json:"win_tile"`
	FlowerCount int      `json:"flower_count"`
	SelfDraw    bool     `json:"self_draw"`
	FourthTile  bool     `json:"fourth_tile"`
	KongRelated bool     `json:"kong_related"`
	WallLast    bool     `json:"wall_last"`
	SeatWind    int      `json:"seat_wind"`
	RoundWind   int      `json:"round_wind"`
}

// Fan.ID is the official WMO 2014 sequence number. Points is the value of one
// instance and Count is its multiplicity; their product contributes to Total.
type Fan struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Points int    `json:"points"`
	Count  int    `json:"count"`
}

type Result struct {
	NonFlower           int           `json:"non_flower"`
	Flower              int           `json:"flower"`
	Total               int           `json:"total"`
	Fans                []Fan         `json:"fans"`
	WinningForm         string        `json:"winning_form"`
	Decomposition       []Group       `json:"decomposition"`
	Explanations        []Explanation `json:"explanations"`
	ExplanationCoverage string        `json:"explanation_coverage"`
}

// Group belongs to the exact winning interpretation selected by the native
// evaluator. Concealed describes whether the group was declared face-down or
// remained in hand; it is not a claim that a discard-completed pung is an
// independently awarded Concealed Pung. No arbitrary second decomposition is
// substituted after score selection.
type Group struct {
	ID        int      `json:"id"`
	Kind      string   `json:"kind"`
	Tiles     []string `json:"tiles"`
	Declared  bool     `json:"declared"`
	Concealed bool     `json:"concealed"`
}

// Explanation lists a computed award and documented exclusion relationships.
// It deliberately does not fabricate per-fan tile attribution for contextual
// fans or claim to list every failed candidate from the native search.
type Explanation struct {
	FanID          int    `json:"fan_id"`
	Source         string `json:"source"`
	ExcludedFanIDs []int  `json:"excluded_fan_ids,omitempty"`
	Reason         string `json:"reason"`
}

// Catalog returns a fresh copy of the 81 official fan definitions. Count is
// zero: these definitions describe possible awards, not an evaluated hand.
func Catalog() []Fan {
	fans := make([]Fan, 81)
	for i := range fans {
		id := C.int(i + 1)
		fans[i] = Fan{ID: i + 1, Name: C.GoString(C.om_mcr_fan_name(id)), Points: int(C.om_mcr_fan_points(id))}
	}
	return fans
}

// Evaluate returns the highest-scoring consistent interpretation. It also
// returns valid hands worth fewer than eight points so callers can distinguish
// structural completion from a legally declarable win.
func Evaluate(input Input) (Result, error) {
	var native C.om_mcr_input
	if len(input.Melds) > 4 || len(input.Hand)+3*len(input.Melds) != 13 {
		return Result{}, fmt.Errorf("%w: hand length + 3*meld count must be 13", ErrInvalidInput)
	}
	if input.FlowerCount < 0 || input.FlowerCount > 8 || input.SeatWind < 0 || input.SeatWind > 3 || input.RoundWind < 0 || input.RoundWind > 3 {
		return Result{}, fmt.Errorf("%w: flowers must be 0..8 and winds 0..3", ErrInvalidInput)
	}
	var counts [128]int
	addTile := func(code string) (uint8, error) {
		tile, err := parseTile(code)
		if err != nil {
			return 0, err
		}
		counts[tile]++
		if counts[tile] > 4 {
			return 0, fmt.Errorf("%w: more than four %s", ErrInvalidInput, code)
		}
		return tile, nil
	}
	for i, code := range input.Hand {
		tile, err := addTile(code)
		if err != nil {
			return Result{}, err
		}
		native.hand[i] = C.uint8_t(tile)
	}
	win, err := addTile(input.WinTile)
	if err != nil {
		return Result{}, err
	}
	native.win_tile = C.uint8_t(win)
	for i, meld := range input.Melds {
		kind := uint8(0)
		want := 3
		switch meld.Kind {
		case "chi", "chow":
			kind = 1
		case "peng", "pon", "pung":
			kind = 2
		case "gang", "kan", "kong":
			kind = 3
			want = 4
		default:
			return Result{}, fmt.Errorf("%w: unknown meld kind %q", ErrInvalidInput, meld.Kind)
		}
		if len(meld.Tiles) != want || meld.FromSeat < 0 || meld.FromSeat > 3 || (meld.Concealed && (kind != 3 || meld.FromSeat != 0)) {
			return Result{}, fmt.Errorf("%w: malformed meld %d", ErrInvalidInput, i)
		}
		tiles := make([]int, want)
		for j, code := range meld.Tiles {
			tile, err := addTile(code)
			if err != nil {
				return Result{}, err
			}
			tiles[j] = int(tile)
		}
		sort.Ints(tiles)
		anchor := tiles[0]
		if kind == 1 {
			if tiles[0]>>4 == 4 || tiles[0]>>4 != tiles[2]>>4 || tiles[1] != tiles[0]+1 || tiles[2] != tiles[0]+2 {
				return Result{}, fmt.Errorf("%w: chi must be a same-suit sequence", ErrInvalidInput)
			}
			anchor = tiles[1]
		} else {
			for _, tile := range tiles {
				if tile != anchor {
					return Result{}, fmt.Errorf("%w: pung/kong tiles differ", ErrInvalidInput)
				}
			}
		}
		offer := meld.FromSeat
		if !meld.Concealed && offer == 0 {
			offer = 1
		}
		native.packs[i] = C.om_mcr_pack{kind: C.uint8_t(kind), tile: C.uint8_t(anchor), offer: C.uint8_t(offer)}
	}
	native.hand_count = C.int(len(input.Hand))
	native.pack_count = C.int(len(input.Melds))
	native.flowers = C.uint8_t(input.FlowerCount)
	native.seat_wind = C.uint8_t(input.SeatWind)
	native.round_wind = C.uint8_t(input.RoundWind)
	if input.SelfDraw {
		native.flags |= 1
	}
	if input.FourthTile {
		native.flags |= 2
	}
	if input.KongRelated {
		native.flags |= 4
	}
	if input.WallLast {
		native.flags |= 8
	}
	var result C.om_mcr_result
	switch status := int(C.om_mcr_evaluate(&native, &result)); status {
	case 0:
	case -3:
		return Result{}, ErrNotWinning
	case -1, -2:
		return Result{}, fmt.Errorf("%w: native code %d", ErrInvalidInput, status)
	default:
		return Result{}, fmt.Errorf("%w: native code %d", ErrEvaluator, status)
	}
	out := Result{Flower: input.FlowerCount, Total: int(result.total), Fans: make([]Fan, 0, 12)}
	out.NonFlower = out.Total - out.Flower
	for id := 1; id <= 81; id++ {
		count := int(result.counts[id])
		if count == 0 {
			continue
		}
		out.Fans = append(out.Fans, Fan{ID: id, Name: C.GoString(C.om_mcr_fan_name(C.int(id))), Points: int(C.om_mcr_fan_points(C.int(id))), Count: count})
	}
	out.WinningForm, out.Decomposition = decompose(input, result, out.Fans)
	out.Explanations = explain(out.Fans)
	out.ExplanationCoverage = "selected_maximum_form_and_documented_exclusions"
	return out, nil
}

func parseTile(code string) (uint8, error) {
	if len(code) != 2 || code[0] < '1' || code[0] > '9' {
		return 0, fmt.Errorf("%w: invalid tile %q", ErrInvalidInput, code)
	}
	rank := code[0] - '0'
	var suit uint8
	switch code[1] {
	case 'm':
		suit = 1
	case 's':
		suit = 2
	case 'p':
		suit = 3
	case 'z':
		suit = 4
		if rank > 7 {
			return 0, fmt.Errorf("%w: invalid honor %q", ErrInvalidInput, code)
		}
	default:
		return 0, fmt.Errorf("%w: invalid suit %q", ErrInvalidInput, code)
	}
	return suit<<4 | rank, nil
}

// Shanten analyzes an after-discard standing hand with 13-3*meldCount tiles.
// Zero means a structurally ready hand. It does not imply the eight-point
// minimum has been met. The rule engine must Evaluate each candidate win to
// check that minimum. Declared concealed kongs count toward meldCount.
//
// Useful tiles are the union of improvements for all equally best hand forms.
// They exclude four copies already in hand, but this function cannot see melds
// or public discards: a caller must filter exhausted tiles using its own
// authorized observation. Nothing in this function observes opponents' hands.
func Shanten(hand []string, meldCount int) (int, []string, error) {
	if meldCount < 0 || meldCount > 4 || len(hand)+3*meldCount != 13 {
		return 0, nil, fmt.Errorf("%w: hand length + 3*meld count must be 13", ErrInvalidInput)
	}
	var native [13]C.uint8_t
	var useful [128]C.uint8_t
	var counts [128]int
	for i, code := range hand {
		tile, err := parseTile(code)
		if err != nil {
			return 0, nil, err
		}
		counts[tile]++
		if counts[tile] > 4 {
			return 0, nil, fmt.Errorf("%w: more than four %s", ErrInvalidInput, code)
		}
		native[i] = C.uint8_t(tile)
	}
	distance := int(C.om_mcr_shanten(&native[0], C.int(len(hand)), C.int(meldCount), &useful[0]))
	if distance < 0 || distance > 13 {
		return 0, nil, ErrEvaluator
	}
	result := make([]string, 0, 34)
	for _, suit := range []byte{'m', 'p', 's', 'z'} {
		maxRank := byte('9')
		if suit == 'z' {
			maxRank = '7'
		}
		for rank := byte('1'); rank <= maxRank; rank++ {
			code := string([]byte{rank, suit})
			tile, _ := parseTile(code)
			if useful[tile] != 0 && counts[tile] < 4 {
				result = append(result, code)
			}
		}
	}
	return distance, result, nil
}

func tileCode(tile uint8) string {
	suit := map[uint8]byte{1: 'm', 2: 's', 3: 'p', 4: 'z'}[tile>>4]
	return string([]byte{'0' + (tile & 15), suit})
}

func decompose(input Input, native C.om_mcr_result, fans []Fan) (string, []Group) {
	groups := make([]Group, 0, 14)
	add := func(kind string, tiles []string, declared, concealed bool) {
		groups = append(groups, Group{ID: len(groups), Kind: kind, Tiles: tiles, Declared: declared, Concealed: concealed})
	}
	if native.form == 1 || native.form == 5 {
		form := "regular"
		if native.form == 5 {
			form = "knitted_straight"
			tiles := make([]string, 9)
			for i := range tiles {
				tiles[i] = tileCode(uint8(native.knitted[i]))
			}
			add("knitted_straight", tiles, false, true)
		}
		for i := 0; i < int(native.pack_count); i++ {
			pack := uint16(native.packs[i])
			anchor := uint8(pack & 255)
			kind := ""
			var tiles []string
			switch (pack >> 8) & 15 {
			case 1:
				kind = "chi"
				tiles = []string{tileCode(anchor - 1), tileCode(anchor), tileCode(anchor + 1)}
			case 2:
				kind = "peng"
				tiles = []string{tileCode(anchor), tileCode(anchor), tileCode(anchor)}
			case 3:
				kind = "gang"
				tiles = []string{tileCode(anchor), tileCode(anchor), tileCode(anchor), tileCode(anchor)}
			case 4:
				kind = "pair"
				tiles = []string{tileCode(anchor), tileCode(anchor)}
			}
			declared := i < len(input.Melds)
			add(kind, tiles, declared, pack&0x3000 == 0)
		}
		return form, groups
	}
	present := map[int]bool{}
	for _, fan := range fans {
		present[fan.ID] = true
	}
	if present[4] {
		base := append([]string(nil), input.Hand...)
		sort.Strings(base)
		add("nine_gates_base", base, false, true)
		add("winning_tile", []string{input.WinTile}, false, true)
		return "nine_gates", groups
	}
	counts := map[string]int{}
	for _, tile := range input.Hand {
		counts[tile]++
	}
	counts[input.WinTile]++
	tiles := make([]string, 0, len(counts))
	for tile := range counts {
		tiles = append(tiles, tile)
	}
	sort.Strings(tiles)
	form := "honors_and_knitted_tiles"
	if present[6] || present[19] {
		form = "seven_pairs"
	} else if present[7] {
		form = "thirteen_orphans"
	}
	for _, tile := range tiles {
		count := counts[tile]
		for count >= 2 {
			add("pair", []string{tile, tile}, false, true)
			count -= 2
		}
		if count == 1 {
			add("single", []string{tile}, false, true)
		}
	}
	return form, groups
}
