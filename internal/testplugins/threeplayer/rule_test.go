package threeplayer

import (
	"bytes"
	"encoding/json"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"testing"
)

func fixtureConfig() rulesdk.Config {
	return rulesdk.Config{Format: "two_hands", Participants: []rulesdk.Participant{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
}
func TestThreePlayersTwoWinnersRepeatDealer(t *testing.T) {
	r := New()
	s, e := r.Init(fixtureConfig(), []byte("hidden"))
	if e != nil {
		t.Fatal(e)
	}
	for hand := 1; hand <= 2; hand++ {
		f, e := r.Inspect(s)
		if e != nil {
			t.Fatal(e)
		}
		if len(f.Assignments) != 3 || f.Decisions[0].ParticipantID != "a" || f.HandIndex != hand {
			t.Fatalf("unexpected flow: %+v", f)
		}
		tr, e := r.Apply(s, rulesdk.Input{Type: "action", ParticipantID: "a", OptionID: f.Decisions[0].Options[0].ID})
		if e != nil {
			t.Fatal(e)
		}
		s = tr.State
		f, _ = r.Inspect(s)
		if !f.HandEnded || f.MatchEnded != (hand == 2) {
			t.Fatalf("wrong completion: %+v", f)
		}
		if f.Scores[0].Total != hand*5 || f.Scores[1].Total != hand*5 || f.Scores[2].Total != -hand*10 {
			t.Fatal("wrong multi-winner payments")
		}
		if hand == 1 {
			tr, e = r.Apply(s, rulesdk.Input{Type: "next_hand"})
			if e != nil {
				t.Fatal(e)
			}
			s = tr.State
		}
	}
}
func TestProjectionDoesNotDisclosePrivateState(t *testing.T) {
	r := New()
	a, _ := r.Init(fixtureConfig(), []byte("secret-A"))
	b, _ := r.Init(fixtureConfig(), []byte("secret-B"))
	av, e := r.Project(a, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
	if e != nil {
		t.Fatal(e)
	}
	bv, _ := r.Project(b, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
	if !bytes.Equal(av, bv) {
		t.Fatal("hidden state changed spectator output")
	}
	if _, e = r.Project(a, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: "intruder"}); e == nil {
		t.Fatal("foreign participant received private view")
	}
}
func TestInvalidActionLeavesSnapshotUnchanged(t *testing.T) {
	r := New()
	s, _ := r.Init(fixtureConfig(), nil)
	copyBefore := append([]byte{}, s...)
	if _, e := r.Apply(s, rulesdk.Input{Type: "action", ParticipantID: "c", OptionID: "toy-1-finish"}); e == nil {
		t.Fatal("accepted invalid action")
	}
	if !bytes.Equal(s, copyBefore) {
		t.Fatal("mutated original snapshot")
	}
}
func FuzzSnapshot(f *testing.F) {
	r := New()
	s, _ := r.Init(fixtureConfig(), nil)
	f.Add([]byte(s))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if !json.Valid(b) {
			return
		}
		_, _ = r.Inspect(b)
		_, _ = r.Project(b, rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly})
	})
}
