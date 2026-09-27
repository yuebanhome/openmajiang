package platform

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPGInvalidOptionRecordsOnceWithOneConnection(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	initial := recoveryMatch(t, s, mid)
	flow := recoveryFlow(t, rule, initial)
	action := recoveryAction(t, s, initial, flow, flow.Decisions[0], "")
	action.OptionID = "not-a-legal-option"
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET deadline_at=now()+interval '1 minute',owner_until=now()+interval '1 minute' WHERE id=$1`, mid); err != nil {
		t.Fatal(err)
	}
	// Reuse this fixture's isolated schema through a pool with exactly one slot.
	// A rejected command cannot acquire a second slot to record its statistics.
	cfg := s.pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	one, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(one.Close)
	s.pool = one
	for attempt := 0; attempt < 3; attempt++ {
		if attempt == 2 {
			action.CommandID = id("command")
		}
		submitCtx, cancel := context.WithTimeout(ctx, time.Second)
		ack, err := s.Submit(submitCtx, action.ParticipantID, "ctrl-"+action.ParticipantID, action)
		contextErr := submitCtx.Err()
		cancel()
		if contextErr != nil {
			t.Fatalf("invalid-option reporting waited for its own pool connection: %v", contextErr)
		}
		if recoveryCode(err) != "INVALID_OPTION" || len(ack) != 0 {
			t.Fatalf("invalid option response: %s %v", ack, err)
		}
	}
	var invalid, commands int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_stat_invalid_actions WHERE match_id=$1 AND participant_id=$2 AND code='INVALID_OPTION'`, mid, action.ParticipantID).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_commands WHERE match_id=$1`, mid).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if invalid != 2 || commands != 0 {
		t.Fatalf("rejected command accounting: invalid=%d commands=%d", invalid, commands)
	}
	unchanged := recoveryMatch(t, s, mid)
	if unchanged.Seq != initial.Seq || !bytes.Equal(unchanged.State, initial.State) || !bytes.Equal(jsonBytes(unchanged.Choices), jsonBytes(initial.Choices)) {
		t.Fatal("invalid option changed authoritative state")
	}
}
