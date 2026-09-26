// Package rulesdk defines the deterministic contract between the platform and rules.
package rulesdk

import "encoding/json"

type Snapshot = json.RawMessage
type Participant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Config struct {
	Format       string          `json:"format"`
	Profile      string          `json:"profile"`
	Participants []Participant   `json:"participants"`
	Options      json.RawMessage `json:"options,omitempty"`
}
type Manifest struct {
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	Name           string   `json:"name"`
	APIVersion     string   `json:"plugin_api_version"`
	StateSchema    string   `json:"state_schema"`
	ViewSchema     string   `json:"view_schema"`
	Renderer       string   `json:"renderer_id"`
	SeatCounts     []int    `json:"seat_counts"`
	Formats        []string `json:"formats"`
	Capabilities   []string `json:"capabilities"`
	ArtifactHash   string   `json:"artifact_hash,omitempty"`
	SourceEdition  string   `json:"source_edition,omitempty"`
	OnlineProfiles []string `json:"online_profiles,omitempty"`
}
type Option struct {
	ID      string          `json:"option_id"`
	Type    string          `json:"type"`
	TileID  string          `json:"tile_id,omitempty"`
	Kind    string          `json:"kind,omitempty"`
	Consume []string        `json:"consume_tile_ids,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}
type Decision struct {
	ID            string   `json:"decision_id"`
	ParticipantID string   `json:"participant_id"`
	Seat          int      `json:"seat_id"`
	Options       []Option `json:"legal_actions"`
}
type Assignment struct {
	ParticipantID string `json:"participant_id"`
	Seat          int    `json:"seat_id"`
}
type Score struct {
	ParticipantID string `json:"participant_id"`
	Delta         int    `json:"delta"`
	Total         int    `json:"total"`
}
type Flow struct {
	Phase       string       `json:"phase"`
	HandIndex   int          `json:"hand_index"`
	WindowID    string       `json:"window_id"`
	WindowKind  string       `json:"window_kind"` // self, reaction, intermission, ended
	Assignments []Assignment `json:"assignments"`
	Decisions   []Decision   `json:"decisions"`
	HandEnded   bool         `json:"hand_ended"`
	MatchEnded  bool         `json:"match_ended"`
	Scores      []Score      `json:"scores"`
}
type Input struct {
	Type          string            `json:"type"` // action, resolve, timeout, next_hand
	ParticipantID string            `json:"participant_id,omitempty"`
	OptionID      string            `json:"option_id,omitempty"`
	Choices       map[string]string `json:"choices,omitempty"`
}
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
type Transition struct {
	State  Snapshot
	Events []Event
}
type Viewer struct {
	Audience      string
	ParticipantID string
}

const (
	ParticipantPrivate   = "participant_private"
	SpectatorDiscardOnly = "spectator_discard_only"
)

type Rule interface {
	Manifest() Manifest
	ValidateConfig(Config) error
	Init(Config, []byte) (Snapshot, error)
	Inspect(Snapshot) (Flow, error)
	Apply(Snapshot, Input) (Transition, error)
	Project(Snapshot, Viewer) (json.RawMessage, error)
}
