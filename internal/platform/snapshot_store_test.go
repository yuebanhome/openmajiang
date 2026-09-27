package platform

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPGSnapshotReadSeparatesPublicProjectionFromRuleState(t *testing.T) {
	s := recoveryService(t, mcr.New())
	room, mid := recoveryRoom(t, s, mcr.New(), "practice_1")
	ctx := context.Background()
	public, err := s.loadSnapshot(ctx, room.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(public.match.State) != 0 {
		t.Fatal("public read fetched authoritative state")
	}
	if public.match.ID != mid || public.match.Seq != 1 || public.index != 1 {
		t.Fatal("snapshot metadata does not match committed projection")
	}
	if _, err = ValidateSpectator(public.view); err != nil {
		t.Fatal(err)
	}
	pid := room.Seats[0].ParticipantID
	private, err := s.loadSnapshot(ctx, room.ID, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.match.State) == 0 || private.epoch != room.Seats[0].Epoch {
		t.Fatal("private snapshot lost state or controller epoch")
	}
	var projected map[string]json.RawMessage
	if err = json.Unmarshal(private.view, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected["hand"]) == 0 {
		t.Fatal("private projection lost participant hand")
	}
	if _, err = s.pool.Exec(ctx, `UPDATE platform_seats SET active=false WHERE room_id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	snapshot, err := s.snapshotUncached(req, room.ID, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot["room"].(Room).Seats) != 0 || wireInt64(snapshot["control_epoch"]) != private.epoch {
		t.Fatal("ended roster changed historical participant authorization")
	}
}
