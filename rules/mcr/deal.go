package mcr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// hashRNG is a domain-separated SHA-256 counter stream. Its definition is part of
// mcr.state@1: neither Go's math/rand nor map iteration determines a dealt wall.
type hashRNG struct {
	key     []byte
	counter uint64
}

func (r *hashRNG) uint64() uint64 {
	m := hmac.New(sha256.New, r.key)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], r.counter)
	r.counter++
	m.Write(n[:])
	return binary.BigEndian.Uint64(m.Sum(nil)[:8])
}
func (r *hashRNG) n(n int) int {
	bound := uint64(n)
	threshold := -bound % bound
	for {
		v := r.uint64()
		if v >= threshold {
			return int(v % bound)
		}
	}
}
func keyed(seed []byte, label string) []byte {
	m := hmac.New(sha256.New, seed)
	m.Write([]byte(label))
	return m.Sum(nil)
}

func (r Rule) Init(c rulesdk.Config, entropy []byte) (rulesdk.Snapshot, error) {
	if err := r.ValidateConfig(c); err != nil {
		return nil, err
	}
	if len(entropy) < 32 {
		return nil, errors.New("at least 32 bytes of host secret entropy required")
	}
	seed := sha256.Sum256(entropy)
	s := &State{Schema: "mcr.state@1", Config: c, Seed: hex.EncodeToString(seed[:]), HandIndex: 1, Step: 1, Totals: map[string]int{}}
	for _, p := range c.Participants {
		s.InitialOrder = append(s.InitialOrder, p.ID)
		s.Totals[p.ID] = 0
	}
	rng := hashRNG{key: keyed(seed[:], "seat-order")}
	for i := len(s.InitialOrder) - 1; i > 0; i-- {
		j := rng.n(i + 1)
		s.InitialOrder[i], s.InitialOrder[j] = s.InitialOrder[j], s.InitialOrder[i]
	}
	if err := s.startHand(); err != nil {
		return nil, err
	}
	return marshal(s)
}

var rotations = [4][4]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {2, 3, 1, 0}, {3, 2, 0, 1}}

func (s *State) startHand() error {
	seed, err := hex.DecodeString(s.Seed)
	if err != nil || len(seed) != 32 {
		return errors.New("invalid secret seed")
	}
	s.Seats = make([]Seat, 4)
	for i, original := range rotations[(s.HandIndex-1)/4] {
		s.Seats[i] = Seat{ParticipantID: s.InitialOrder[original], Hand: []Tile{}, Melds: []Meld{}, Flowers: []Tile{}}
	}
	s.Dealer = (s.HandIndex - 1) % 4
	s.Active = s.Dealer
	s.Wall = make([]Tile, 0, 144)
	add := func(kind string) {
		id := keyed(seed, fmt.Sprintf("tile/%d/%d", s.HandIndex, len(s.Wall)))
		s.Wall = append(s.Wall, Tile{ID: hex.EncodeToString(id[:12]), Kind: kind})
	}
	for _, suit := range []byte{'m', 'p', 's', 'z'} {
		count := 9
		if suit == 'z' {
			count = 7
		}
		for n := 1; n <= count; n++ {
			for copy := 0; copy < 4; copy++ {
				add(fmt.Sprintf("%d%c", n, suit))
			}
		}
	}
	for n := 1; n <= 8; n++ {
		add(fmt.Sprintf("h%d", n))
	}
	rng := hashRNG{key: keyed(seed, fmt.Sprintf("wall/%d", s.HandIndex))}
	for i := len(s.Wall) - 1; i > 0; i-- {
		j := rng.n(i + 1)
		s.Wall[i], s.Wall[j] = s.Wall[j], s.Wall[i]
	}
	s.Head = 0
	s.Tail = len(s.Wall)
	s.Discards = []Discard{}
	s.Pending = nil
	s.Result = nil
	s.Standings = nil
	s.DrawnID = ""
	s.DrawSource = "deal"
	s.CanKong = true
	// Three rounds of four tiles, one tile to each player, and one extra to East.
	for round := 0; round < 3; round++ {
		for offset := 0; offset < 4; offset++ {
			for n := 0; n < 4; n++ {
				s.dealOne((s.Dealer + offset) % 4)
			}
		}
	}
	for offset := 0; offset < 4; offset++ {
		s.dealOne((s.Dealer + offset) % 4)
	}
	s.DrawRemainingBefore = s.remaining()
	s.dealOne(s.Dealer)
	s.DrawnID = s.Seats[s.Dealer].Hand[len(s.Seats[s.Dealer].Hand)-1].ID
	for offset := 0; offset < 4; offset++ {
		seat := (s.Dealer + offset) % 4
		for {
			var flower Tile
			for _, t := range s.Seats[seat].Hand {
				if isFlower(t.Kind) {
					flower = t
					break
				}
			}
			if flower.ID == "" {
				break
			}
			hand, t, err := removeTile(s.Seats[seat].Hand, flower.ID)
			if err != nil {
				return err
			}
			s.Seats[seat].Hand = hand
			s.Seats[seat].Flowers = append(s.Seats[seat].Flowers, t)
			if s.remaining() == 0 {
				return errors.New("wall exhausted during initial flowers")
			}
			remainingBefore := s.remaining()
			s.Tail--
			replacement := s.Wall[s.Tail]
			s.Seats[seat].Hand = append(s.Seats[seat].Hand, replacement)
			if seat == s.Dealer {
				s.DrawnID = replacement.ID
				s.DrawSource = "flower_replacement"
				s.DrawRemainingBefore = remainingBefore
			}
		}
	}
	for i := range s.Seats {
		sortHand(s.Seats[i].Hand)
	}
	s.Phase = "self"
	s.LastAction = LastAction{Type: "hand_started", Seat: s.Dealer}
	return nil
}
func (s *State) dealOne(seat int) {
	s.Seats[seat].Hand = append(s.Seats[seat].Hand, s.Wall[s.Head])
	s.Head++
}
func sortHand(h []Tile) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Kind == h[j].Kind {
			return h[i].ID < h[j].ID
		}
		return kindOrder(h[i].Kind) < kindOrder(h[j].Kind)
	})
}
func kindOrder(k string) int {
	if isFlower(k) {
		return 34 + int(k[1]-'1')
	}
	if len(k) != 2 {
		return 100
	}
	base := map[byte]int{'m': 0, 'p': 9, 's': 18, 'z': 27}[k[1]]
	return base + int(k[0]-'1')
}

func (s *State) draw(seat int, source string) error {
	if s.remaining() == 0 {
		s.finishDraw()
		return nil
	}
	s.Active = seat
	s.DrawRemainingBefore = s.remaining()
	var tile Tile
	if source == "normal" {
		tile = s.Wall[s.Head]
		s.Head++
	} else {
		s.Tail--
		tile = s.Wall[s.Tail]
	}
	s.Seats[seat].Hand = append(s.Seats[seat].Hand, tile)
	sortHand(s.Seats[seat].Hand)
	s.DrawnID = tile.ID
	s.DrawSource = source
	s.CanKong = true
	s.Phase = "self"
	s.Pending = nil
	s.LastAction = LastAction{Type: "draw", Seat: seat}
	return nil
}
