package mcr

import (
	"encoding/json"
	"errors"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr/scoring"
)

type RuleIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type PublicDiscard struct {
	ID      string `json:"discard_id"`
	Seat    int    `json:"from_seat"`
	Kind    string `json:"kind"`
	Claimed bool   `json:"claimed"`
}
type PublicSeat struct {
	Seat            int    `json:"seat_id"`
	ParticipantID   string `json:"participant_id"`
	Name            string `json:"name"`
	ParticipantKind string `json:"participant_kind"`
	Wind            int    `json:"seat_wind"`
	HandCount       int    `json:"hand_count"`
}
type PublicResult struct {
	Method string          `json:"method"`
	Winner int             `json:"winner_seat"`
	Source int             `json:"source_seat"`
	Scores []rulesdk.Score `json:"score_deltas"`
}

// SpectatorView is an explicit allowlist. Never embed State, Seat, Meld, Result,
// scoring.Result, arbitrary maps or participant snapshots in this public DTO.
// The only card faces in this type are the Kind fields of historical discards.
type SpectatorView struct {
	Policy        string          `json:"view_policy"`
	Ruleset       RuleIdentity    `json:"ruleset"`
	Phase         string          `json:"phase"`
	HandIndex     int             `json:"hand_index"`
	ActiveSeat    int             `json:"active_seat"`
	DealerSeat    int             `json:"dealer_seat"`
	RoundWind     int             `json:"round_wind"`
	WallRemaining int             `json:"wall_remaining"`
	Seats         []PublicSeat    `json:"seats"`
	Discards      []PublicDiscard `json:"discards"`
	LastAction    LastAction      `json:"last_action"`
	Scores        []rulesdk.Score `json:"scores"`
	Result        *PublicResult   `json:"result,omitempty"`
	Standings     []Standing      `json:"standings,omitempty"`
}
type MeldView struct {
	Type      string `json:"type"`
	Count     int    `json:"tile_count"`
	Tiles     []Tile `json:"tiles,omitempty"`
	Concealed bool   `json:"concealed"`
	FromSeat  int    `json:"from_seat"`
}
type ParticipantSeat struct {
	PublicSeat
	Melds   []MeldView `json:"melds"`
	Flowers []Tile     `json:"flowers"`
}
type TriggerView struct {
	Type      string `json:"type"`
	FromSeat  int    `json:"from_seat"`
	Tile      Tile   `json:"tile"`
	DiscardID string `json:"discard_id,omitempty"`
}
type ParticipantView struct {
	Policy           string            `json:"view_policy"`
	Ruleset          RuleIdentity      `json:"ruleset"`
	SourceEdition    string            `json:"source_edition"`
	EvaluatorVersion string            `json:"evaluator_version"`
	OnlineProfile    string            `json:"online_profile"`
	MatchFormat      string            `json:"match_format"`
	Phase            string            `json:"phase"`
	HandIndex        int               `json:"hand_index"`
	ParticipantID    string            `json:"participant_id"`
	Seat             int               `json:"seat_id"`
	ActiveSeat       int               `json:"active_seat"`
	DealerSeat       int               `json:"dealer_seat"`
	RoundWind        int               `json:"round_wind"`
	WallRemaining    int               `json:"wall_remaining"`
	Hand             []Tile            `json:"hand"`
	DrawnID          string            `json:"drawn_tile_id,omitempty"`
	DrawContext      string            `json:"draw_context,omitempty"`
	Seats            []ParticipantSeat `json:"seats"`
	Discards         []Discard         `json:"discards"`
	Trigger          *TriggerView      `json:"trigger,omitempty"`
	LastAction       LastAction        `json:"last_action"`
	Scores           []rulesdk.Score   `json:"scores"`
	Result           *Result           `json:"result,omitempty"`
	Standings        []Standing        `json:"standings,omitempty"`
}

func (s *State) publicSeat(i int) PublicSeat {
	p := s.Seats[i]
	out := PublicSeat{Seat: i, ParticipantID: p.ParticipantID, Wind: (i - s.Dealer + 4) % 4, HandCount: len(p.Hand)}
	for _, participant := range s.Config.Participants {
		if participant.ID == p.ParticipantID {
			out.Name = participant.Name
			out.ParticipantKind = participant.Kind
			break
		}
	}
	return out
}
func (r Rule) Project(raw rulesdk.Snapshot, viewer rulesdk.Viewer) (json.RawMessage, error) {
	s, err := load(raw)
	if err != nil {
		return nil, err
	}
	return s.project(viewer)
}

// ProjectMany validates the immutable snapshot once and serializes each
// audience independently. No decoded state or shared result buffers escape.
func (r Rule) ProjectMany(raw rulesdk.Snapshot, viewers []rulesdk.Viewer) ([]json.RawMessage, error) {
	s, err := load(raw)
	if err != nil {
		return nil, err
	}
	views := make([]json.RawMessage, len(viewers))
	for i, viewer := range viewers {
		views[i], err = s.project(viewer)
		if err != nil {
			return nil, err
		}
	}
	return views, nil
}

func (s *State) project(viewer rulesdk.Viewer) (json.RawMessage, error) {
	identity := RuleIdentity{ID: "openmajiang.mcr", Version: version}
	if viewer.Audience == rulesdk.SpectatorDiscardOnly {
		v := SpectatorView{Policy: "spectator_discard_only@1", Ruleset: identity, Phase: s.Phase, HandIndex: s.HandIndex, ActiveSeat: s.Active, DealerSeat: s.Dealer, RoundWind: (s.HandIndex - 1) / 4, WallRemaining: s.remaining(), Seats: []PublicSeat{}, Discards: []PublicDiscard{}, LastAction: s.LastAction, Scores: s.scores(), Standings: s.Standings}
		for i := range s.Seats {
			v.Seats = append(v.Seats, s.publicSeat(i))
		}
		for _, d := range s.Discards {
			v.Discards = append(v.Discards, PublicDiscard{ID: d.ID, Seat: d.Seat, Kind: d.Tile.Kind, Claimed: d.Claimed})
		}
		if s.Result != nil {
			v.Result = &PublicResult{Method: s.Result.Method, Winner: s.Result.Winner, Source: s.Result.Source, Scores: s.Result.Scores}
		}
		return json.Marshal(v)
	}
	if viewer.Audience != rulesdk.ParticipantPrivate {
		return nil, errors.New("unsupported audience")
	}
	seat := s.seatOf(viewer.ParticipantID)
	if seat < 0 {
		return nil, errors.New("viewer is not a match participant")
	}
	v := ParticipantView{Policy: "participant_private@1", Ruleset: identity, SourceEdition: "wmo-mcr-2014-2nd", EvaluatorVersion: scoring.Profile, OnlineProfile: s.Config.Profile, MatchFormat: s.Config.Format, Phase: s.Phase, HandIndex: s.HandIndex, ParticipantID: viewer.ParticipantID, Seat: seat, ActiveSeat: s.Active, DealerSeat: s.Dealer, RoundWind: (s.HandIndex - 1) / 4, WallRemaining: s.remaining(), Hand: s.Seats[seat].Hand, Seats: []ParticipantSeat{}, Discards: s.Discards, LastAction: s.LastAction, Scores: s.scores(), Result: s.Result, Standings: s.Standings}
	if seat == s.Active {
		v.DrawnID = s.DrawnID
		v.DrawContext = s.DrawSource
	}
	finished := s.Phase == "intermission" || s.Phase == "ended"
	for i, p := range s.Seats {
		sv := ParticipantSeat{PublicSeat: s.publicSeat(i), Melds: []MeldView{}, Flowers: p.Flowers}
		for _, m := range p.Melds {
			mv := MeldView{Type: m.Type, Count: len(m.Tiles), Concealed: m.Concealed, FromSeat: m.FromSeat}
			if !m.Concealed || i == seat || finished {
				mv.Tiles = m.Tiles
			}
			sv.Melds = append(sv.Melds, mv)
		}
		v.Seats = append(v.Seats, sv)
	}
	if p := s.Pending; p != nil {
		v.Trigger = &TriggerView{Type: p.Type, FromSeat: p.Seat, Tile: p.Tile}
		if p.Type == "discard" {
			v.Trigger.DiscardID = s.Discards[p.DiscardIndex].ID
		}
	}
	return json.Marshal(v)
}
