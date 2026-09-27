package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/internal/testplugins/threeplayer"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// The fixture has three seats and plugin-defined actions. Scheduling must use
// the current decision's participant identity, not MCR seat/phase conventions.
func nextRunRoom(t *testing.T, s *Service, rule rulesdk.Rule, kinds []string) string {
	t.Helper()
	ctx := context.Background()
	owner := auth.User{ID: id("next_run_owner"), Name: "Scheduling owner", Verified: true, Status: "active"}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test-only',true)`, owner.ID, owner.ID+"@example.invalid", owner.Name); err != nil {
		t.Fatal(err)
	}
	manifest := rule.Manifest()
	room, _, err := s.create(ctx, owner, createRequest{Name: "Scheduling fixture", Mode: "mixed", RuleID: manifest.ID, RuleVersion: manifest.Version, Format: manifest.Formats[0], Capacity: len(kinds), InviteOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for seat, kind := range kinds {
		pid, uid := room.Seats[0].ParticipantID, owner.ID
		if seat > 0 {
			pid, uid = id("next_run_participant"), id("next_run_user")
			if _, err = s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,'Scheduling player','test-only',true)`, uid, uid+"@example.invalid"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.pool.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order) VALUES($1,$2,$3,$4,$5,$6)`, pid, room.ID, uid, fmt.Sprintf("Player %d", seat), kind, seat); err != nil {
				t.Fatal(err)
			}
		} else if _, err = s.pool.Exec(ctx, `UPDATE platform_seats SET kind=$2 WHERE participant_id=$1`, pid, kind); err != nil {
			t.Fatal(err)
		}
		if kind == "bot" {
			bid, credential, session := id("next_run_bot"), id("next_run_key"), id("next_run_session")
			if _, err = s.pool.Exec(ctx, `INSERT INTO platform_bots(id,owner_id,name) VALUES($1,$2,'External scheduling bot')`, bid, uid); err != nil {
				t.Fatal(err)
			}
			if _, err = s.pool.Exec(ctx, `INSERT INTO platform_bot_credentials(id,bot_id,secret_hash) VALUES($1,$2,$3)`, credential, bid, id("credential_hash")); err != nil {
				t.Fatal(err)
			}
			capabilities := jsonBytes([]map[string]string{{"id": manifest.ID, "version": manifest.Version}})
			if _, err = s.pool.Exec(ctx, `INSERT INTO platform_bot_sessions(id,bot_id,credential_id,secret_hash,expires_at,capabilities) VALUES($1,$2,$3,$4,now()+interval '1 hour',$5)`, session, bid, credential, id("session_hash"), capabilities); err != nil {
				t.Fatal(err)
			}
			if _, err = s.pool.Exec(ctx, `UPDATE platform_seats SET bot_id=$2,controller_session=$3 WHERE participant_id=$1`, pid, bid, session); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.pool.Exec(ctx, `UPDATE platform_seats SET ready=true,control_epoch=1,controller='ctrl-'||participant_id,builtin_strategy='random_legal',connected_until=now()+interval '1 hour' WHERE room_id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	mid, err := s.start(ctx, room.ID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mid
}

type nextRunClock struct {
	Next, OwnerUntil time.Time
	Deadline         *time.Time
	Xmin             string
	Seq              int64
	Due              bool
}

func nextRunRead(t *testing.T, s *Service, mid string) nextRunClock {
	t.Helper()
	var clock nextRunClock
	err := s.pool.QueryRow(context.Background(), `SELECT next_run_at,deadline_at,owner_until,xmin::text,seq,next_run_at<=now() FROM platform_matches WHERE id=$1`, mid).Scan(&clock.Next, &clock.Deadline, &clock.OwnerUntil, &clock.Xmin, &clock.Seq, &clock.Due)
	if err != nil {
		t.Fatal(err)
	}
	return clock
}

func nextRunAssertDiscovered(t *testing.T, s *Service, mid string, want bool) {
	t.Helper()
	ids, err := s.dueMatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range ids {
		found = found || candidate == mid
	}
	if found != want {
		t.Fatalf("match discovery=%v, want %v", found, want)
	}
}

func nextRunAssertDeadline(t *testing.T, s *Service, mid string) {
	t.Helper()
	clock := nextRunRead(t, s, mid)
	if clock.Deadline == nil || !clock.Next.Equal(*clock.Deadline) || clock.Due {
		t.Fatalf("idle decision did not sleep until its deadline: %+v", clock)
	}
	nextRunAssertDiscovered(t, s, mid, false)
}

func nextRunAssertNoWrite(t *testing.T, s *Service, mid string) {
	t.Helper()
	before := nextRunRead(t, s, mid)
	s.tickMatch(context.Background(), mid)
	after := nextRunRead(t, s, mid)
	if after.Xmin != before.Xmin || after.Seq != before.Seq || !after.Next.Equal(before.Next) || !after.OwnerUntil.Equal(before.OwnerUntil) {
		t.Fatalf("non-due timer rewrote the match: before=%+v after=%+v", before, after)
	}
}

func TestPGNextRunWaitsForExternalDecisionsAndIntermission(t *testing.T) {
	rule := threeplayer.New()
	s := recoveryService(t, rule)
	for _, test := range []struct {
		name  string
		kinds []string
	}{
		{"human", []string{"human", "human", "human"}},
		{"external_bot", []string{"bot", "human", "human"}},
		{"human_with_unrelated_builtins", []string{"human", "builtin", "builtin"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mid := nextRunRoom(t, s, rule, test.kinds)
			// Initial scheduling must not wake a human/external controller, even
			// when other seats at the same table have built-in controllers.
			nextRunAssertDeadline(t, s, mid)
			nextRunAssertNoWrite(t, s, mid)
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "timeout"})
			flow := recoveryFlow(t, rule, recoveryMatch(t, s, mid))
			if flow.WindowKind != "intermission" || len(flow.Decisions) != 0 {
				t.Fatal("fixture did not reach the no-decision intermission")
			}
			nextRunAssertDeadline(t, s, mid)
			nextRunAssertNoWrite(t, s, mid)
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "next_hand"})
			// Exercise the UPDATE path as well as INSERT: this is another
			// external decision, so the new window still waits for its deadline.
			nextRunAssertDeadline(t, s, mid)
			nextRunAssertNoWrite(t, s, mid)
		})
	}
}

func TestPGNextRunBuiltinDecisionRunsEarlyWithPluginLegalAction(t *testing.T) {
	rule := threeplayer.New()
	s := recoveryService(t, rule)
	mid := nextRunRoom(t, s, rule, []string{"builtin", "human", "human"})
	for hand := 1; hand <= 2; hand++ {
		before := recoveryMatch(t, s, mid)
		flow := recoveryFlow(t, rule, before)
		clock := nextRunRead(t, s, mid)
		if !clock.Due || clock.Deadline == nil || !clock.Next.Before(*clock.Deadline) {
			t.Fatalf("built-in decision was not scheduled before its deadline: %+v", clock)
		}
		nextRunAssertDiscovered(t, s, mid, true)
		s.tickMatch(context.Background(), mid)
		after := recoveryMatch(t, s, mid)
		if after.Seq != before.Seq+1 {
			t.Fatal("due built-in controller did not advance its decision")
		}
		var raw json.RawMessage
		if err := s.pool.QueryRow(context.Background(), `SELECT input FROM platform_events WHERE match_id=$1 AND seq=$2`, mid, after.Seq).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var input rulesdk.Input
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		decision := flow.Decisions[0]
		if input.Type != "action" || input.ParticipantID != decision.ParticipantID || input.OptionID != decision.Options[0].ID {
			t.Fatalf("built-in did not use the plugin's legal option: %+v", input)
		}
		want, err := rule.Apply(before.State, input)
		if err != nil {
			t.Fatal(err)
		}
		// PostgreSQL jsonb normalizes object order and spacing; compare the
		// state values rather than the original serialization bytes.
		var expected, actual any
		if err = json.Unmarshal(want.State, &expected); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(after.State, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected, actual) {
			t.Fatal("built-in state differs from its committed legal action")
		}
		if hand == 1 {
			nextRunAssertDeadline(t, s, mid)
			nextRunAssertNoWrite(t, s, mid)
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "next_hand"})
		} else {
			if after.Status != "completed" || after.Deadline != nil {
				t.Fatal("plugin end was not persisted")
			}
			// next_run_at is NOT NULL even after deadline_at becomes NULL.
			nextRunRead(t, s, mid)
			nextRunAssertDiscovered(t, s, mid, false)
			nextRunAssertNoWrite(t, s, mid)
		}
	}
}

func TestPGNextRunStaleQueuedTimerDoesNotRewriteSubmittedWindow(t *testing.T) {
	rule := threeplayer.New()
	s := recoveryService(t, rule)
	mid := nextRunRoom(t, s, rule, []string{"human", "human", "human"})
	if _, err := s.pool.Exec(context.Background(), `UPDATE platform_matches SET next_run_at=now() WHERE id=$1`, mid); err != nil {
		t.Fatal(err)
	}
	// Discovery has queued this ID, but the player's command advances it
	// before the queued worker acquires the match lock.
	nextRunAssertDiscovered(t, s, mid, true)
	m := recoveryMatch(t, s, mid)
	flow := recoveryFlow(t, rule, m)
	recoverySubmit(t, s, recoveryAction(t, s, m, flow, flow.Decisions[0], ""))
	nextRunAssertDeadline(t, s, mid)
	nextRunAssertNoWrite(t, s, mid)
}

func TestPGNextRunDeadlineAndExpiredLeaseRemainDiscoverable(t *testing.T) {
	rule := threeplayer.New()
	s := recoveryService(t, rule)
	for _, reason := range []string{"deadline", "own_expired_lease", "foreign_expired_lease"} {
		t.Run(reason, func(t *testing.T) {
			mid := nextRunRoom(t, s, rule, []string{"human", "human", "human"})
			before := recoveryMatch(t, s, mid)
			recovery := reason != "deadline"
			_, err := s.pool.Exec(context.Background(), `UPDATE platform_matches SET
 next_run_at=now()+interval '1 hour',
 deadline_at=CASE WHEN $2 THEN deadline_at ELSE now()-interval '1 millisecond' END,
 owner_until=CASE WHEN $2 THEN now()-interval '1 second' ELSE now()+interval '1 hour' END,
 owner_id=CASE WHEN $3 THEN 'another-node' ELSE owner_id END
WHERE id=$1`, mid, recovery, reason == "foreign_expired_lease")
			if err != nil {
				t.Fatal(err)
			}
			nextRunAssertDiscovered(t, s, mid, true)
			s.tickMatch(context.Background(), mid)
			after := recoveryMatch(t, s, mid)
			wantEpoch := before.OwnerEpoch
			if recovery {
				wantEpoch++
			}
			if after.Seq != before.Seq+1 || after.Owner != s.id || after.OwnerEpoch != wantEpoch || after.Interrupted != recovery {
				t.Fatalf("due timer did not advance with the expected lease fence: before=%+v after=%+v", before, after)
			}
			var outcome string
			if err = s.pool.QueryRow(context.Background(), `SELECT outcome FROM platform_stat_decisions WHERE match_id=$1 AND decision_id=$2`, mid, recoveryFlow(t, rule, before).Decisions[0].ID).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "timeout"
			if recovery {
				wantOutcome = "recovery"
			}
			if outcome != wantOutcome {
				t.Fatalf("decision outcome=%q, want %q", outcome, wantOutcome)
			}
			nextRunAssertDeadline(t, s, mid)
			nextRunAssertNoWrite(t, s, mid)
		})
	}
}
