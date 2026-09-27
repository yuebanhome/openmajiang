package platform

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPGLeaseRenewalSkipsBusyTableAndPreservesEligibility(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	ctx := context.Background()
	matches := map[string]string{}
	for _, name := range []string{"busy", "free", "expired", "foreign", "inactive", "fresh"} {
		_, matches[name] = recoveryRoom(t, s, rule, "practice_1")
	}
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET
 owner_until=CASE WHEN id=$1 THEN now()-interval '1 second' WHEN id=$4 THEN now()+interval '1 hour' ELSE now()+interval '2500 milliseconds' END,
 owner_id=CASE WHEN id=$2 THEN 'another-node' ELSE owner_id END,
 status=CASE WHEN id=$3 THEN 'completed' ELSE status END`, matches["expired"], matches["foreign"], matches["inactive"], matches["fresh"]); err != nil {
		t.Fatal(err)
	}
	leases := func() map[string]time.Time {
		t.Helper()
		out := map[string]time.Time{}
		for name, mid := range matches {
			var until time.Time
			if err := s.pool.QueryRow(ctx, `SELECT owner_until FROM platform_matches WHERE id=$1`, mid).Scan(&until); err != nil {
				t.Fatal(err)
			}
			out[name] = until
		}
		return out
	}
	before := leases()
	busy, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Rollback(ctx)
	if _, err = loadMatch(ctx, busy, matches["busy"], true); err != nil {
		t.Fatal(err)
	}
	// The lock remains held for the whole renewal. A plain multi-row UPDATE
	// cannot finish, regardless of which eligible row it happens to visit first.
	renewCtx, cancel := context.WithTimeout(ctx, time.Second)
	err = s.renewOwnedLeases(renewCtx)
	cancel()
	if err != nil {
		t.Fatalf("unrelated lease renewal waited for a busy table: %v", err)
	}
	after := leases()
	for name, original := range before {
		if name == "free" {
			if !after[name].After(original) {
				t.Fatal("unlocked eligible lease was not renewed")
			}
		} else if !after[name].Equal(original) {
			t.Fatalf("renewal changed %s lease", name)
		}
	}
	if err = busy.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.renewOwnedLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if !leases()["busy"].After(before["busy"]) {
		t.Fatal("later heartbeat did not retry the unlocked eligible table")
	}
}

func TestPGTimerSkipsBusyTableThenAdvancesOnce(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	initial, _ := recoveryReaction(t, s, rule, mid)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET deadline_at=now()-interval '1 millisecond',owner_until=now()+interval '5 seconds' WHERE id=$1`, mid); err != nil {
		t.Fatal(err)
	}
	observed := 0
	s.cfg.ObserveTimerLag = func(time.Duration) { observed++ }
	busy, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Rollback(ctx)
	if _, err = loadMatch(ctx, busy, mid, true); err != nil {
		t.Fatal(err)
	}
	// A timer must release its connection while this lock is still held.
	// Returning only because its context expired is the old blocking behavior.
	tickCtx, cancel := context.WithTimeout(ctx, time.Second)
	s.tickMatch(tickCtx, mid)
	contextErr := tickCtx.Err()
	cancel()
	if contextErr != nil {
		t.Fatalf("timer waited for a busy match: %v", contextErr)
	}
	unchanged := recoveryMatch(t, s, mid)
	if unchanged.Seq != initial.Seq || !bytes.Equal(unchanged.State, initial.State) || observed != 0 {
		t.Fatal("skipped timer changed state or reported an adjudication")
	}
	if err = busy.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	s.tickMatch(ctx, mid)
	advanced := recoveryMatch(t, s, mid)
	if advanced.Seq != initial.Seq+1 || advanced.OwnerEpoch != initial.OwnerEpoch || advanced.Interrupted || observed != 1 {
		t.Fatal("retry did not adjudicate the expired window once under the existing lease")
	}
	s.tickMatch(ctx, mid)
	if retried := recoveryMatch(t, s, mid); retried.Seq != advanced.Seq || observed != 1 {
		t.Fatal("subsequent timer repeated the adjudication")
	}
}
