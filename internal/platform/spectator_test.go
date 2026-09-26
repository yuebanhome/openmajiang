package platform

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestActualMCRSpectatorContract(t *testing.T) {
	rule := mcr.New()
	cfg := rulesdk.Config{Profile: "om-mcr-1", Format: "practice_1", Participants: []rulesdk.Participant{{ID: "a", Name: "A", Kind: "human"}, {ID: "b", Name: "B", Kind: "bot"}, {ID: "c", Name: "C", Kind: "bot"}, {ID: "d", Name: "D", Kind: "bot"}}}
	state, e := rule.Init(cfg, bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 700; i++ {
		view, e := rule.Project(state, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = ValidateSpectator(view); e != nil {
			t.Fatalf("step %d: %v", i, e)
		}
		f, e := rule.Inspect(state)
		if e != nil {
			t.Fatal(e)
		}
		if f.MatchEnded {
			return
		}
		input := rulesdk.Input{Type: "timeout"}
		if f.WindowKind == "reaction" {
			input = rulesdk.Input{Type: "resolve", Choices: map[string]string{}}
		}
		next, e := rule.Apply(state, input)
		if e != nil {
			t.Fatal(e)
		}
		state = next.State
	}
	t.Fatal("match did not finish")
}
func TestSpectatorRejectsPrivateExtensions(t *testing.T) {
	for _, extra := range []string{`"hand":["1m"]`, `"wall":[]`, `"fan_items":[]`, `"metadata":{"hidden":"5m"}`, `"tile_id":"secret"`} {
		raw := json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[],` + extra + `}`)
		if _, e := ValidateSpectator(raw); e == nil {
			t.Errorf("accepted private extension %s", extra)
		}
	}
	raw := json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[],"last_action":{"type":"pon","source_discard_id":"never-discarded"}}`)
	if _, e := ValidateSpectator(raw); e == nil {
		t.Fatal("accepted false discard reference")
	}
}
func TestSpectatorRejectsNestedPrivateTile(t *testing.T) {
	raw := json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[{"discard_id":"d1","from_seat":0,"kind":"1m","tile_id":"internal-tile-id"}]}`)
	if _, e := ValidateSpectator(raw); e == nil {
		t.Fatal("accepted nested entity ID")
	}
}
