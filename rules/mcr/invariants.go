package mcr

import "errors"

func validKind(kind string) bool {
	if isFlower(kind) {
		return true
	}
	if len(kind) != 2 || kind[0] < '1' {
		return false
	}
	switch kind[1] {
	case 'm', 'p', 's':
		return kind[0] <= '9'
	case 'z':
		return kind[0] <= '7'
	}
	return false
}

// validateInventory checks all 144 physical tiles. The complete wall is retained
// as a canonical identity catalog, while only [Head,Tail) still belongs to it.
// A claimed discard is a history reference, never a second physical owner.
func (s *State) validateInventory() error {
	catalog := map[string]string{}
	counts := map[string]int{}
	for _, t := range s.Wall {
		if t.ID == "" || !validKind(t.Kind) || catalog[t.ID] != "" {
			return errors.New("invalid tile catalog")
		}
		catalog[t.ID] = t.Kind
		counts[t.Kind]++
	}
	for _, suit := range []byte{'m', 'p', 's', 'z'} {
		limit := byte('9')
		if suit == 'z' {
			limit = '7'
		}
		for rank := byte('1'); rank <= limit; rank++ {
			if counts[string([]byte{rank, suit})] != 4 {
				return errors.New("invalid ordinary tile count")
			}
		}
	}
	for rank := byte('1'); rank <= '8'; rank++ {
		if counts[string([]byte{'h', rank})] != 1 {
			return errors.New("invalid flower count")
		}
	}
	owned := map[string]bool{}
	claim := func(t Tile) error {
		if catalog[t.ID] != t.Kind || owned[t.ID] {
			return errors.New("tile has invalid or duplicate ownership")
		}
		owned[t.ID] = true
		return nil
	}
	for _, t := range s.Wall[s.Head:s.Tail] {
		if err := claim(t); err != nil {
			return err
		}
	}
	for _, seat := range s.Seats {
		if len(seat.Hand) > 18 || len(seat.Melds) > 4 || len(seat.Flowers) > 8 {
			return errors.New("invalid player tile zones")
		}
		for _, t := range seat.Hand {
			if err := claim(t); err != nil {
				return err
			}
		}
		for _, t := range seat.Flowers {
			if !isFlower(t.Kind) {
				return errors.New("non-flower in flower zone")
			}
			if err := claim(t); err != nil {
				return err
			}
		}
		for _, m := range seat.Melds {
			if m.FromSeat < 0 || m.FromSeat > 3 {
				return errors.New("invalid meld source")
			}
			length := 3
			switch m.Type {
			case "chi", "pon":
			case "kan_open", "kan_added", "kan_closed":
				length = 4
			default:
				return errors.New("invalid meld type")
			}
			if len(m.Tiles) != length {
				return errors.New("invalid meld length")
			}
			for _, t := range m.Tiles {
				if isFlower(t.Kind) {
					return errors.New("flower in meld")
				}
				if err := claim(t); err != nil {
					return err
				}
			}
		}
	}
	if len(s.Discards) > 144 {
		return errors.New("too many discards")
	}
	seenDiscards := map[string]bool{}
	for _, d := range s.Discards {
		if d.ID == "" || seenDiscards[d.ID] || d.Seat < 0 || d.Seat > 3 || catalog[d.Tile.ID] != d.Tile.Kind {
			return errors.New("invalid discard history")
		}
		seenDiscards[d.ID] = true
		if !d.Claimed {
			if err := claim(d.Tile); err != nil {
				return err
			}
		}
	}
	if len(owned) != 144 {
		return errors.New("physical tile conservation failed")
	}
	if p := s.Pending; p != nil {
		if catalog[p.Tile.ID] != p.Tile.Kind {
			return errors.New("invalid pending tile")
		}
		if p.Type == "discard" {
			d := s.Discards[p.DiscardIndex]
			if d.Tile.ID != p.Tile.ID || d.Seat != p.Seat || d.Claimed {
				return errors.New("invalid pending discard")
			}
		} else if _, ok := findTile(s.Seats[p.Seat].Hand, p.Tile.ID); !ok {
			return errors.New("invalid added kong tile")
		}
	}
	return nil
}
