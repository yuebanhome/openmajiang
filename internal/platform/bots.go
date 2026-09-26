package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type botIdentity struct{ ID, Owner, Name, Session string }

func (s *Service) botRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/bots", s.listBots)
	m.HandleFunc("POST /v1/bots", s.mutation(s.createBot))
	m.HandleFunc("PATCH /v1/bots/{id}", s.mutation(s.updateBot))
	m.HandleFunc("GET /v1/bots/{id}/versions", s.botVersions)
	m.HandleFunc("POST /v1/bots/{id}/versions", s.mutation(s.createBotVersion))
	m.HandleFunc("GET /v1/bots/{id}/credentials", s.botCredentials)
	m.HandleFunc("POST /v1/bots/{id}/credentials", s.mutation(s.createCredential))
	m.HandleFunc("DELETE /v1/bots/{id}/credentials/{credential}", s.mutation(s.revokeCredential))
	m.HandleFunc("POST /v1/bot-sessions", s.botSession)
	m.HandleFunc("GET /v1/bot/active-match", s.botActive)
}
func (s *Service) ownBot(w http.ResponseWriter, r *http.Request) (botIdentity, bool) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return botIdentity{}, false
	}
	var b botIdentity
	e = s.pool.QueryRow(r.Context(), `SELECT id,owner_id,name FROM platform_bots WHERE id=$1 AND owner_id=$2`, r.PathValue("id"), u.ID).Scan(&b.ID, &b.Owner, &b.Name)
	if e != nil {
		failure(w, api(404, "BOT_NOT_FOUND"))
		return b, false
	}
	return b, true
}
func (s *Service) listBots(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	rows, e := s.pool.Query(r.Context(), `SELECT b.id,b.name,b.enabled,b.current_version,b.suspended,EXISTS(SELECT 1 FROM platform_bot_sessions bs WHERE bs.bot_id=b.id AND bs.revoked_at IS NULL AND bs.expires_at>now()) FROM platform_bots b WHERE owner_id=$1 ORDER BY created_at`, u.ID)
	if e != nil {
		failure(w, e)
		return
	}
	defer rows.Close()
	bots := []any{}
	for rows.Next() {
		var id, name, version string
		var enabled, online, suspended bool
		if e = rows.Scan(&id, &name, &enabled, &version, &suspended, &online); e != nil {
			failure(w, e)
			return
		}
		bots = append(bots, map[string]any{"id": id, "name": name, "enabled": enabled, "suspended": suspended, "current_version": version, "online": online})
	}
	write(w, 200, map[string]any{"bots": bots})
}
func (s *Service) createBot(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if e = decode(w, r, &b); e != nil || len([]rune(b.Name)) < 1 || len([]rune(b.Name)) > 50 {
		failure(w, api(400, "INVALID_NAME"))
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		failure(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	if e = lockEligible(r.Context(), tx, u.ID); e != nil {
		failure(w, e)
		return
	}
	var count int
	_ = tx.QueryRow(r.Context(), `SELECT count(*) FROM platform_bots WHERE owner_id=$1`, u.ID).Scan(&count)
	if count >= 4 {
		failure(w, api(409, "BOT_QUOTA_EXCEEDED"))
		return
	}
	bid := id("bot")
	vid := id("version")
	_, e = tx.Exec(r.Context(), `INSERT INTO platform_bots(id,owner_id,name,current_version) VALUES($1,$2,$3,$4)`, bid, u.ID, b.Name, vid)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO platform_bot_versions(id,bot_id,label) VALUES($1,$2,'v1')`, vid, bid)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"bot": map[string]any{"id": bid, "name": b.Name, "enabled": true, "current_version": vid}})
}
func (s *Service) updateBot(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	var v struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if e := decode(w, r, &v); e != nil {
		failure(w, e)
		return
	}
	if v.Name != nil && (len([]rune(*v.Name)) < 1 || len([]rune(*v.Name)) > 50) {
		failure(w, api(400, "INVALID_NAME"))
		return
	}
	if v.Enabled != nil && *v.Enabled {
		var suspended bool
		if e := s.pool.QueryRow(r.Context(), `SELECT suspended FROM platform_bots WHERE id=$1`, b.ID).Scan(&suspended); e != nil || suspended {
			failure(w, api(403, "BOT_SUSPENDED"))
			return
		}
	}
	_, e := s.pool.Exec(r.Context(), `UPDATE platform_bots SET name=COALESCE($2,name),enabled=COALESCE($3,enabled) WHERE id=$1`, b.ID, v.Name, v.Enabled)
	if e != nil {
		failure(w, e)
		return
	}
	if v.Enabled != nil && !*v.Enabled {
		s.revokeBot(r.Context(), b.ID)
	}
	write(w, 200, map[string]any{"ok": true})
}
func (s *Service) botVersions(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	rows, e := s.pool.Query(r.Context(), `SELECT id,label,metadata,created_at FROM platform_bot_versions WHERE bot_id=$1 ORDER BY created_at DESC`, b.ID)
	if e != nil {
		failure(w, e)
		return
	}
	defer rows.Close()
	vs := []any{}
	for rows.Next() {
		var id, label string
		var meta json.RawMessage
		var at time.Time
		if rows.Scan(&id, &label, &meta, &at) == nil {
			vs = append(vs, map[string]any{"id": id, "label": label, "metadata": meta, "created_at": at})
		}
	}
	write(w, 200, map[string]any{"versions": vs})
}
func (s *Service) createBotVersion(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	var v struct {
		Label    string          `json:"label"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if e := decode(w, r, &v); e != nil {
		failure(w, e)
		return
	}
	if len(v.Label) < 1 || len(v.Label) > 80 {
		failure(w, api(400, "INVALID_VERSION"))
		return
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage(`{}`)
	}
	vid := id("version")
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		failure(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `INSERT INTO platform_bot_versions(id,bot_id,label,metadata) VALUES($1,$2,$3,$4)`, vid, b.ID, v.Label, v.Metadata)
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE platform_bots SET current_version=$2 WHERE id=$1`, b.ID, vid)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"version": map[string]any{"id": vid, "label": v.Label}})
}
func (s *Service) createCredential(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	var suspended bool
	if e := s.pool.QueryRow(r.Context(), `SELECT suspended FROM platform_bots WHERE id=$1`, b.ID).Scan(&suspended); e != nil || suspended {
		failure(w, api(403, "BOT_SUSPENDED"))
		return
	}
	secret := id("omj_bot")
	cid := id("credential")
	_, e := s.pool.Exec(r.Context(), `INSERT INTO platform_bot_credentials(id,bot_id,secret_hash) VALUES($1,$2,$3)`, cid, b.ID, s.hash(secret))
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"credential_id": cid, "api_key": secret, "shown_once": true})
}
func (s *Service) botCredentials(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	rows, e := s.pool.Query(r.Context(), `SELECT id,created_at,revoked_at FROM platform_bot_credentials WHERE bot_id=$1 ORDER BY created_at DESC`, b.ID)
	if e != nil {
		failure(w, e)
		return
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		var id string
		var created time.Time
		var revoked *time.Time
		if rows.Scan(&id, &created, &revoked) == nil {
			result = append(result, map[string]any{"id": id, "created_at": created, "revoked_at": revoked})
		}
	}
	write(w, 200, map[string]any{"credentials": result})
}
func (s *Service) revokeCredential(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		failure(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `UPDATE platform_bot_credentials SET revoked_at=now() WHERE id=$1 AND bot_id=$2`, r.PathValue("credential"), b.ID)
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE platform_bot_sessions SET revoked_at=now() WHERE credential_id=$1 AND bot_id=$2`, r.PathValue("credential"), b.ID)
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE platform_seats SET control_epoch=control_epoch+1,controller='',connected_until=NULL WHERE bot_id=$1`, b.ID)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"ok": true})
}
func (s *Service) revokeBot(ctx context.Context, bid string) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `UPDATE platform_bot_sessions SET revoked_at=now() WHERE bot_id=$1`, bid)
	_, _ = tx.Exec(ctx, `UPDATE platform_bot_credentials SET revoked_at=now() WHERE bot_id=$1`, bid)
	_, _ = tx.Exec(ctx, `UPDATE platform_seats SET control_epoch=control_epoch+1,controller='',connected_until=NULL,leave_after_hand=true WHERE bot_id=$1 AND active`, bid)
	_, _ = tx.Exec(ctx, `DELETE FROM platform_queue WHERE bot_id=$1`, bid)
	_ = tx.Commit(ctx)
}
func (s *Service) botSession(w http.ResponseWriter, r *http.Request) {
	if !s.limit("bot-session:"+s.clientIP(r), 30) {
		failure(w, api(429, "RATE_LIMITED"))
		return
	}
	var c struct {
		Protocol string `json:"protocol_version"`
		Rulesets []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"rulesets"`
	}
	if e := decode(w, r, &c); e != nil {
		failure(w, e)
		return
	}
	if c.Protocol != "1.0" || len(c.Rulesets) == 0 {
		failure(w, api(400, "UNSUPPORTED_PROTOCOL"))
		return
	}
	for _, rule := range c.Rulesets {
		if _, e := s.rule(rule.ID, rule.Version); e != nil {
			failure(w, e)
			return
		}
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		failure(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var uid string
	e = tx.QueryRow(r.Context(), `SELECT b.owner_id FROM platform_bot_credentials c JOIN platform_bots b ON b.id=c.bot_id WHERE c.secret_hash=$1`, s.hash(bearer(r))).Scan(&uid)
	if e != nil {
		failure(w, api(401, "INVALID_BOT_KEY"))
		return
	}
	if e = lockEligible(r.Context(), tx, uid); e != nil {
		failure(w, e)
		return
	}
	var bid, cid string
	e = tx.QueryRow(r.Context(), `SELECT c.bot_id,c.id FROM platform_bot_credentials c JOIN platform_bots b ON b.id=c.bot_id WHERE c.secret_hash=$1 AND c.revoked_at IS NULL AND b.enabled AND NOT b.suspended FOR SHARE OF c,b`, s.hash(bearer(r))).Scan(&bid, &cid)
	if e != nil {
		failure(w, api(401, "INVALID_BOT_KEY"))
		return
	}
	session := id("bot_session")
	secret := id("omj_session")
	expires := time.Now().Add(30 * time.Minute)
	_, e = tx.Exec(r.Context(), `INSERT INTO platform_bot_sessions(id,bot_id,credential_id,secret_hash,expires_at,capabilities) VALUES($1,$2,$3,$4,$5,$6)`, session, bid, cid, s.hash(secret), expires, jsonBytes(c.Rulesets))
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"session_token": secret, "expires_at": expires, "bot_id": bid, "protocol_version": "1.0"})
}

func (s *Service) authenticateBot(r *http.Request) (botIdentity, error) {
	var b botIdentity
	e := s.pool.QueryRow(r.Context(), `SELECT b.id,b.owner_id,b.name,ss.id FROM platform_bot_sessions ss JOIN platform_bot_credentials c ON c.id=ss.credential_id JOIN platform_bots b ON b.id=ss.bot_id JOIN auth_users u ON u.id=b.owner_id WHERE ss.secret_hash=$1 AND ss.expires_at>now() AND ss.revoked_at IS NULL AND c.revoked_at IS NULL AND b.enabled AND NOT b.suspended AND u.status='active' AND u.verified`, s.hash(bearer(r))).Scan(&b.ID, &b.Owner, &b.Name, &b.Session)
	if e != nil {
		return b, api(401, "AUTH_EXPIRED")
	}
	return b, nil
}
func (s *Service) botActive(w http.ResponseWriter, r *http.Request) {
	b, e := s.authenticateBot(r)
	if e != nil {
		failure(w, e)
		return
	}
	var rid string
	e = s.pool.QueryRow(r.Context(), `SELECT room_id FROM platform_seats WHERE bot_id=$1 AND active`, b.ID).Scan(&rid)
	if e != nil {
		write(w, 200, map[string]any{"room": nil})
		return
	}
	room, e := loadRoom(r.Context(), s.pool, rid)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"room": room, "match_id": room.MatchID})
}
func (s *Service) botWS(w http.ResponseWriter, r *http.Request) {
	b, e := s.authenticateBot(r)
	if e != nil {
		failure(w, e)
		return
	}
	rid := r.URL.Query().Get("room_id")
	if rid == "" {
		_ = s.pool.QueryRow(r.Context(), `SELECT room_id FROM platform_seats WHERE bot_id=$1 AND active`, b.ID).Scan(&rid)
	}
	p, e := s.participant(r.Context(), rid, b.Owner, b.ID)
	if e != nil {
		failure(w, e)
		return
	}
	c, release, e := s.accept(w, r, "bot:"+b.ID)
	if e != nil {
		return
	}
	defer release()
	defer c.Close(1000, "closed")
	control, _, _, e := s.acquireControl(r.Context(), p, b.Session, true, true)
	if e != nil {
		return
	}
	s.socketLoop(r.Context(), c, r, rid, p.ParticipantID, control, false, func() bool { _, e := s.authenticateBot(r); return e == nil })
}
func (s *Service) addBot(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var v struct {
		Bot     string `json:"bot_id"`
		Builtin string `json:"builtin"`
	}
	if e = decode(w, r, &v); e == nil {
		e = s.addBotToRoom(r.Context(), r.PathValue("id"), u.ID, v.Bot, v.Builtin)
	}
	if e != nil {
		failure(w, e)
		return
	}
	s.getRoom(w, r)
}
func (s *Service) addBotToRoom(ctx context.Context, rid, owner, bid, builtin string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.requireAdmissionsTx(ctx, tx, "", ""); e != nil {
		return e
	}
	if e = lockEligible(ctx, tx, owner); e != nil {
		return e
	}
	var status string
	var uid string
	e = tx.QueryRow(ctx, `SELECT status,owner_id FROM platform_rooms WHERE id=$1 FOR UPDATE`, rid).Scan(&status, &uid)
	if e != nil {
		return e
	}
	if uid != owner || status != "waiting" {
		return api(403, "FORBIDDEN")
	}
	r, e := loadRoom(ctx, tx, rid)
	if e != nil {
		return e
	}
	if r.Mode == "human_only" || len(r.Seats) >= r.Capacity {
		return api(409, "BOT_NOT_ALLOWED")
	}
	kind := "bot"
	name := ""
	version := ""
	if builtin != "" {
		if builtin != "random_legal" && builtin != "basic_heuristic" {
			return api(400, "UNKNOWN_BUILTIN")
		}
		kind = "builtin"
		name = "内置练习 Bot"
		bid = ""
	} else {
		e = tx.QueryRow(ctx, `SELECT name,current_version FROM platform_bots WHERE id=$1 AND owner_id=$2 AND enabled AND NOT suspended`, bid, owner).Scan(&name, &version)
		if e != nil {
			return api(403, "BOT_NOT_OWNED")
		}
		if !r.SelfTest {
			for _, p := range r.Seats {
				if p.UserID == owner && p.Kind != "builtin" {
					return api(409, "OWNER_ALREADY_SEATED")
				}
			}
		}
	}
	var order int
	e = tx.QueryRow(ctx, `SELECT n FROM generate_series(0,$2-1) n WHERE NOT EXISTS(SELECT 1 FROM platform_seats WHERE room_id=$1 AND seat_order=n) ORDER BY n LIMIT 1`, rid, r.Capacity).Scan(&order)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,bot_id,name,kind,seat_order,ready,bot_version,builtin_strategy) VALUES($1,$2,$3,$4,$5,$6,$7,$9,$8,$10)`, id("participant"), rid, owner, bid, name, kind, order, version, kind == "builtin", builtin)
	if e != nil {
		return api(409, "BOT_ALREADY_SEATED")
	}
	if e = resetReady(ctx, tx, rid); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func lockEligible(ctx context.Context, tx pgx.Tx, uid string) error {
	var status string
	var verified bool
	e := tx.QueryRow(ctx, `SELECT status,verified FROM auth_users WHERE id=$1 FOR UPDATE`, uid).Scan(&status, &verified)
	if e != nil || status != "active" || !verified {
		return api(403, "ACCOUNT_NOT_ELIGIBLE")
	}
	return nil
}
