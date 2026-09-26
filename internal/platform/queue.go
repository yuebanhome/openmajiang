package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type queueRequest struct {
	Rule       string `json:"ruleset_id"`
	Version    string `json:"ruleset_version"`
	Format     string `json:"match_format"`
	Continuous bool   `json:"continuous"`
}

func (s *Service) queueRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /v1/queue", s.mutation(s.joinHumanQueue))
	m.HandleFunc("DELETE /v1/queue", s.mutation(s.leaveHumanQueue))
	m.HandleFunc("GET /v1/queue", s.humanQueueStatus)
	m.HandleFunc("POST /v1/bots/{id}/queue", s.mutation(s.joinOwnerBotQueue))
	m.HandleFunc("DELETE /v1/bots/{id}/queue", s.mutation(s.leaveOwnerBotQueue))
	m.HandleFunc("POST /v1/bot/queue", s.joinBotQueue)
	m.HandleFunc("DELETE /v1/bot/queue", s.leaveBotQueue)
}
func (s *Service) enqueue(ctx context.Context, uid, bid string, q queueRequest) error {
	if q.Rule == "" {
		q.Rule = "openmajiang.mcr"
	}
	if q.Version == "" {
		q.Version = "1.0.0"
	}
	if q.Format == "" {
		q.Format = "standard_16"
	}
	rule, e := s.rule(q.Rule, q.Version)
	if e != nil {
		return e
	}
	supported := false
	for _, f := range rule.Manifest().Formats {
		if f == q.Format {
			supported = true
		}
	}
	if !supported {
		return api(400, "INVALID_FORMAT")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockEligible(ctx, tx, uid); e != nil {
		return e
	}
	var busy bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active AND kind<>'builtin') OR EXISTS(SELECT 1 FROM platform_queue WHERE user_id=$1 AND bot_id<>$2)`, uid, bid).Scan(&busy)
	if e != nil {
		return e
	}
	if busy {
		return api(409, "OWNER_ALREADY_SEATED_OR_QUEUED")
	}
	if bid != "" {
		var eligible bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_bots b WHERE b.id=$1 AND b.owner_id=$2 AND enabled AND NOT suspended AND EXISTS(SELECT 1 FROM platform_bot_sessions ss WHERE ss.bot_id=b.id AND ss.revoked_at IS NULL AND ss.expires_at>now()))`, bid, uid).Scan(&eligible)
		if e != nil || !eligible {
			return api(409, "BOT_OFFLINE")
		}
	}
	key := "user:" + uid
	if bid != "" {
		key = "bot:" + bid
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_queue(participant_key,user_id,bot_id,ruleset_id,ruleset_version,match_format,continuous) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(participant_key) DO UPDATE SET continuous=excluded.continuous`, key, uid, bid, q.Rule, q.Version, q.Format, q.Continuous)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) joinHumanQueue(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var q queueRequest
	if e = decode(w, r, &q); e == nil {
		e = s.enqueue(r.Context(), u.ID, "", q)
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 202, map[string]any{"status": "queued"})
}
func (s *Service) leaveHumanQueue(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e == nil {
		_, e = s.pool.Exec(r.Context(), `DELETE FROM platform_queue WHERE user_id=$1 AND bot_id=''`, u.ID)
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"status": "cancelled"})
}
func (s *Service) humanQueueStatus(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	var queued bool
	e = s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM platform_queue WHERE user_id=$1 AND bot_id='')`, u.ID).Scan(&queued)
	if e != nil {
		failure(w, e)
		return
	}
	var room string
	_ = s.pool.QueryRow(r.Context(), `SELECT room_id FROM platform_seats WHERE user_id=$1 AND active AND kind='human'`, u.ID).Scan(&room)
	write(w, 200, map[string]any{"queued": queued, "room_id": room})
}
func (s *Service) joinOwnerBotQueue(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	var q queueRequest
	e := decode(w, r, &q)
	if e == nil {
		e = s.enqueue(r.Context(), b.Owner, b.ID, q)
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 202, map[string]any{"status": "queued"})
}
func (s *Service) leaveOwnerBotQueue(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	_, e := s.pool.Exec(r.Context(), `DELETE FROM platform_queue WHERE bot_id=$1`, b.ID)
	if e != nil {
		failure(w, e)
		return
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE platform_seats SET continuous=false WHERE bot_id=$1`, b.ID)
	write(w, 200, map[string]any{"status": "cancelled"})
}
func (s *Service) joinBotQueue(w http.ResponseWriter, r *http.Request) {
	b, e := s.authenticateBot(r)
	if e != nil {
		failure(w, e)
		return
	}
	var q queueRequest
	if e = decode(w, r, &q); e == nil {
		e = s.enqueue(r.Context(), b.Owner, b.ID, q)
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 202, map[string]any{"status": "queued"})
}
func (s *Service) leaveBotQueue(w http.ResponseWriter, r *http.Request) {
	b, e := s.authenticateBot(r)
	if e == nil {
		_, e = s.pool.Exec(r.Context(), `DELETE FROM platform_queue WHERE bot_id=$1`, b.ID)
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"status": "cancelled"})
}

type queued struct {
	key, uid, bid, rule, version, format string
	continuous                           bool
}

func (s *Service) matchQueues(ctx context.Context) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	var locked bool
	if tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(918735034)`).Scan(&locked) != nil || !locked {
		return
	}
	rows, e := tx.Query(ctx, `SELECT participant_key,user_id,bot_id,ruleset_id,ruleset_version,match_format,continuous FROM platform_queue ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if e != nil {
		return
	}
	groups := map[string][]queued{}
	for rows.Next() {
		var q queued
		if rows.Scan(&q.key, &q.uid, &q.bid, &q.rule, &q.version, &q.format, &q.continuous) == nil {
			k := q.rule + "@" + q.version + ":" + q.format
			if q.bid != "" {
				k += ":bot"
			}
			groups[k] = append(groups[k], q)
		}
	}
	rows.Close()
	for _, items := range groups {
		if len(items) == 0 {
			continue
		}
		rule, e := s.rule(items[0].rule, items[0].version)
		if e != nil {
			continue
		}
		n := rule.Manifest().SeatCounts[0]
		if len(items) < n {
			continue
		}
		items = items[:n]
		valid := true
		for _, q := range items {
			var eligible bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE id=$1 AND status='active' AND verified) AND NOT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active AND kind<>'builtin')`, q.uid).Scan(&eligible)
			if e != nil || !eligible {
				valid = false
				_, _ = tx.Exec(ctx, `DELETE FROM platform_queue WHERE participant_key=$1`, q.key)
			}
		}
		if !valid {
			continue
		}
		rid := id("room")
		mode := "human_only"
		if items[0].bid != "" {
			mode = "bot_only"
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform_rooms(id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,capacity,online_profile,queue_deadline) VALUES($1,'公共匹配',$2,$3,$4,$5,$6,$7,$8,now()+interval '60 seconds')`, rid, items[0].uid, mode, items[0].rule, items[0].version, items[0].format, n, firstProfile(rule.Manifest().OnlineProfiles))
		if e != nil {
			return
		}
		for i, q := range items {
			var name, version string
			kind := "human"
			if q.bid == "" {
				e = tx.QueryRow(ctx, `SELECT name FROM auth_users WHERE id=$1`, q.uid).Scan(&name)
			} else {
				kind = "bot"
				e = tx.QueryRow(ctx, `SELECT name,current_version FROM platform_bots WHERE id=$1 AND enabled AND NOT suspended`, q.bid).Scan(&name, &version)
			}
			if e != nil {
				return
			}
			_, e = tx.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,bot_id,name,kind,seat_order,ready,bot_version,continuous) VALUES($1,$2,$3,$4,$5,$6,$7,false,$8,$9)`, id("participant"), rid, q.uid, q.bid, name, kind, i, version, q.continuous)
			if e != nil {
				return
			}
			_, e = tx.Exec(ctx, `DELETE FROM platform_queue WHERE participant_key=$1`, q.key)
			if e != nil {
				return
			}
		}
	}
	_ = tx.Commit(ctx)
	s.expireQueueRooms(ctx)
}
func (s *Service) expireQueueRooms(ctx context.Context) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT id FROM platform_rooms WHERE status='waiting' AND queue_deadline<now() FOR UPDATE SKIP LOCKED`)
	if e != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, _ = tx.Exec(ctx, `UPDATE platform_rooms SET status='cancelled' WHERE id=$1`, id)
		_, _ = tx.Exec(ctx, `UPDATE platform_seats SET active=false WHERE room_id=$1`, id)
	}
	_ = tx.Commit(ctx)
}
func (s *Service) tryAutoStart(ctx context.Context, rid string) {
	var owner string
	var ready bool
	e := s.pool.QueryRow(ctx, `SELECT owner_id,(SELECT count(*) FROM platform_seats WHERE room_id=$1 AND active AND ready)=capacity FROM platform_rooms WHERE id=$1 AND status='waiting' AND queue_deadline IS NOT NULL`, rid).Scan(&owner, &ready)
	if e == nil && ready {
		_, _ = s.start(ctx, rid, owner)
	}
}

var _ = time.Second
var _ pgx.Tx

func firstProfile(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

func (s *Service) requeueContinuous(ctx context.Context, tx pgx.Tx, m match) error {
	if m.Status != "completed" {
		return nil
	}
	_, e := tx.Exec(ctx, `INSERT INTO platform_queue(participant_key,user_id,bot_id,ruleset_id,ruleset_version,match_format,continuous)
 SELECT 'bot:'||s.bot_id,s.user_id,s.bot_id,$2,$3,$4,true FROM platform_seats s JOIN platform_bots b ON b.id=s.bot_id JOIN auth_users u ON u.id=s.user_id
 WHERE s.room_id=$1 AND s.continuous AND s.kind='bot' AND b.enabled AND NOT b.suspended AND u.status='active' AND u.verified AND EXISTS(SELECT 1 FROM platform_bot_sessions ss JOIN platform_bot_credentials c ON c.id=ss.credential_id WHERE ss.bot_id=b.id AND ss.expires_at>now() AND ss.revoked_at IS NULL AND c.revoked_at IS NULL)
 ON CONFLICT(participant_key) DO NOTHING`, m.RoomID, m.RulesetID, m.RulesetVersion, m.Format)
	return e
}
