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
	ID             string         `json:"id"`
	Version        string         `json:"version"`
	Name           string         `json:"name"`
	APIVersion     string         `json:"plugin_api_version"`
	StateSchema    string         `json:"state_schema"`
	ViewSchema     string         `json:"view_schema"`
	Renderer       string         `json:"renderer_id"`
	SeatCounts     []int          `json:"seat_counts"`
	Formats        []string       `json:"formats"`
	Capabilities   []string       `json:"capabilities"`
	ArtifactHash   string         `json:"artifact_hash,omitempty"`
	SourceHash     string         `json:"source_hash,omitempty"`
	Build          *BuildIdentity `json:"build,omitempty"`
	SourceEdition  string         `json:"source_edition,omitempty"`
	OnlineProfiles []string       `json:"online_profiles,omitempty"`
}

// BuildIdentity describes the host executable that contains the compiled rule.
// It is build metadata, never a hash of a match, wall or private observation.
type BuildIdentity struct {
	GoVersion    string `json:"go_version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
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
	HandResult       *HandResult  `json:"hand_result,omitempty"`
	Rankings         []Ranking    `json:"rankings,omitempty"`
	PreserveDeadline bool         `json:"preserve_deadline,omitempty"` // current window continues the previous self-turn clock
	Phase            string       `json:"phase"`
	HandIndex        int          `json:"hand_index"`
	WindowID         string       `json:"window_id"`
	WindowKind       string       `json:"window_kind"` // self, reaction, intermission, ended
	Assignments      []Assignment `json:"assignments"`
	Decisions        []Decision   `json:"decisions"`
	HandEnded        bool         `json:"hand_ended"`
	MatchEnded       bool         `json:"match_ended"`
	Scores           []Score      `json:"scores"`
}

// HandResult contains only settled facts needed for generic platform statistics.
// It cannot include tile identities, hand composition or a private fan breakdown.
type HandResult struct {
	HandIndex   int      `json:"hand_index"`
	Scores      []Score  `json:"scores"`
	Winners     []Winner `json:"winners"`
	DiscarderID string   `json:"discarder_id,omitempty"`
}
type Winner struct {
	ParticipantID   string `json:"participant_id"`
	SelfDraw        bool   `json:"self_draw"`
	NonFlowerPoints *int   `json:"non_flower_points,omitempty"`
}
type Rational struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}
type Ranking struct {
	ParticipantID  string    `json:"participant_id"`
	Rank           int       `json:"rank"`
	RawScore       int       `json:"raw_score"`
	StandardPoints *Rational `json:"standard_points,omitempty"`
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

// BatchProjector is an optional optimization for projecting one immutable
// snapshot to several viewers. Results must match Project, preserve viewer
// order, and own independent buffers. The snapshot must receive the same full
// validation as Project. An error must return no partial views.
// Rules implementing only Rule remain supported by the platform.
type BatchProjector interface {
	ProjectMany(Snapshot, []Viewer) ([]json.RawMessage, error)
}
