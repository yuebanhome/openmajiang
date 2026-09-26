// Package mcr implements the versioned WMO MCR 2014 rules with OM-MCR-1 online amendments.
// It is deterministic, performs no I/O, and never consults a clock or global random source.
package mcr

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
)

const version = "1.0.0"

type Rule struct{}

func New() rulesdk.Rule { return Rule{} }

func (Rule) Manifest() rulesdk.Manifest {
	return rulesdk.Manifest{ID: "openmajiang.mcr", Version: version, Name: "国标麻将", APIVersion: "1", ArtifactHash: ruleFingerprint(), SourceEdition: "wmo-mcr-2014-2nd", OnlineProfiles: []string{"om-mcr-1"}, StateSchema: "mcr.state@1", ViewSchema: "mcr.view@1", Renderer: "mcr", SeatCounts: []int{4}, Formats: []string{"standard_16", "practice_1", "practice_4"}, Capabilities: []string{"flowers", "single_winner", "spectator_discard_only@1", "seat_rotation"}}
}

func (Rule) ValidateConfig(c rulesdk.Config) error {
	if c.Profile != "om-mcr-1" {
		return errors.New("MCR requires online profile om-mcr-1")
	}
	if c.Format != "standard_16" && c.Format != "practice_1" && c.Format != "practice_4" {
		return errors.New("unsupported match format")
	}
	if len(c.Participants) != 4 {
		return errors.New("MCR requires four participants")
	}
	seen := map[string]bool{}
	for _, p := range c.Participants {
		if p.ID == "" || seen[p.ID] {
			return errors.New("participant IDs must be unique and nonempty")
		}
		seen[p.ID] = true
	}
	if len(c.Options) > 0 && string(c.Options) != "null" && string(c.Options) != "{}" {
		return errors.New("MCR does not accept client supplied rule options or wall seeds")
	}
	return nil
}

type Tile struct {
	ID   string `json:"tile_id"`
	Kind string `json:"kind"`
}
type Meld struct {
	Type      string `json:"type"`
	Tiles     []Tile `json:"tiles"`
	FromSeat  int    `json:"from_seat"`
	Concealed bool   `json:"concealed"`
}
type Discard struct {
	ID      string `json:"discard_id"`
	Seat    int    `json:"from_seat"`
	Tile    Tile   `json:"tile"`
	Claimed bool   `json:"claimed"`
}
type Seat struct {
	ParticipantID string `json:"participant_id"`
	Hand          []Tile `json:"hand"`
	Melds         []Meld `json:"melds"`
	Flowers       []Tile `json:"flowers"`
}
type LastAction struct {
	Type      string `json:"type"`
	Seat      int    `json:"by_seat"`
	DiscardID string `json:"source_discard_id,omitempty"`
}
type Pending struct {
	Type         string `json:"type"`
	Seat         int    `json:"seat"`
	Tile         Tile   `json:"tile"`
	DiscardIndex int    `json:"discard_index"`
	MeldIndex    int    `json:"meld_index"`
}
type Rational struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}
type Standing struct {
	ParticipantID  string    `json:"participant_id"`
	Rank           int       `json:"rank"`
	Score          int       `json:"score"`
	StandardPoints *Rational `json:"standard_points,omitempty"`
}
type Result struct {
	Method              string                `json:"method"`
	Winner              int                   `json:"winner_seat"`
	Source              int                   `json:"source_seat"`
	WinningTile         *Tile                 `json:"winning_tile,omitempty"`
	WinningHand         []Tile                `json:"winning_hand,omitempty"`
	FanItems            []scoring.Fan         `json:"fan_items,omitempty"`
	WinningForm         string                `json:"winning_form,omitempty"`
	Decomposition       []scoring.Group       `json:"decomposition,omitempty"`
	Explanations        []scoring.Explanation `json:"explanations,omitempty"`
	ExplanationCoverage string                `json:"explanation_coverage,omitempty"`
	NonFlower           int                   `json:"non_flower_fan"`
	Flower              int                   `json:"flower_points"`
	Total               int                   `json:"total_fan"`
	Scores              []rulesdk.Score       `json:"score_deltas"`
}
type State struct {
	Schema              string         `json:"schema"`
	Config              rulesdk.Config `json:"config"`
	Seed                string         `json:"seed"`
	InitialOrder        []string       `json:"initial_order"`
	HandIndex           int            `json:"hand_index"`
	Step                uint64         `json:"step"`
	Phase               string         `json:"phase"`
	Dealer              int            `json:"dealer"`
	Active              int            `json:"active"`
	Seats               []Seat         `json:"seats"`
	Wall                []Tile         `json:"wall"`
	Head                int            `json:"head"`
	Tail                int            `json:"tail"`
	Discards            []Discard      `json:"discards"`
	DrawnID             string         `json:"drawn_tile_id"`
	DrawSource          string         `json:"draw_source"`
	DrawRemainingBefore int            `json:"draw_remaining_before"`
	CanKong             bool           `json:"can_kong"`
	Pending             *Pending       `json:"pending,omitempty"`
	Totals              map[string]int `json:"totals"`
	LastAction          LastAction     `json:"last_action"`
	Result              *Result        `json:"result,omitempty"`
	Standings           []Standing     `json:"standings,omitempty"`
}

func (s *State) remaining() int   { return s.Tail - s.Head }
func (s *State) windowID() string { return fmt.Sprintf("mcr-h%d-w%d", s.HandIndex, s.Step) }
func (s *State) handLimit() int {
	if s.Config.Format == "standard_16" {
		return 16
	}
	if s.Config.Format == "practice_4" {
		return 4
	}
	return 1
}
func (s *State) seatOf(id string) int {
	for i, p := range s.Seats {
		if p.ParticipantID == id {
			return i
		}
	}
	return -1
}
func marshal(s *State) (rulesdk.Snapshot, error) { return json.Marshal(s) }
func load(raw rulesdk.Snapshot) (*State, error) {
	if len(raw) > 262144 {
		return nil, errors.New("invalid MCR snapshot size")
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("invalid MCR snapshot")
	}
	if s.Schema != "mcr.state@1" {
		return nil, errors.New("incompatible MCR state schema")
	}
	if err := (Rule{}).ValidateConfig(s.Config); err != nil {
		return nil, err
	}
	if len(s.Seats) != 4 || len(s.InitialOrder) != 4 || s.HandIndex < 1 || s.HandIndex > s.handLimit() || s.Active < 0 || s.Active > 3 || s.Dealer < 0 || s.Dealer > 3 || s.Head < 0 || s.Tail < s.Head || s.Tail > len(s.Wall) || len(s.Wall) != 144 || s.Totals == nil {
		return nil, errors.New("invalid MCR state bounds")
	}
	if s.Phase != "self" && s.Phase != "reaction" && s.Phase != "intermission" && s.Phase != "ended" {
		return nil, errors.New("invalid MCR phase")
	}
	if s.Phase != "reaction" && s.Pending != nil {
		return nil, errors.New("pending response outside reaction phase")
	}
	if (s.Phase == "intermission" || s.Phase == "ended") != (s.Result != nil) {
		return nil, errors.New("invalid hand settlement phase")
	}
	if result := s.Result; result != nil {
		switch result.Method {
		case "exhaustive_draw":
			if result.Winner != -1 || result.Source != -1 {
				return nil, errors.New("invalid draw settlement")
			}
		case "self_draw", "discard_win", "rob_kong":
			if result.Winner < 0 || result.Winner > 3 || result.Source < 0 || result.Source > 3 {
				return nil, errors.New("invalid winner settlement")
			}
		default:
			return nil, errors.New("invalid settlement method")
		}
	}
	if s.Phase == "reaction" && (s.Pending == nil || s.Pending.Seat < 0 || s.Pending.Seat > 3 || (s.Pending.Type != "discard" && s.Pending.Type != "rob_kong") || (s.Pending.Type == "discard" && (s.Pending.DiscardIndex < 0 || s.Pending.DiscardIndex >= len(s.Discards))) || (s.Pending.Type == "rob_kong" && (s.Pending.MeldIndex < 0 || s.Pending.MeldIndex >= len(s.Seats[s.Pending.Seat].Melds)))) {
		return nil, errors.New("invalid reaction source")
	}
	ids := map[string]bool{}
	for _, seat := range s.Seats {
		if seat.ParticipantID == "" || ids[seat.ParticipantID] {
			return nil, errors.New("invalid seat assignment")
		}
		ids[seat.ParticipantID] = true
	}
	for _, p := range s.Config.Participants {
		if !ids[p.ID] {
			return nil, errors.New("invalid seat participant")
		}
	}
	initialIDs := map[string]bool{}
	for _, id := range s.InitialOrder {
		if !ids[id] || initialIDs[id] {
			return nil, errors.New("invalid initial seat order")
		}
		initialIDs[id] = true
	}
	if err := s.validateInventory(); err != nil {
		return nil, err
	}
	return &s, nil
}

func isFlower(kind string) bool {
	return len(kind) == 2 && kind[0] == 'h' && kind[1] >= '1' && kind[1] <= '8'
}
func findTile(hand []Tile, id string) (Tile, bool) {
	for _, t := range hand {
		if t.ID == id {
			return t, true
		}
	}
	return Tile{}, false
}
func removeTile(hand []Tile, id string) ([]Tile, Tile, error) {
	for i, t := range hand {
		if t.ID == id {
			return append(hand[:i:i], hand[i+1:]...), t, nil
		}
	}
	return hand, Tile{}, errors.New("tile not held")
}
