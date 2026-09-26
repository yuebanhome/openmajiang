package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminSchema = `
CREATE TABLE IF NOT EXISTS platform_system (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 maintenance boolean NOT NULL DEFAULT false, public_message text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO platform_system(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS platform_rule_switches (
 ruleset_id text NOT NULL, ruleset_version text NOT NULL, enabled boolean NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(ruleset_id,ruleset_version)
);
ALTER TABLE platform_bots ADD COLUMN IF NOT EXISTS suspended boolean NOT NULL DEFAULT false;
ALTER TABLE platform_audit ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '';
ALTER TABLE platform_audit ADD COLUMN IF NOT EXISTS outcome text NOT NULL DEFAULT 'succeeded';
`

func AdminMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, adminSchema)
	return err
}

// Admission transactions must call this before locking users, rooms or bots.
// The shared row lock fences maintenance/rule-switch changes until admission commits.
func (s *Service) requireAdmissionsTx(ctx context.Context, tx pgx.Tx, ruleset, version string) error {
	var maintenance bool
	if err := tx.QueryRow(ctx, `SELECT maintenance FROM platform_system WHERE singleton=true FOR SHARE`).Scan(&maintenance); err != nil {
		return err
	}
	if maintenance {
		return api(503, "MAINTENANCE")
	}
	if ruleset != "" {
		var enabled bool
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT enabled FROM platform_rule_switches WHERE ruleset_id=$1 AND ruleset_version=$2),true)`, ruleset, version).Scan(&enabled)
		if err != nil {
			return err
		}
		if !enabled {
			return api(409, "RULESET_DISABLED")
		}
	}
	return nil
}
func (s *Service) requireAdmissions(ctx context.Context) error {
	var maintenance bool
	if err := s.pool.QueryRow(ctx, `SELECT maintenance FROM platform_system WHERE singleton=true`).Scan(&maintenance); err != nil {
		return err
	}
	if maintenance {
		return api(503, "MAINTENANCE")
	}
	return nil
}
func (s *Service) adminRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/public/status", s.publicStatus)
	m.HandleFunc("GET /v1/admin/status", s.adminStatus)
	m.HandleFunc("POST /v1/admin/maintenance", s.adminMutation(s.adminMaintenance))
	m.HandleFunc("POST /v1/admin/users/{id}/status", s.adminMutation(s.adminUser))
	m.HandleFunc("POST /v1/admin/bots/{id}/disable", s.adminMutation(s.adminBot))
	m.HandleFunc("POST /v1/admin/bots/{id}/restore", s.adminMutation(s.adminRestoreBot))
	m.HandleFunc("POST /v1/admin/rulesets/{id}/versions/{version}/status", s.adminMutation(s.adminRule))
	m.HandleFunc("POST /v1/admin/matches/{id}/abort", s.adminMutation(s.adminAbortMatch))
	m.HandleFunc("GET /v1/admin/audit", s.adminAudit)
	m.HandleFunc("POST /v1/rooms/{id}/invite-rotate", s.mutation(s.rotateInvite))
}

type adminResponse struct {
	http.ResponseWriter
	status int
}

func (w *adminResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *adminResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (s *Service) adminMutation(next http.HandlerFunc) http.HandlerFunc {
	return s.mutation(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.admin(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
		if err != nil {
			failure(w, api(400, "INVALID_REQUEST"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var input struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &input)
		if !adminText(input.Reason, 200) {
			input.Reason = "invalid request"
		}
		response := &adminResponse{ResponseWriter: w}
		next(response, r)
		if response.status < 400 {
			return
		}
		// Success is audited in the mutation transaction. A rejected authorized
		// request is recorded separately, without retaining its arbitrary body.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
		defer cancel()
		_, _ = s.pool.Exec(ctx, `INSERT INTO platform_audit(actor_id,action,target_id,reason,outcome) VALUES($1,$2,$3,$4,$5)`, actor, "rejected:"+r.Pattern, r.PathValue("id"), input.Reason, "http_"+strconv.Itoa(response.status))
	})
}
func (s *Service) admin(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, err := s.user(r, true)
	if err != nil {
		failure(w, err)
		return "", false
	}
	if u.Role != "operator" {
		failure(w, api(403, "FORBIDDEN"))
		return "", false
	}
	return u.ID, true
}
func (s *Service) beginAdmin(ctx context.Context, r *http.Request, actor string) (pgx.Tx, error) {
	sid, err := s.cfg.Auth.SessionID(r)
	if err != nil {
		return nil, api(401, "AUTH_EXPIRED")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	failed := func(e error) (pgx.Tx, error) { _ = tx.Rollback(ctx); return nil, e }
	var singleton bool
	if err = tx.QueryRow(ctx, `SELECT singleton FROM platform_system WHERE singleton=true FOR UPDATE`).Scan(&singleton); err != nil {
		return failed(err)
	}
	var role, status string
	var verified bool
	if err = tx.QueryRow(ctx, `SELECT role,status,verified FROM auth_users WHERE id=$1 FOR UPDATE`, actor).Scan(&role, &status, &verified); err != nil {
		return failed(err)
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, sid, actor).Scan(&live); err != nil {
		return failed(err)
	}
	if role != "operator" || status != "active" || !verified || !live {
		return failed(api(403, "FORBIDDEN"))
	}
	return tx, nil
}
func adminText(value string, max int) bool {
	if len([]rune(strings.TrimSpace(value))) < 2 || len([]rune(value)) > max {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}
func decodeAdmin(w http.ResponseWriter, r *http.Request, out any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return api(415, "JSON_REQUIRED")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return api(400, "INVALID_REQUEST")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return api(400, "INVALID_REQUEST")
	}
	return nil
}
func auditTx(ctx context.Context, tx pgx.Tx, actor, action, target, reason, outcome string) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform_audit(actor_id,action,target_id,reason,outcome) VALUES($1,$2,$3,$4,$5)`, actor, action, target, strings.TrimSpace(reason), outcome)
	return err
}
func (s *Service) publicStatus(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	var message string
	if err := s.pool.QueryRow(r.Context(), `SELECT maintenance,public_message FROM platform_system WHERE singleton=true`).Scan(&enabled, &message); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"maintenance": enabled, "message": message})
}
func (s *Service) adminStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	var active, waiting, queued, mailQueued, mailFailed int
	var maintenance bool
	var message string
	err := s.pool.QueryRow(r.Context(), `SELECT maintenance,public_message,(SELECT count(*) FROM platform_matches WHERE status='active'),(SELECT count(*) FROM platform_rooms WHERE status='waiting'),(SELECT count(*) FROM platform_queue),(SELECT count(*) FROM auth_mail_outbox WHERE status IN('queued','sending')),(SELECT count(*) FROM auth_mail_outbox WHERE status='failed') FROM platform_system WHERE singleton=true`).Scan(&maintenance, &message, &active, &waiting, &queued, &mailQueued, &mailFailed)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"maintenance": maintenance, "message": message, "active_matches": active, "waiting_rooms": waiting, "queued": queued, "mail_queued": mailQueued, "mail_failed": mailFailed})
}
func (s *Service) adminMaintenance(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled *bool  `json:"enabled"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := decodeAdmin(w, r, &b); err != nil {
		failure(w, err)
		return
	}
	if b.Enabled == nil || !adminText(b.Reason, 200) || len([]rune(b.Message)) > 160 {
		failure(w, api(400, "REASON_REQUIRED"))
		return
	}
	if !*b.Enabled {
		b.Message = ""
	} else if b.Message == "" {
		b.Message = "平台维护中，暂不接受新对局。"
	}
	tx, err := s.beginAdmin(r.Context(), r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE platform_system SET maintenance=$1,public_message=$2,updated_at=now() WHERE singleton=true`, *b.Enabled, b.Message)
	if err == nil {
		err = auditTx(r.Context(), tx, actor, "maintenance:"+strconv.FormatBool(*b.Enabled), "system", b.Reason, "succeeded")
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"maintenance": *b.Enabled})
}
func (s *Service) adminUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	var b struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := decodeAdmin(w, r, &b); err != nil {
		failure(w, err)
		return
	}
	if (b.Status != "active" && b.Status != "restricted") || !adminText(b.Reason, 200) {
		failure(w, api(400, "INVALID_STATUS_OR_REASON"))
		return
	}
	target := r.PathValue("id")
	if actor == target {
		failure(w, api(409, "CANNOT_RESTRICT_SELF"))
		return
	}
	tx, err := s.beginAdmin(r.Context(), r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM auth_users WHERE id=$1 FOR UPDATE`, target).Scan(&status); errors.Is(err, pgx.ErrNoRows) || status == "deleted" {
		failure(w, api(404, "USER_NOT_FOUND"))
		return
	} else if err != nil {
		failure(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE auth_users SET status=$2,updated_at=now() WHERE id=$1`, target, b.Status)
	if err == nil && b.Status == "restricted" {
		for _, q := range []string{
			`UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`,
			`UPDATE platform_bot_credentials SET revoked_at=now() WHERE bot_id IN(SELECT id FROM platform_bots WHERE owner_id=$1) AND revoked_at IS NULL`,
			`UPDATE platform_bot_sessions SET revoked_at=now() WHERE bot_id IN(SELECT id FROM platform_bots WHERE owner_id=$1) AND revoked_at IS NULL`,
			`DELETE FROM platform_queue WHERE user_id=$1`,
			`UPDATE platform_seats SET continuous=false WHERE user_id=$1`,
			`UPDATE platform_seats SET control_epoch=control_epoch+1,controller='',controller_session='',connected_until=NULL,leave_after_hand=true WHERE user_id=$1 AND active`,
			`DELETE FROM platform_seats s USING platform_rooms r WHERE s.room_id=r.id AND r.status='waiting' AND s.user_id=$1`,
		} {
			if _, err = tx.Exec(r.Context(), q, target); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = auditTx(r.Context(), tx, actor, "user_status:"+b.Status, target, b.Reason, "succeeded")
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	if b.Status == "restricted" {
		s.OnRevoke(target, "", true)
	}
	write(w, 200, map[string]bool{"ok": true})
}
func (s *Service) adminBot(w http.ResponseWriter, r *http.Request) { s.setBotSuspended(w, r, true) }
func (s *Service) adminRestoreBot(w http.ResponseWriter, r *http.Request) {
	s.setBotSuspended(w, r, false)
}
func (s *Service) setBotSuspended(w http.ResponseWriter, r *http.Request, suspended bool) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if err := decodeAdmin(w, r, &b); err != nil {
		failure(w, err)
		return
	}
	if !adminText(b.Reason, 200) {
		failure(w, api(400, "REASON_REQUIRED"))
		return
	}
	target := r.PathValue("id")
	tx, err := s.beginAdmin(r.Context(), r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), `UPDATE platform_bots SET suspended=$2,enabled=false WHERE id=$1`, target, suspended)
	if err != nil {
		failure(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		failure(w, api(404, "BOT_NOT_FOUND"))
		return
	}
	for _, q := range []string{
		`UPDATE platform_bot_credentials SET revoked_at=now() WHERE bot_id=$1 AND revoked_at IS NULL`,
		`UPDATE platform_bot_sessions SET revoked_at=now() WHERE bot_id=$1 AND revoked_at IS NULL`,
	} {
		if _, err = tx.Exec(r.Context(), q, target); err != nil {
			failure(w, err)
			return
		}
	}
	if suspended {
		for _, q := range []string{
			`UPDATE platform_seats SET continuous=false WHERE bot_id=$1`,
			`UPDATE platform_seats SET control_epoch=control_epoch+1,controller='',controller_session='',connected_until=NULL,leave_after_hand=true WHERE bot_id=$1 AND active`,
			`DELETE FROM platform_queue WHERE bot_id=$1`,
			`DELETE FROM platform_seats s USING platform_rooms r WHERE s.room_id=r.id AND r.status='waiting' AND s.bot_id=$1`,
		} {
			if _, err = tx.Exec(r.Context(), q, target); err != nil {
				break
			}
		}
	}
	action := "bot_suspend"
	if !suspended {
		action = "bot_restore"
	}
	if err == nil {
		err = auditTx(r.Context(), tx, actor, action, target, b.Reason, "succeeded")
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"ok": true, "suspended": suspended, "new_credential_required": true})
}
func (s *Service) adminRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled *bool  `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := decodeAdmin(w, r, &b); err != nil {
		failure(w, err)
		return
	}
	if b.Enabled == nil || !adminText(b.Reason, 200) {
		failure(w, api(400, "REASON_REQUIRED"))
		return
	}
	rid, version := r.PathValue("id"), r.PathValue("version")
	if _, err := s.rule(rid, version); err != nil {
		failure(w, err)
		return
	}
	tx, err := s.beginAdmin(r.Context(), r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO platform_rule_switches(ruleset_id,ruleset_version,enabled) VALUES($1,$2,$3) ON CONFLICT(ruleset_id,ruleset_version) DO UPDATE SET enabled=excluded.enabled,updated_at=now()`, rid, version, *b.Enabled)
	if err == nil {
		err = auditTx(r.Context(), tx, actor, "rule_enabled:"+strconv.FormatBool(*b.Enabled), rid+"@"+version, b.Reason, "succeeded")
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"enabled": *b.Enabled})
}
func (s *Service) adminAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.admin(w, r); !ok {
		return
	}
	before := int64(0)
	if text := r.URL.Query().Get("before"); text != "" {
		var err error
		before, err = strconv.ParseInt(text, 10, 64)
		if err != nil || before < 1 {
			failure(w, api(400, "INVALID_CURSOR"))
			return
		}
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id,actor_id,action,target_id,reason,outcome,created_at FROM platform_audit WHERE ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT 100`, before)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	entries := []any{}
	var next int64
	for rows.Next() {
		var id int64
		var actor, action, target, reason, outcome string
		var created time.Time
		if err = rows.Scan(&id, &actor, &action, &target, &reason, &outcome, &created); err != nil {
			failure(w, err)
			return
		}
		entries = append(entries, map[string]any{"id": id, "actor_id": actor, "action": action, "target_id": target, "reason": reason, "outcome": outcome, "created_at": created})
		next = id
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"entries": entries, "next_before": next})
}

func (s *Service) adminAbortMatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admin(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeAdmin(w, r, &body); err != nil {
		failure(w, err)
		return
	}
	if !adminText(body.Reason, 200) {
		failure(w, api(400, "REASON_REQUIRED"))
		return
	}
	tx, err := s.beginAdmin(r.Context(), r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	m, err := loadMatch(r.Context(), tx, r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if m.Status != "active" {
		failure(w, api(409, "MATCH_NOT_ACTIVE"))
		return
	}
	// Keep the entire engine state and already-settled score untouched. The
	// unfinished hand is not adjudicated as a win or a loss by an operator.
	_, err = tx.Exec(r.Context(), `UPDATE platform_matches SET status='aborted_by_operator',deadline_at=NULL,choices='{}',owner_epoch=owner_epoch+1,interrupted=true,updated_at=now() WHERE id=$1`, m.ID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE platform_rooms SET status='ended' WHERE id=$1`, m.RoomID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE platform_seats SET active=false,control_epoch=control_epoch+1,controller='',controller_session='',connected_until=NULL,leave_after_hand=true WHERE room_id=$1`, m.RoomID)
	}
	if err == nil {
		err = auditTx(r.Context(), tx, actor, "match_abort", m.ID, body.Reason, "succeeded")
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"match_id": m.ID, "status": "aborted_by_operator"})
}

// GrantOperator is an explicit deployment-CLI operation, never a public HTTP route.
func GrantOperator(ctx context.Context, pool *pgxpool.Pool, email, reason string) error {
	if !adminText(reason, 200) {
		return errors.New("operator grant requires a 2–200 character reason")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid string
	if err = tx.QueryRow(ctx, `SELECT id FROM auth_users WHERE email=$1 AND status='active' AND verified FOR UPDATE`, strings.ToLower(strings.TrimSpace(email))).Scan(&uid); err != nil {
		return errors.New("operator must be an existing verified active account")
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET role='operator',updated_at=now() WHERE id=$1`, uid); err != nil {
		return err
	}
	if err = auditTx(ctx, tx, "deployment_cli", "operator_grant", uid, reason, "succeeded"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) rotateInvite(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	code := id("invite")
	res, e := s.pool.Exec(r.Context(), `UPDATE platform_rooms SET invite_hash=$3 WHERE id=$1 AND owner_id=$2 AND status='waiting'`, r.PathValue("id"), u.ID, s.hash(code))
	if e != nil {
		failure(w, e)
		return
	}
	if res.RowsAffected() != 1 {
		failure(w, api(403, "FORBIDDEN"))
		return
	}
	write(w, 200, map[string]any{"invite_code": code})
}
func (s *Service) BeforeDeleteTx(ctx context.Context, tx pgx.Tx, userID string) error {
	var active bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active)`, userID).Scan(&active)
	if e != nil {
		return e
	}
	if active {
		return api(409, "ACTIVE_PARTICIPATION")
	}
	_, e = tx.Exec(ctx, `DELETE FROM platform_queue WHERE user_id=$1`, userID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_bot_credentials SET revoked_at=now() WHERE bot_id IN(SELECT id FROM platform_bots WHERE owner_id=$1)`, userID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_bot_sessions SET revoked_at=now() WHERE bot_id IN(SELECT id FROM platform_bots WHERE owner_id=$1)`, userID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_bots SET enabled=false,name='已注销 Bot' WHERE owner_id=$1`, userID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_seats SET name='已注销用户',control_epoch=control_epoch+1,controller='',controller_session='',connected_until=NULL WHERE user_id=$1`, userID)
	return e
}
