package platform

import (
	"context"
	"net/http"
	"sort"
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
	if e = s.requireAdmissionsTx(ctx, tx, q.Rule, q.Version); e != nil {
		return e
	}
	if e = lockQuota(ctx, tx); e != nil {
		return e
	}
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
	if e = s.queueQuota(ctx, tx, key); e != nil {
		return e
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
		e = s.cancelQueue(r.Context(), u.ID, "")
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
	e := s.cancelQueue(r.Context(), b.Owner, b.ID)
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
		e = s.cancelQueue(r.Context(), b.Owner, b.ID)
	}
	if e != nil {
		failure(w, e)
		return
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE platform_seats SET continuous=false WHERE bot_id=$1`, b.ID)
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
	if e = s.requireAdmissionsTx(ctx, tx, "", ""); e != nil {
		return
	}
	if e = lockQuota(ctx, tx); e != nil {
		return
	}
	if e = s.requeueCompleted(ctx, tx); e != nil {
		return
	}
	var locked bool
	if tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(918735034)`).Scan(&locked) != nil || !locked {
		return
	}
	rows, e := tx.Query(ctx, `SELECT participant_key,user_id,bot_id,ruleset_id,ruleset_version,match_format,continuous FROM platform_queue ORDER BY created_at LIMIT 100`)
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
		if e = s.requireAdmissionsTx(ctx, tx, items[0].rule, items[0].version); e != nil {
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
		ordered := append([]queued(nil), items...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].uid < ordered[j].uid })
		for _, q := range ordered {
			if e = lockEligible(ctx, tx, q.uid); e != nil {
				return
			}
		}
		valid := true
		for _, q := range items {
			var key string
			if tx.QueryRow(ctx, `SELECT participant_key FROM platform_queue WHERE participant_key=$1 AND ruleset_id=$2 AND ruleset_version=$3 AND match_format=$4 FOR UPDATE`, q.key, q.rule, q.version, q.format).Scan(&key) != nil {
				valid = false
				continue
			}
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
		if e = s.waitingQuota(ctx, tx, items[0].uid); e != nil {
			continue
		}
		if e = s.activeQuota(ctx, tx); e != nil {
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

// Completion leaves a pending continuous seat; the matchmaker consumes it
// under the admission lock. Never wait for that lock while holding a match row.
func (s *Service) requeueContinuous(context.Context, pgx.Tx, match) error { return nil }
func (s *Service) requeueCompleted(ctx context.Context, tx pgx.Tx) error {
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM platform_queue`).Scan(&count); e != nil {
		return e
	}
	available := s.cfg.MaxQueuedParticipants - count
	if available <= 0 {
		return nil
	}
	if available > 100 {
		available = 100
	}
	rows, e := tx.Query(ctx, `SELECT s.participant_id,s.user_id,s.bot_id,m.ruleset_id,m.ruleset_version,m.match_format FROM platform_seats s JOIN platform_matches m ON m.room_id=s.room_id JOIN platform_bots b ON b.id=s.bot_id JOIN auth_users u ON u.id=s.user_id WHERE u.status='active' AND u.verified AND NOT s.active AND s.continuous AND s.kind='bot' AND m.status='completed' AND b.enabled AND NOT b.suspended ORDER BY m.updated_at DESC,s.participant_id LIMIT $1`, available)
	if e != nil {
		return e
	}
	type candidate struct{ pid, uid, bid, rule, version, format string }
	var pending []candidate
	for rows.Next() {
		var p candidate
		if e = rows.Scan(&p.pid, &p.uid, &p.bid, &p.rule, &p.version, &p.format); e != nil {
			rows.Close()
			return e
		}
		pending = append(pending, p)
	}
	rows.Close()
	sort.Slice(pending, func(i, j int) bool { return pending[i].uid < pending[j].uid })
	for _, p := range pending {
		if e = lockEligible(ctx, tx, p.uid); e != nil {
			if code, ok := e.(*APIError); ok && code.Code == "ACCOUNT_NOT_ELIGIBLE" {
				_, e = tx.Exec(ctx, `UPDATE platform_seats SET continuous=false WHERE participant_id=$1`, p.pid)
				if e != nil {
					return e
				}
				continue
			}
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform_queue(participant_key,user_id,bot_id,ruleset_id,ruleset_version,match_format,continuous) SELECT 'bot:'||$1,$2,$1,$3,$4,$5,true WHERE NOT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$2 AND active AND kind<>'builtin') AND NOT EXISTS(SELECT 1 FROM platform_queue WHERE user_id=$2) AND EXISTS(SELECT 1 FROM platform_bot_sessions bs JOIN platform_bot_credentials bc ON bc.id=bs.credential_id WHERE bs.bot_id=$1 AND bs.revoked_at IS NULL AND bc.revoked_at IS NULL AND bs.expires_at>now() AND bs.last_seen_at>now()-interval '30 seconds') ON CONFLICT DO NOTHING`, p.bid, p.uid, p.rule, p.version, p.format)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE platform_seats SET continuous=false WHERE participant_id=$1`, p.pid)
		if e != nil {
			return e
		}
	}
	return nil
}
