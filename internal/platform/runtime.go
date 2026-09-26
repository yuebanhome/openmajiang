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
	if m.Owner != s.id {
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
		e = s.advance(ctx, tx, &m, rule, rulesdk.Input{Type: "action", ParticipantID: participant, OptionID: a.OptionID})
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
func (s *Service) Run(ctx context.Context) {
	go s.archiveWorker(ctx)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}
func (s *Service) tick(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `UPDATE platform_matches SET owner_until=now()+interval '5 seconds' WHERE status='active' AND owner_id=$1 AND owner_until>=now() AND owner_until<now()+interval '3 seconds'`, s.id)
	rows, e := s.pool.Query(ctx, `SELECT id FROM platform_matches WHERE status='active' AND ((owner_id=$1 AND (next_run_at<=now() OR deadline_at<=now())) OR owner_until<now()) ORDER BY LEAST(next_run_at,deadline_at) LIMIT 200`, s.id)
	if e != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var mid string
		if rows.Scan(&mid) == nil {
			ids = append(ids, mid)
		}
	}
	rows.Close()
	// Different tables may progress together; each table remains fenced by its
	// PostgreSQL row lock. A bounded worker set prevents one busy table from
	// making unrelated deadlines wait for a full sequential scan.
	jobs := make(chan string, len(ids))
	for _, mid := range ids {
		jobs <- mid
	}
	close(jobs)
	workers := 8
	if len(ids) < workers {
		workers = len(ids)
	}
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for mid := range jobs {
				s.tickMatch(ctx, mid)
			}
		}()
	}
	group.Wait()
	s.matchQueues(ctx)
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
	var leaseExpired bool
	if tx.QueryRow(ctx, `SELECT owner_until<now() FROM platform_matches WHERE id=$1`, mid).Scan(&leaseExpired) != nil {
		return
	}
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
	_, e = tx.Exec(ctx, `UPDATE platform_matches SET owner_until=now()+interval '5 seconds' WHERE id=$1 AND owner_id=$2 AND owner_epoch=$3`, mid, s.id, m.OwnerEpoch)
	if e != nil {
		return
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
		e = s.advance(ctx, tx, &m, rule, rulesdk.Input{Type: "next_hand"})
	} else if expired || recovered {
		if expired || recovered {
			for _, d := range flow.Decisions {
				if _, exists := m.Choices[d.ParticipantID]; !exists {
					outcome := "timeout"
					if recovered {
						outcome = "recovery"
					}
					if e = s.recordDecision(ctx, tx, m, d.ID, d.ParticipantID, outcome, time.Now()); e != nil {
						return
					}
					if recovered {
						continue
					}
					if e = s.recordBotErrorTx(ctx, tx, d.ParticipantID, m.ID, "timeout:"+d.ID, "DECISION_TIMEOUT"); e != nil {
						return
					}
					column := "self_timeouts"
					if flow.WindowKind == "reaction" {
						column = "reaction_timeouts"
					}
					_, e = tx.Exec(ctx, "UPDATE platform_seats SET "+column+"="+column+"+1 WHERE participant_id=$1", d.ParticipantID)
					if e != nil {
						return
					}
				}
			}
		}
		if flow.WindowKind == "reaction" {
			e = s.advance(ctx, tx, &m, rule, rulesdk.Input{Type: "resolve", Choices: m.Choices})
		} else if flow.WindowKind == "self" {
			pid := ""
			if len(flow.Decisions) > 0 {
				pid = flow.Decisions[0].ParticipantID
			}
			e = s.advance(ctx, tx, &m, rule, rulesdk.Input{Type: "timeout", ParticipantID: pid})
		}
	} else if flow.WindowKind == "self" || flow.WindowKind == "reaction" {
		for _, d := range flow.Decisions {
			if _, ok := m.Choices[d.ParticipantID]; ok {
				continue
			}
			var kind string
			var strategy string
			if tx.QueryRow(ctx, `SELECT kind,builtin_strategy FROM platform_seats WHERE participant_id=$1`, d.ParticipantID).Scan(&kind, &strategy) != nil {
				continue
			}
			if kind != "builtin" {
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
			if e = s.recordDecision(ctx, tx, m, d.ID, d.ParticipantID, "builtin", time.Now()); e != nil {
				return
			}
			if flow.WindowKind == "reaction" {
				m.Choices[d.ParticipantID] = choice
				_, e = tx.Exec(ctx, `UPDATE platform_matches SET choices=$2 WHERE id=$1`, m.ID, jsonBytes(m.Choices))
			} else {
				e = s.advance(ctx, tx, &m, rule, rulesdk.Input{Type: "action", ParticipantID: d.ParticipantID, OptionID: choice})
				break
			}
		}
	}
	if e == nil && m.Seq == initialSeq && m.Deadline != nil {
		_, e = tx.Exec(ctx, `UPDATE platform_matches SET next_run_at=$2 WHERE id=$1`, m.ID, m.Deadline)
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
