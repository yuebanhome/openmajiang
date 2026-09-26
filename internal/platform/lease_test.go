package platform

import (
	"context"
	"testing"

	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPGExpiredSameOwnerLeaseRequiresRecoveryBeforeNewCommand(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	m, flow := recoveryReaction(t, s, rule, mid)
	action := recoveryAction(t, s, m, flow, flow.Decisions[0], "pass")
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET owner_until=now()-interval '1 second',deadline_at=now()+interval '1 minute',updated_at=now()-interval '2 seconds' WHERE id=$1`, mid); err != nil {
		t.Fatal(err)
	}
	ack, err := s.Submit(ctx, action.ParticipantID, "ctrl-"+action.ParticipantID, action)
	if recoveryCode(err) != "TABLE_RECOVERING" || len(ack) != 0 {
		t.Fatalf("expired owner acknowledged new command: %s %v", ack, err)
	}
	var count int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_commands WHERE match_id=$1 AND command_id=$2`, mid, action.CommandID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected command persisted: %d %v", count, err)
	}
	unchanged := recoveryMatch(t, s, mid)
	if unchanged.Seq != m.Seq || len(unchanged.Choices) != 0 || unchanged.Interrupted {
		t.Fatal("rejected command changed the match before recovery")
	}
	s.tickMatch(ctx, mid)
	recovered := recoveryMatch(t, s, mid)
	if recovered.Owner != s.id || recovered.OwnerEpoch != m.OwnerEpoch+1 || !recovered.Interrupted || recovered.Seq != m.Seq+1 || recovered.LeaseExpired {
		t.Fatal("same-owner recovery did not establish a new fenced window")
	}
	if ack, err = s.Submit(ctx, action.ParticipantID, "ctrl-"+action.ParticipantID, action); recoveryCode(err) != "DECISION_CLOSED" || len(ack) != 0 {
		t.Fatalf("old window accepted after recovery: %s %v", ack, err)
	}
	nextFlow := recoveryFlow(t, rule, recovered)
	next := recoveryAction(t, s, recovered, nextFlow, nextFlow.Decisions[0], "discard")
	recoverySubmit(t, s, next)
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_commands WHERE match_id=$1 AND command_id=$2`, mid, next.CommandID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new recovered window did not persist command: %d %v", count, err)
	}
}
