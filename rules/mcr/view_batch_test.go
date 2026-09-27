package mcr

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func projectionViewers() []rulesdk.Viewer {
	return []rulesdk.Viewer{
		{Audience: rulesdk.SpectatorDiscardOnly},
		{Audience: rulesdk.ParticipantPrivate, ParticipantID: "A"},
		{Audience: rulesdk.ParticipantPrivate, ParticipantID: "B"},
		{Audience: rulesdk.ParticipantPrivate, ParticipantID: "C"},
		{Audience: rulesdk.ParticipantPrivate, ParticipantID: "D"},
	}
}

func TestProjectManyMatchesIndividualAcrossPhases(t *testing.T) {
	rule := Rule{}
	check := func(t *testing.T, raw rulesdk.Snapshot) {
		t.Helper()
		before := bytes.Clone(raw)
		viewers := projectionViewers()
		// Deliberately reorder and repeat viewers to catch seat-order coupling.
		viewers = append([]rulesdk.Viewer{viewers[3]}, append(viewers, viewers[0])...)
		views, err := rule.ProjectMany(raw, viewers)
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != len(viewers) || !bytes.Equal(raw, before) {
			t.Fatal("batch changed the snapshot or projection count")
		}
		for i, viewer := range viewers {
			want, err := rule.Project(raw, viewer)
			if err != nil || !bytes.Equal(views[i], want) {
				t.Fatalf("viewer %d differs from individual projection: %v", i, err)
			}
		}
	}

	t.Run("initial", func(t *testing.T) { check(t, initialized(t, "standard_16", 7)) })
	t.Run("reaction_and_open_meld", func(t *testing.T) {
		s := discard(t, fixture(t, "3m", "1m 2m", "3m 3m"), 0, "3m")
		check(t, encode(t, s))
		pon := choose(t, s.reactionOptions(2), "pon")
		check(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve", Choices: map[string]string{"C": pon.ID}}))
	})
	t.Run("concealed_kong_and_end", func(t *testing.T) {
		s := fixture(t, "7m 7m 7m 7m 1p")
		o := choose(t, s.selfOptions(), "kan_closed")
		s = state(t, apply(t, encode(t, s), rulesdk.Input{Type: "action", ParticipantID: "A", OptionID: o.ID}))
		check(t, encode(t, s))
		s.finishDraw()
		check(t, encode(t, s))
	})
	t.Run("intermission_and_seat_rotation", func(t *testing.T) {
		s := state(t, initialized(t, "standard_16", 11))
		s.finishDraw()
		check(t, encode(t, s))
		check(t, apply(t, encode(t, s), rulesdk.Input{Type: "next_hand"}))
	})
	t.Run("winning_settlement", func(t *testing.T) {
		s := discard(t, fixture(t, "1z", "1m 2m 3m 4m 5m 6m 7m 8m 9m 1p 1p 1p 1z"), 0, "1z")
		hu := choose(t, s.reactionOptions(1), "hu")
		check(t, apply(t, encode(t, s), rulesdk.Input{Type: "resolve", Choices: map[string]string{"B": hu.ID}}))
	})
}

func TestProjectManyKeepsPrivateViewsAndBuffersIsolated(t *testing.T) {
	rule := Rule{}
	raw := initialized(t, "practice_1", 19)
	s := state(t, raw)
	viewers := projectionViewers()
	viewers = append(viewers, viewers[1])
	views, err := rule.ProjectMany(raw, viewers)
	if err != nil {
		t.Fatal(err)
	}
	for _, seat := range s.Seats {
		for _, tile := range seat.Hand {
			if bytes.Contains(views[0], []byte(tile.ID)) {
				t.Fatal("spectator contains a hidden tile identity")
			}
			for j, other := range viewers[1:5] {
				if other.ParticipantID != seat.ParticipantID && bytes.Contains(views[j+1], []byte(tile.ID)) {
					t.Fatalf("%s sees %s's hidden tile", other.ParticipantID, seat.ParticipantID)
				}
			}
		}
	}
	var owner ParticipantView
	ownerSeat := s.seatOf("A")
	if err := json.Unmarshal(views[1], &owner); err != nil || len(owner.Hand) != len(s.Seats[ownerSeat].Hand) {
		t.Fatalf("owner lost its own hand: %v", err)
	}
	// A hidden swap preserves all public facts. It must affect only its owner's
	// private hand; sharing one decoded snapshot must not widen any audience.
	hidden := s.Seats[ownerSeat].Hand[0]
	for i, tile := range s.Wall {
		if tile.ID == hidden.ID {
			s.Wall[i] = s.Wall[s.Head]
			break
		}
	}
	s.Seats[ownerSeat].Hand[0], s.Wall[s.Head] = s.Wall[s.Head], hidden
	changed, err := rule.ProjectMany(encode(t, s), viewers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(changed[0], views[0]) || bytes.Equal(changed[1], views[1]) {
		t.Fatal("hidden hand change did not respect audience boundaries")
	}
	for i := 2; i < 5; i++ {
		if !bytes.Equal(changed[i], views[i]) {
			t.Fatalf("hidden hand change affected participant %d", i)
		}
	}
	duplicateBefore := bytes.Clone(views[5])
	for i := range views[1] {
		views[1][i] = 'x'
	}
	if !bytes.Equal(views[5], duplicateBefore) {
		t.Fatal("duplicate viewer results share a mutable buffer")
	}
	fresh, err := rule.Project(raw, viewers[1])
	if err != nil || !bytes.Equal(fresh, duplicateBefore) {
		t.Fatalf("result mutation corrupted the snapshot: %v", err)
	}
}

func TestProjectManyRejectsInvalidSnapshotOrViewerWithoutPartialViews(t *testing.T) {
	rule := Rule{}
	raw := initialized(t, "practice_1", 3)
	invalid := state(t, raw)
	invalid.Seats[0].Hand[0] = invalid.Seats[1].Hand[0]
	for name, snapshot := range map[string]rulesdk.Snapshot{
		"malformed": []byte(`{"schema":`),
		"inventory": encode(t, invalid),
	} {
		t.Run(name, func(t *testing.T) {
			for _, viewers := range [][]rulesdk.Viewer{projectionViewers(), nil} {
				if views, err := rule.ProjectMany(snapshot, viewers); err == nil || views != nil {
					t.Fatal("invalid snapshot bypassed validation or returned partial views")
				}
			}
		})
	}
	for _, invalidViewer := range []rulesdk.Viewer{
		{Audience: "omniscient", ParticipantID: "A"},
		{Audience: rulesdk.ParticipantPrivate, ParticipantID: "intruder"},
	} {
		if views, err := rule.ProjectMany(raw, append(projectionViewers(), invalidViewer)); err == nil || views != nil {
			t.Fatal("invalid final viewer returned successful or partial views")
		}
	}
}

func BenchmarkProjectFiveViews(b *testing.B) {
	rule := Rule{}
	raw, err := rule.Init(config("practice_1"), bytes.Repeat([]byte{19}, 32))
	if err != nil {
		b.Fatal(err)
	}
	viewers := projectionViewers()
	b.Run("individual", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, viewer := range viewers {
				if _, err := rule.Project(raw, viewer); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := rule.ProjectMany(raw, viewers); err != nil {
				b.Fatal(err)
			}
		}
	})
}
