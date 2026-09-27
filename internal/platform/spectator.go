package platform

import (
	"bytes"
	"encoding/json"
	"errors"
)

// No arbitrary metadata can cross this boundary. Plugins cannot add private fields.
// Reject rather than silently remove unknown fields: plugin disclosure is a defect.
type spectator struct {
	Winners []string `json:"winners,omitempty"`
	Policy  string   `json:"view_policy"`
	Ruleset struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"ruleset"`
	Phase         string `json:"phase"`
	HandIndex     int    `json:"hand_index"`
	ActiveSeat    int    `json:"active_seat"`
	DealerSeat    int    `json:"dealer_seat"`
	RoundWind     int    `json:"round_wind"`
	WallRemaining int    `json:"wall_remaining"`
	Seats         []struct {
		Seat        int    `json:"seat_id"`
		Participant string `json:"participant_id"`
		Name        string `json:"name"`
		Kind        string `json:"participant_kind"`
		Wind        int    `json:"seat_wind"`
		Count       int    `json:"hand_count"`
	} `json:"seats"`
	Discards []struct {
		ID      string `json:"discard_id"`
		Seat    int    `json:"from_seat"`
		Kind    string `json:"kind"`
		Claimed bool   `json:"claimed"`
	} `json:"discards"`
	LastAction *struct {
		Type    string `json:"type"`
		Seat    int    `json:"by_seat"`
		Discard string `json:"source_discard_id,omitempty"`
	} `json:"last_action,omitempty"`
	Scores []struct {
		Participant string `json:"participant_id"`
		Delta       int    `json:"delta,omitempty"`
		Total       int    `json:"total,omitempty"`
		Score       int    `json:"score,omitempty"`
	} `json:"scores"`
	Result *struct {
		Method string `json:"method"`
		Winner int    `json:"winner_seat"`
		Source int    `json:"source_seat"`
		Scores []struct {
			Participant string `json:"participant_id"`
			Seat        int    `json:"seat_id,omitempty"`
			Delta       int    `json:"delta"`
			Total       int    `json:"total,omitempty"`
		} `json:"score_deltas"`
	} `json:"result,omitempty"`
	Standings []struct {
		Participant    string `json:"participant_id"`
		Score          int    `json:"score"`
		Rank           int    `json:"rank"`
		StandardPoints *struct {
			Numerator   int `json:"numerator"`
			Denominator int `json:"denominator"`
		} `json:"standard_points,omitempty"`
	} `json:"standings,omitempty"`
}

func ValidateSpectator(raw json.RawMessage) (json.RawMessage, error) {
	var v spectator
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&v); e != nil {
		return nil, e
	}
	if v.Policy != "spectator_discard_only@1" {
		return nil, errors.New("invalid spectator policy")
	}
	seen := map[string]bool{}
	for _, a := range v.Discards {
		if a.ID == "" || seen[a.ID] || !validDiscardKind(a.Kind) {
			return nil, errors.New("invalid spectator discard")
		}
		seen[a.ID] = true
	}
	if v.LastAction != nil && v.LastAction.Discard != "" && !seen[v.LastAction.Discard] {
		return nil, errors.New("unknown discard reference")
	}
	return jsonBytes(v), nil
}
func validDiscardKind(k string) bool {
	if len(k) == 2 && k[0] >= '1' && k[0] <= '9' && (k[1] == 'm' || k[1] == 'p' || k[1] == 's') {
		return true
	}
	return len(k) == 2 && ((k[1] == 'z' && k[0] >= '1' && k[0] <= '7') || (k[0] == 'h' && k[1] >= '1' && k[1] <= '8'))
}
