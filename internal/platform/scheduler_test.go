package platform

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

type stalledApplyRule struct {
	rulesdk.Rule
	target           string
	entered, release chan struct{}
	once             sync.Once
}

func (r *stalledApplyRule) Apply(state rulesdk.Snapshot, input rulesdk.Input) (rulesdk.Transition, error) {
	if string(state) == r.target {
		r.once.Do(func() { close(r.entered); <-r.release })
	}
	return r.Rule.Apply(state, input)
}

func TestPGSlowTableDoesNotBlockNewlyDueTable(t *testing.T) {
	rule := &stalledApplyRule{Rule: mcr.New(), entered: make(chan struct{}), release: make(chan struct{})}
	s := recoveryService(t, rule)
	_, slow := recoveryRoom(t, s, rule, "practice_1")
	_, fast := recoveryRoom(t, s, rule, "practice_1")
	rule.target = string(recoveryMatch(t, s, slow).State)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET next_run_at=now()+interval '1 hour',deadline_at=now()+interval '1 hour' WHERE id IN ($1,$2)`, slow, fast); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET deadline_at=now()-interval '1 millisecond' WHERE id=$1`, slow); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { close(rule.release); cancel(); <-done }()
	select {
	case <-rule.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("slow table did not enter rule")
	}
	if _, err := s.pool.Exec(ctx, `UPDATE platform_matches SET deadline_at=now()-interval '1 millisecond' WHERE id=$1`, fast); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		var seq int64
		if err := s.pool.QueryRow(ctx, `SELECT seq FROM platform_matches WHERE id=$1`, fast).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if seq > 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("new deadline waited for unrelated stalled table")
}
