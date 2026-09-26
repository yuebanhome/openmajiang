package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuebanhome/openmajiang/internal/bots"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// Submit serializes against the same row lock as the timer. ACK is returned only
// after commit, including response intents that do not immediately change state.
func (s *Service) Submit(ctx context.Context, participant, controller string, a Action) (json.RawMessage, error) {
	if len(a.CommandID) < 8 || len(a.CommandID) > 128 {
		return nil, api(400, "INVALID_COMMAND_ID")
	}
	if a.Protocol != "" && a.Protocol != "1.0" {
		return nil, api(400, "UNSUPPORTED_PROTOCOL")
	}
	if a.ParticipantID != participant {
		return nil, api(403, "FORBIDDEN_SEAT")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	m, e := loadMatch(ctx, tx, a.MatchID, true)
	if e != nil {
		return nil, e
	}
	if m.ArchivedAt != nil {
		return nil, api(410, "MATCH_ARCHIVED")
	}
	var epoch int64
	var current string
	e = tx.QueryRow(ctx, `SELECT control_epoch,controller FROM platform_seats WHERE participant_id=$1 AND room_id=$2`, participant, m.RoomID).Scan(&epoch, &current)
	if e != nil {
		return nil, api(403, "FORBIDDEN_SEAT")
	}
	digest := sha256.Sum256(jsonBytes(a))
	hash := hex.EncodeToString(digest[:])
	var oldHash string
	var old json.RawMessage
	e = tx.QueryRow(ctx, `SELECT payload_hash,response FROM platform_commands WHERE participant_id=$1 AND match_id=$2 AND command_id=$3`, participant, m.ID, a.CommandID).Scan(&oldHash, &old)
	if e == nil {
		if hash != oldHash {
			return nil, api(409, "IDEMPOTENCY_CONFLICT")
		}
		return old, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if epoch != a.Epoch || current != controller {
		return nil, api(409, "STALE_CONTROL")
	}
	if m.Owner != s.id || m.LeaseExpired {
		return nil, api(503, "TABLE_RECOVERING")
	}
	if m.Status != "active" || m.Deadline == nil || !time.Now().Before(*m.Deadline) {
		return nil, api(409, "DECISION_CLOSED")
	}
	rule, e := s.rule(m.RulesetID, m.RulesetVersion)
	if e != nil {
		return nil, e
	}
	if rule.Manifest().ArtifactHash != m.Artifact {
		return nil, api(503, "RULE_ARTIFACT_MISMATCH")
	}
	flow, e := rule.Inspect(m.State)
	if e != nil {
		return nil, e
	}
	if a.HandID != handID(m.ID, flow.HandIndex) || a.Assignment != flow.HandIndex || a.WindowID != flow.WindowID {
		return nil, api(409, "DECISION_CLOSED")
	}
	var selected *rulesdk.Decision
	for i := range flow.Decisions {
		d := &flow.Decisions[i]
		if d.ParticipantID == participant && d.ID == a.DecisionID && d.Seat == a.Seat {
			selected = d
		}
	}
	if selected == nil {
		return nil, api(403, "FORBIDDEN_SEAT")
	}
	valid := false
	for _, o := range selected.Options {
		if o.ID == a.OptionID {
			valid = true
		}
	}
	if !valid {
		_ = s.recordInvalidAction(ctx, m, participant, a.CommandID, "INVALID_OPTION")
		return nil, api(400, "INVALID_OPTION")
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_commands WHERE participant_id=$1 AND match_id=$2 AND decision_id=$3)`, participant, m.ID, a.DecisionID).Scan(&exists)
	if e != nil {
		return nil, e
	}
	if exists {
		return nil, api(409, "ALREADY_SUBMITTED")
	}
	accepted := time.Now().UTC()
	if !accepted.Before(*m.Deadline) {
		return nil, api(409, "DECISION_CLOSED")
	}
	status := "applied"
	if flow.WindowKind == "reaction" {
		status = "recorded"
		m.Choices[participant] = a.OptionID
		_, e = tx.Exec(ctx, `UPDATE platform_matches SET choices=$2 WHERE id=$1 AND owner_id=$3 AND owner_epoch=$4`, m.ID, jsonBytes(m.Choices), s.id, m.OwnerEpoch)
	} else {
		e = s.advanceFromFlow(ctx, tx, &m, rule, rulesdk.Input{Type: "action", ParticipantID: participant, OptionID: a.OptionID}, flow)
	}
	if e != nil {
		return nil, e
	}
	if e = s.recordDecision(ctx, tx, m, a.DecisionID, participant, "accepted", accepted); e != nil {
		return nil, e
	}
	response := jsonBytes(map[string]any{"type": "command_ack", "command_id": a.CommandID, "decision_id": a.DecisionID, "status": status})
	_, e = tx.Exec(ctx, `INSERT INTO platform_commands(participant_id,match_id,command_id,payload_hash,decision_id,response,accepted_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, participant, m.ID, a.CommandID, hash, a.DecisionID, response, accepted)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return response, nil
}

// Each table has at most one scheduled worker. A slow rule/controller for one
// table must not prevent the next polling cycle from finding another deadline.
func (s *Service) Run(ctx context.Context) {
	var group sync.WaitGroup
	start := func(f func()) { group.Add(1); go func() { defer group.Done(); f() }() }
	start(func() { s.archiveWorker(ctx) })
	start(func() {
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.matchQueues(ctx)
			}
		}
	})
	start(func() {
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_, _ = s.pool.Exec(ctx, `UPDATE platform_matches SET owner_until=now()+interval '5 seconds' WHERE status='active' AND owner_id=$1 AND owner_until>=now() AND owner_until<now()+interval '3 seconds'`, s.id)
			}
		}
	})
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	defer group.Wait()
	runTableScheduler(ctx, ticker.C, s.dueMatches, s.tickMatch)
}

func (s *Service) dueMatches(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM platform_matches WHERE status='active' AND ((owner_id=$1 AND (next_run_at<=now() OR deadline_at<=now())) OR owner_until<now()) ORDER BY CASE WHEN deadline_at<=now() THEN 0 ELSE 1 END,LEAST(next_run_at,deadline_at),id LIMIT 200`, s.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var mid string
		if err = rows.Scan(&mid); err != nil {
			return nil, err
		}
		ids = append(ids, mid)
	}
	return ids, rows.Err()
}

// Polling discovers and reprioritizes due tables, but does not pace work already
// discovered. Refill a free slot immediately: requiring another poll for each
// group of eight adds 150ms to a 50-table burst even when the work itself is fast.
func runTableScheduler(ctx context.Context, polls <-chan time.Time, due func(context.Context) ([]string, error), work func(context.Context, string)) {
	const parallelTables = 8
	completed := make(chan string, parallelTables)
	running := map[string]bool{}
	var pending []string
	var group sync.WaitGroup
	defer group.Wait()
	dispatch := func() {
		for len(pending) > 0 && len(running) < parallelTables && ctx.Err() == nil {
			mid := pending[0]
			pending = pending[1:]
			running[mid] = true
			group.Add(1)
			go func() {
				defer group.Done()
				work(ctx, mid)
				// At most eight workers can complete after cancellation, so the
				// buffer also permits shutdown without a receiver in this loop.
				completed <- mid
			}()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case mid := <-completed:
			delete(running, mid)
			dispatch()
		case <-polls:
			for len(completed) > 0 {
				delete(running, <-completed)
			}
			ids, err := due(ctx)
			if err != nil {
				dispatch()
				continue
			}
			// Replace the queue even while every slot is occupied. This drops
			// externally advanced tables and promotes newly expired deadlines.
			// A table may still change after this read; work rechecks its state,
			// deadline and ownership while holding the match row lock.
			pending = pending[:0]
			queued := make(map[string]bool, len(ids))
			for _, mid := range ids {
				if !running[mid] && !queued[mid] {
					pending = append(pending, mid)
					queued[mid] = true
				}
			}
			dispatch()
		}
	}
}
func (s *Service) tickMatch(ctx context.Context, mid string) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	m, e := loadMatch(ctx, tx, mid, true)
	if e != nil || m.Status != "active" {
		return
	}
	initialSeq := m.Seq
	originalDeadline := m.Deadline
	recovered := false
	leaseExpired := m.LeaseExpired
	// A process that lost its database lease must recover explicitly too; it
	// cannot silently renew an expired lease and call server downtime a client timeout.
	if m.Owner != s.id || leaseExpired {
		if !leaseExpired {
			return
		}
		if time.Since(m.Updated) > 60*time.Second {
			s.abort(ctx, tx, m, "aborted_by_server")
			_ = tx.Commit(ctx)
			return
		}
		m.Owner = s.id
		m.OwnerEpoch++
		m.Interrupted = true
		recovered = true
		_, e = tx.Exec(ctx, `UPDATE platform_matches SET owner_id=$2,owner_epoch=$3,interrupted=true WHERE id=$1`, mid, s.id, m.OwnerEpoch)
		if e != nil {
			return
		}
	}
	rule, e := s.rule(m.RulesetID, m.RulesetVersion)
	if e != nil || rule.Manifest().ArtifactHash != m.Artifact {
		s.abort(ctx, tx, m, "missing_ruleset")
		_ = tx.Commit(ctx)
		return
	}
	flow, e := rule.Inspect(m.State)
	if e != nil {
		s.abort(ctx, tx, m, "invalid_state")
		_ = tx.Commit(ctx)
		return
	}
	now := time.Now()
	expired := m.Deadline != nil && !now.Before(*m.Deadline)
	if flow.WindowKind == "intermission" && expired {
		var leave bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE room_id=$1 AND active AND (leave_after_hand OR (kind IN ('human','bot') AND (connected_until IS NULL OR connected_until<now()))))`, m.RoomID).Scan(&leave)
		if leave {
			s.abort(ctx, tx, m, "ended_early")
			_ = tx.Commit(ctx)
			return
		}
		e = s.advanceFromFlow(ctx, tx, &m, rule, rulesdk.Input{Type: "next_hand"}, flow)
	} else if expired || recovered {
		var missing []rulesdk.Decision
		var missingIDs []string
		outcome := "timeout"
		if recovered {
			outcome = "recovery"
		}
		for _, d := range flow.Decisions {
			if _, exists := m.Choices[d.ParticipantID]; exists {
				continue
			}
			missing = append(missing, d)
			missingIDs = append(missingIDs, d.ParticipantID)
			if !recovered {
				if e = s.recordBotErrorTx(ctx, tx, d.ParticipantID, m.ID, "timeout:"+d.ID, "DECISION_TIMEOUT"); e != nil {
					return
				}
			}
		}
		if e = s.recordDecisions(ctx, tx, m, missing, outcome, now); e != nil {
			return
		}
		if !recovered && len(missingIDs) > 0 {
			column := "self_timeouts"
			if flow.WindowKind == "reaction" {
				column = "reaction_timeouts"
			}
			if _, e = tx.Exec(ctx, "UPDATE platform_seats SET "+column+"="+column+"+1 WHERE participant_id=ANY($1::text[])", missingIDs); e != nil {
				return
			}
		}

		if flow.WindowKind == "reaction" {
			e = s.advanceFromFlow(ctx, tx, &m, rule, rulesdk.Input{Type: "resolve", Choices: m.Choices}, flow)
		} else if flow.WindowKind == "self" {
			pid := ""
			if len(flow.Decisions) > 0 {
				pid = flow.Decisions[0].ParticipantID
			}
			e = s.advanceFromFlow(ctx, tx, &m, rule, rulesdk.Input{Type: "timeout", ParticipantID: pid}, flow)
		}
	} else if flow.WindowKind == "self" || flow.WindowKind == "reaction" {
		rows, err := tx.Query(ctx, `SELECT participant_id,builtin_strategy FROM platform_seats WHERE room_id=$1 AND kind='builtin' AND active`, m.RoomID)
		if err != nil {
			return
		}
		strategies := map[string]string{}
		for rows.Next() {
			var pid, strategy string
			if err = rows.Scan(&pid, &strategy); err != nil {
				rows.Close()
				return
			}
			strategies[pid] = strategy
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return
		}
		var chosen []rulesdk.Decision
		var chosenAt time.Time
		for _, d := range flow.Decisions {
			if _, ok := m.Choices[d.ParticipantID]; ok {
				continue
			}
			strategy, ok := strategies[d.ParticipantID]
			if !ok {
				continue
			}
			view, err := rule.Project(m.State, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: d.ParticipantID})
			if err != nil {
				return
			}
			choice, err := bots.Choose(strategy, view, d.Options)
			if err != nil {
				return
			}
			if choice == "" {
				continue
			}
			chosen = append(chosen, d)
			chosenAt = time.Now()
			if flow.WindowKind == "reaction" {
				m.Choices[d.ParticipantID] = choice
			} else {
				e = s.advanceFromFlow(ctx, tx, &m, rule, rulesdk.Input{Type: "action", ParticipantID: d.ParticipantID, OptionID: choice}, flow)
				if e != nil {
					return
				}
				break
			}
		}
		if e = s.recordDecisions(ctx, tx, m, chosen, "builtin", chosenAt); e != nil {
			return
		}
	}

	if e == nil && m.Seq == initialSeq && m.Deadline != nil {
		_, e = tx.Exec(ctx, `UPDATE platform_matches SET next_run_at=$2,choices=$3,owner_until=now()+interval '5 seconds' WHERE id=$1`, m.ID, m.Deadline, jsonBytes(m.Choices))
	}
	if e == nil {
		if err := tx.Commit(ctx); err == nil && expired && originalDeadline != nil && m.Seq != initialSeq && s.cfg.ObserveTimerLag != nil {
			s.cfg.ObserveTimerLag(time.Since(*originalDeadline))
		}
	}
}

// The hosted controller receives only this seat's legal options, never a rule snapshot.
func fallback(d rulesdk.Decision, window string) string {
	for _, o := range d.Options {
		if o.Type == "hu" || o.Type == "replace_flower" {
			return o.ID
		}
	}
	if window == "reaction" {
		for _, o := range d.Options {
			if o.Type == "pass" {
				return o.ID
			}
		}
	}
	for _, o := range d.Options {
		if o.Type == "discard" {
			return o.ID
		}
	}
	if len(d.Options) > 0 {
		return d.Options[0].ID
	}
	return ""
}
func (s *Service) abort(ctx context.Context, tx pgx.Tx, m match, reason string) {
	_, _ = tx.Exec(ctx, `UPDATE platform_matches SET status=$2,deadline_at=NULL,interrupted=true,updated_at=now() WHERE id=$1`, m.ID, reason)
	_, _ = tx.Exec(ctx, `UPDATE platform_rooms SET status='ended' WHERE id=$1`, m.RoomID)
	_, _ = tx.Exec(ctx, `UPDATE platform_seats SET active=false WHERE room_id=$1`, m.RoomID)
}
