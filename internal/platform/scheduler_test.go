package platform

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func schedulerStarted(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case mid := <-started:
		return mid
	case <-time.After(3 * time.Second):
		t.Fatal("due table did not start without another poll")
		return ""
	}
}

func schedulerStopped(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not join its workers after cancellation")
	}
}

func TestTableSchedulerDrainsDueBurstWithoutExtraPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time, 1)
	started := make(chan string, 50)
	release := make(chan struct{})
	done := make(chan struct{})
	var active, peak, discoveries atomic.Int64
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = fmt.Sprintf("table-%d", i)
	}
	go func() {
		defer close(done)
		runTableScheduler(ctx, polls, func(context.Context) ([]string, error) {
			discoveries.Add(1)
			return ids, nil
		}, func(ctx context.Context, mid string) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- mid
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() { schedulerStopped(t, cancel, done) })
	// One discovery must drain all 50 tables. Advancing the poll clock again
	// would hide the regression where each group waited another 25ms.
	polls <- time.Now()
	seen := map[string]bool{}
	for i := 0; i < len(ids); i++ {
		if i >= 8 {
			release <- struct{}{}
		}
		mid := schedulerStarted(t, started)
		if seen[mid] {
			t.Fatalf("table %s was scheduled twice", mid)
		}
		seen[mid] = true
	}
	schedulerStopped(t, cancel, done)
	if discoveries.Load() != 1 || peak.Load() != 8 || active.Load() != 0 {
		t.Fatalf("discoveries=%d peak workers=%d remaining workers=%d", discoveries.Load(), peak.Load(), active.Load())
	}
}

func TestTableSchedulerRefreshesPendingWhileBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time, 1)
	started := make(chan string, 20)
	release := make(chan struct{})
	refreshed := make(chan struct{})
	refreshApplied := make(chan struct{})
	done := make(chan struct{})
	ids := make([]string, 8)
	for i := range ids {
		ids[i] = fmt.Sprintf("running-%d", i)
	}
	go func() {
		defer close(done)
		poll := 0
		runTableScheduler(ctx, polls, func(context.Context) ([]string, error) {
			poll++
			if poll == 1 {
				return append(append([]string{}, ids...), "stale", "retained"), nil
			}
			if poll == 2 {
				close(refreshed)
			}
			if poll == 3 {
				// A subsequent query can only start after the scheduler has
				// applied the previous result to its pending queue.
				close(refreshApplied)
			}
			// Simulate a newly expired deadline and an external submission
			// making the old pending table no longer due. Running tables must
			// not reenter the queue, even if the database still lists them.
			return append(append([]string{}, ids...), "urgent", "retained", "urgent"), nil
		}, func(ctx context.Context, mid string) {
			started <- mid
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() { schedulerStopped(t, cancel, done) })
	polls <- time.Now()
	for range ids {
		schedulerStarted(t, started)
	}
	polls <- time.Now()
	select {
	case <-refreshed:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not refresh deadlines while all eight slots were busy")
	}
	refreshPoll := time.NewTicker(time.Millisecond)
	defer refreshPoll.Stop()
	refreshTimeout := time.NewTimer(3 * time.Second)
	defer refreshTimeout.Stop()
waitForRefresh:
	for {
		select {
		case <-refreshApplied:
			break waitForRefresh
		case now := <-refreshPoll.C:
			select {
			case polls <- now:
			default:
			}
		case <-refreshTimeout.C:
			t.Fatal("scheduler did not apply the refreshed discovery")
		}
	}
	for _, want := range []string{"urgent", "retained"} {
		release <- struct{}{}
		if got := schedulerStarted(t, started); got != want {
			t.Fatalf("started %s, want %s after priority refresh", got, want)
		}
	}
}

func TestTableSchedulerBlockedDiscoveryDoesNotStopPendingWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time)
	started := make(chan string, 32)
	releaseWork := make(chan struct{})
	discoveryBlocked := make(chan struct{})
	releaseDiscovery := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int64
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("table-%d", i)
	}
	go func() {
		defer close(done)
		runTableScheduler(ctx, polls, func(ctx context.Context) ([]string, error) {
			switch calls.Add(1) {
			case 1:
				return ids, nil
			case 2:
				close(discoveryBlocked)
				select {
				case <-releaseDiscovery:
					// Represents a snapshot read before the workers advanced.
					return ids, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			default:
				return []string{"fresh"}, nil
			}
		}, func(ctx context.Context, mid string) {
			started <- mid
			select {
			case <-releaseWork:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() { schedulerStopped(t, cancel, done) })
	poll := func() {
		t.Helper()
		select {
		case polls <- time.Now():
		case <-time.After(3 * time.Second):
			t.Fatal("discovery blocked the dispatch loop")
		}
	}
	poll()
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		seen[schedulerStarted(t, started)] = true
	}
	poll()
	select {
	case <-discoveryBlocked:
	case <-time.After(3 * time.Second):
		t.Fatal("second discovery did not start")
	}
	// Polls must be coalesced rather than creating unbounded pool waiters.
	for i := 0; i < 10; i++ {
		poll()
	}
	if calls.Load() != 2 {
		t.Fatalf("overlapping discovery calls: %d", calls.Load())
	}
	for i := 8; i < len(ids); i++ {
		releaseWork <- struct{}{}
		mid := schedulerStarted(t, started)
		if seen[mid] {
			t.Fatalf("table %s ran twice", mid)
		}
		seen[mid] = true
	}
	// All 20 tables started despite the second discovery still waiting.
	close(releaseWork)
	close(releaseDiscovery)
	// Wait for a fresh query. Completed tables returned by the stale query
	// must not be requeued even after their workers leave the running map.
	until := time.NewTimer(3 * time.Second)
	defer until.Stop()
	for {
		select {
		case mid := <-started:
			if mid != "fresh" {
				t.Fatalf("stale discovery requeued completed table %s", mid)
			}
			return
		case polls <- time.Now():
		case <-until.C:
			t.Fatal("fresh work did not start after discovery recovered")
		}
	}
}

func TestTableSchedulerCancellationDoesNotStartPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time, 1)
	started := make(chan string, 50)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTableScheduler(ctx, polls, func(context.Context) ([]string, error) {
			ids := make([]string, 50)
			for i := range ids {
				ids[i] = fmt.Sprintf("table-%d", i)
			}
			return ids, nil
		}, func(ctx context.Context, mid string) {
			started <- mid
			<-ctx.Done()
		})
	}()
	t.Cleanup(func() { schedulerStopped(t, cancel, done) })
	polls <- time.Now()
	for i := 0; i < 8; i++ {
		schedulerStarted(t, started)
	}
	schedulerStopped(t, cancel, done)
	select {
	case mid := <-started:
		t.Fatalf("pending table %s started after cancellation", mid)
	default:
	}
}

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
