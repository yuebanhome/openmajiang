package platform

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func strconvI(i int) string { return strconv.Itoa(i) }

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadRoom(ctx context.Context, q queryer, rid string) (Room, error) {
	var v Room
	var hash string
	e := q.QueryRow(ctx, `SELECT id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,capacity,invite_hash,self_test,status,match_id,online_profile FROM platform_rooms WHERE id=$1`, rid).Scan(&v.ID, &v.Name, &v.OwnerID, &v.Mode, &v.RulesetID, &v.RulesetVersion, &v.Format, &v.Capacity, &hash, &v.SelfTest, &v.Status, &v.MatchID, &v.Profile)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, api(404, "ROOM_NOT_FOUND")
	}
	if e != nil {
		return v, e
	}
	v.InviteOnly = hash != ""
	v.Seats = []Seat{}
	rows, e := q.Query(ctx, `SELECT participant_id,user_id,bot_id,name,kind,seat_order,ready,control_epoch,COALESCE(connected_until>now(),false),leave_after_hand FROM platform_seats WHERE room_id=$1 AND active ORDER BY seat_order`, rid)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var a Seat
		if e = rows.Scan(&a.ParticipantID, &a.UserID, &a.BotID, &a.Name, &a.Kind, &a.Order, &a.Ready, &a.Epoch, &a.Connected, &a.Leave); e != nil {
			return v, e
		}
		v.Seats = append(v.Seats, a)
	}
	return v, rows.Err()
}
func loadMatch(ctx context.Context, q queryer, mid string, lock bool) (match, error) {
	locking := ""
	if lock {
		locking = " FOR UPDATE"
	}
	return loadMatchQuery(ctx, q, mid, locking)
}

func loadMatchForTick(ctx context.Context, q queryer, mid string) (match, error) {
	// A queued discovery can outlive a competing transition. Recheck its due
	// time before locking so stale work cannot cause another scheduling write.
	return loadMatchQuery(ctx, q, mid, " AND (next_run_at<=now() OR deadline_at<=now() OR owner_until<now()) FOR UPDATE SKIP LOCKED")
}

func loadMatchQuery(ctx context.Context, q queryer, mid, suffix string) (match, error) {
	var m match
	var choices []byte
	sql := `SELECT id,room_id,ruleset_id,ruleset_version,match_format,state,seq,status,window_id,deadline_at,choices,owner_id,owner_epoch,updated_at,interrupted,artifact_hash,archived_at,owner_until<now() FROM platform_matches WHERE id=$1`
	sql += suffix
	e := q.QueryRow(ctx, sql, mid).Scan(&m.ID, &m.RoomID, &m.RulesetID, &m.RulesetVersion, &m.Format, &m.State, &m.Seq, &m.Status, &m.WindowID, &m.Deadline, &choices, &m.Owner, &m.OwnerEpoch, &m.Updated, &m.Interrupted, &m.Artifact, &m.ArchivedAt, &m.LeaseExpired)
	if errors.Is(e, pgx.ErrNoRows) {
		return m, api(404, "MATCH_NOT_FOUND")
	}
	if e == nil {
		e = json.Unmarshal(choices, &m.Choices)
	}
	return m, e
}
func (s *Service) persistViews(ctx context.Context, tx pgx.Tx, m match, rule rulesdk.Rule, events []rulesdk.Event, input rulesdk.Input) error {
	f, e := rule.Inspect(m.State)
	if e != nil {
		return e
	}
	return s.persistFlowViews(ctx, tx, m, rule, events, input, f)
}

// The caller has already inspected this immutable state. Reusing that Flow
// avoids reevaluating every legal win while the table's row lock is held.
func (s *Service) persistFlowViews(ctx context.Context, tx pgx.Tx, m match, rule rulesdk.Rule, events []rulesdk.Event, input rulesdk.Input, f rulesdk.Flow) error {
	views, e := projectFlowViews(rule, m.State, f.Assignments)
	if e != nil {
		return e
	}
	public, e := ValidateSpectator(views[0])
	if e != nil {
		return e
	}
	// Validate all projections before sending the batch. The public and private
	// rows remain separate; a failed projection aborts the entire transaction.
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO platform_views(match_id,seq,hand_index,participant_id,view) VALUES($1,$2,$3,'',$4)`, m.ID, m.Seq, f.HandIndex, public)
	for i, a := range f.Assignments {
		batch.Queue(`INSERT INTO platform_views(match_id,seq,hand_index,participant_id,view) VALUES($1,$2,$3,$4,$5)`, m.ID, m.Seq, f.HandIndex, a.ParticipantID, views[i+1])
	}
	batch.Queue(`INSERT INTO platform_events(match_id,seq,events,input,owner_epoch) VALUES($1,$2,$3,$4,$5)`, m.ID, m.Seq, jsonBytes(events), jsonBytes(input), m.OwnerEpoch)
	if e = tx.SendBatch(ctx, batch).Close(); e != nil {
		return e
	}
	return s.persistStatistics(ctx, tx, m, f)
}

func projectFlowViews(rule rulesdk.Rule, snapshot rulesdk.Snapshot, assignments []rulesdk.Assignment) ([]json.RawMessage, error) {
	viewers := make([]rulesdk.Viewer, 1, len(assignments)+1)
	viewers[0] = rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly}
	for _, assignment := range assignments {
		viewers = append(viewers, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: assignment.ParticipantID})
	}
	if projector, ok := rule.(rulesdk.BatchProjector); ok {
		views, err := projector.ProjectMany(snapshot, viewers)
		if err != nil {
			return nil, err
		}
		if len(views) != len(viewers) {
			return nil, errors.New("rule returned incorrect projection count")
		}
		return views, nil
	}
	views := make([]json.RawMessage, len(viewers))
	for i, viewer := range viewers {
		view, err := rule.Project(snapshot, viewer)
		if err != nil {
			return nil, err
		}
		views[i] = view
	}
	return views, nil
}

func decisionParticipantIDs(decisions []rulesdk.Decision) []string {
	ids := make([]string, len(decisions))
	for i, decision := range decisions {
		ids[i] = decision.ParticipantID
	}
	return ids
}

func (s *Service) deadline(ctx context.Context, tx pgx.Tx, m match, flow rulesdk.Flow) time.Time {
	duration := 15 * time.Second
	if flow.WindowKind == "reaction" {
		duration = 4 * time.Second
	}
	var human bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE room_id=$1 AND active AND kind='human')`, m.RoomID).Scan(&human)
	if !human {
		duration = 2 * time.Second
		if flow.WindowKind == "reaction" {
			duration = 500 * time.Millisecond
		}
	}
	if flow.WindowKind == "intermission" {
		duration = 15 * time.Second
		if !human {
			duration = time.Second
		}
	}
	return time.Now().UTC().Add(duration)
}
func (s *Service) advance(ctx context.Context, tx pgx.Tx, m *match, rule rulesdk.Rule, input rulesdk.Input) error {
	old, e := rule.Inspect(m.State)
	if e != nil {
		return e
	}
	return s.advanceFromFlow(ctx, tx, m, rule, input, old)
}

func (s *Service) advanceFromFlow(ctx context.Context, tx pgx.Tx, m *match, rule rulesdk.Rule, input rulesdk.Input, old rulesdk.Flow) error {
	transition, e := rule.Apply(m.State, input)
	if e != nil {
		return e
	}
	f, e := rule.Inspect(transition.State)
	if e != nil {
		return e
	}
	m.State = transition.State
	m.Seq++
	m.Choices = map[string]string{}
	if f.MatchEnded {
		m.Status = "completed"
		m.Deadline = nil
	} else if old.WindowID != f.WindowID {
		d := s.deadline(ctx, tx, *m, f)
		if f.PreserveDeadline && old.WindowKind == "self" && f.WindowKind == "self" && old.HandIndex == f.HandIndex && m.Deadline != nil {
			d = *m.Deadline
		}

		m.Deadline = &d
	}
	m.WindowID = f.WindowID
	// Only a built-in controller with a current decision needs an early tick.
	// Human/external-bot windows and intermissions can wait for their unchanged
	// deadline without a second transaction just to defer next_run_at.
	res, e := tx.Exec(ctx, `UPDATE platform_matches m SET state=$2,seq=$3,status=$4,window_id=$5,deadline_at=$6,choices=$7,updated_at=now(),
 next_run_at=CASE WHEN EXISTS(SELECT 1 FROM platform_seats s WHERE s.room_id=m.room_id AND s.active AND s.kind='builtin' AND s.participant_id=ANY($10::text[])) THEN now() ELSE COALESCE($6::timestamptz,now()) END,
 owner_until=now()+interval '5 seconds'
 WHERE m.id=$1 AND m.owner_id=$8 AND m.owner_epoch=$9`, m.ID, m.State, m.Seq, m.Status, m.WindowID, m.Deadline, jsonBytes(m.Choices), s.id, m.OwnerEpoch, decisionParticipantIDs(f.Decisions))
	if e != nil {
		return e
	}
	if res.RowsAffected() != 1 {
		return api(409, "OWNERSHIP_LOST")
	}
	if e = s.persistFlowViews(ctx, tx, *m, rule, transition.Events, input, f); e != nil {
		return e
	}
	if m.Status != "active" {
		_, e = tx.Exec(ctx, `UPDATE platform_rooms SET status='ended' WHERE id=$1`, m.RoomID)
		if e != nil {
			return e
		}
		if e = s.requeueContinuous(ctx, tx, *m); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE platform_seats SET active=false WHERE room_id=$1`, m.RoomID)
	}
	return e
}
func (s *Service) start(ctx context.Context, roomID, userID string) (string, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = s.requireAdmissionsTx(ctx, tx, "", ""); e != nil {
		return "", e
	}
	if e = lockQuota(ctx, tx); e != nil {
		return "", e
	}
	if e = s.activeQuota(ctx, tx); e != nil {
		return "", e
	}
	if e = lockEligible(ctx, tx, userID); e != nil {
		return "", e
	}
	var status, owner string
	e = tx.QueryRow(ctx, `SELECT status,owner_id FROM platform_rooms WHERE id=$1 FOR UPDATE`, roomID).Scan(&status, &owner)
	if e != nil {
		return "", api(404, "ROOM_NOT_FOUND")
	}
	if owner != userID {
		return "", api(403, "FORBIDDEN")
	}
	if status != "waiting" {
		return "", api(409, "ROOM_NOT_WAITING")
	}
	r, e := loadRoom(ctx, tx, roomID)
	if e != nil {
		return "", e
	}
	if e = s.requireAdmissionsTx(ctx, tx, r.RulesetID, r.RulesetVersion); e != nil {
		return "", e
	}
	if len(r.Seats) != r.Capacity {
		return "", api(409, "TABLE_NOT_FULL")
	}
	cfg := rulesdk.Config{Format: r.Format, Profile: r.Profile}
	for _, a := range r.Seats {
		if !a.Ready {
			return "", api(409, "NOT_ALL_READY")
		}
		if a.Kind == "bot" {
			var capable bool
			capability := jsonBytes([]map[string]string{{"id": r.RulesetID, "version": r.RulesetVersion}})
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_bot_sessions ss JOIN platform_bot_credentials c ON c.id=ss.credential_id JOIN platform_bots b ON b.id=ss.bot_id JOIN platform_seats ps ON ps.bot_id=b.id AND ps.controller_session=ss.id WHERE ps.participant_id=$1 AND ss.expires_at>now() AND ss.revoked_at IS NULL AND c.revoked_at IS NULL AND b.enabled AND NOT b.suspended AND ss.capabilities @> $2::jsonb AND ps.connected_until>now())`, a.ParticipantID, capability).Scan(&capable)
			if e != nil || !capable {
				return "", api(409, "BOT_OFFLINE_OR_INCOMPATIBLE")
			}
		}
		cfg.Participants = append(cfg.Participants, rulesdk.Participant{ID: a.ParticipantID, Name: a.Name, Kind: a.Kind})
	}
	rule, e := s.rule(r.RulesetID, r.RulesetVersion)
	if e != nil {
		return "", e
	}
	seed := make([]byte, 32)
	if _, e = rand.Read(seed); e != nil {
		return "", e
	}
	state, e := rule.Init(cfg, seed)
	if e != nil {
		return "", e
	}
	flow, e := rule.Inspect(state)
	if e != nil {
		return "", e
	}
	m := match{ID: id("match"), RoomID: r.ID, RulesetID: r.RulesetID, RulesetVersion: r.RulesetVersion, Format: r.Format, State: state, Seq: 1, Status: "active", WindowID: flow.WindowID, Owner: s.id, OwnerEpoch: 1, Choices: map[string]string{}}
	d := s.deadline(ctx, tx, m, flow)
	m.Deadline = &d
	_, e = tx.Exec(ctx, `INSERT INTO platform_matches(id,room_id,ruleset_id,ruleset_version,match_format,state,window_id,deadline_at,owner_id,artifact_hash,manifest,config,owner_until,next_run_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now()+interval '5 seconds',
 CASE WHEN EXISTS(SELECT 1 FROM platform_seats WHERE room_id=$2 AND active AND kind='builtin' AND participant_id=ANY($13::text[])) THEN now() ELSE COALESCE($8::timestamptz,now()) END)`, m.ID, r.ID, m.RulesetID, m.RulesetVersion, m.Format, state, m.WindowID, d, s.id, rule.Manifest().ArtifactHash, jsonBytes(rule.Manifest()), jsonBytes(cfg), decisionParticipantIDs(flow.Decisions))
	if e != nil {
		return "", e
	}
	if e = s.freezeStatistics(ctx, tx, m); e != nil {
		return "", e
	}
	if e = s.persistFlowViews(ctx, tx, m, rule, nil, rulesdk.Input{Type: "init"}, flow); e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_rooms SET status='playing',match_id=$2 WHERE id=$1`, r.ID, m.ID)
	if e != nil {
		return "", e
	}
	return m.ID, tx.Commit(ctx)
}
