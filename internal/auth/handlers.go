package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Name        string `json:"name"`
		AcceptTerms bool   `json:"accept_terms"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, ok := normalizeEmail(in.Email)
	in.Name = strings.TrimSpace(in.Name)
	if !ok || !validPassword(in.Password) || !validName(in.Name) || !in.AcceptTerms {
		fail(w, 400, "invalid_registration", "请填写有效邮箱、1–40字展示名、15–128字密码并同意服务条款")
		return
	}
	if !s.limit(w, r, "register", email, 5) {
		return
	}
	hashed, err := passwordHash(in.Password)
	if err != nil {
		internal(w)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	id := newID()
	result, err := tx.Exec(r.Context(), `INSERT INTO auth_users(id,email,name,password_hash) VALUES($1,$2,$3,$4) ON CONFLICT(email) DO NOTHING`, id, email, in.Name, hashed)
	if err != nil {
		internal(w)
		return
	}
	if result.RowsAffected() == 1 {
		if err = s.issueToken(r.Context(), tx, id, email, "verify", 24*time.Hour); err != nil {
			internal(w)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w)
		return
	}
	accepted(w)
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, _ := normalizeEmail(in.Email)
	if !s.limit(w, r, "login", email, 10) {
		return
	}
	var user User
	var encoded string
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `SELECT id,name,email,password_hash,verified,role,status FROM auth_users WHERE email=$1 FOR UPDATE`, email).Scan(&user.ID, &user.Name, &user.Email, &encoded, &user.Verified, &user.Role, &user.Status)
	if err != nil {
		encoded = s.dummyHash
	}
	valid := verifyPassword(encoded, in.Password)
	if err != nil || !valid || user.Status == "deleted" {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	// Only a correct password can reveal verification/restriction status.
	secret, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO auth_sessions(id,user_id,secret_hash,csrf_token,user_agent,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, newID(), user.ID, hashSecret(secret), csrf, truncate(r.UserAgent(), 256), time.Now().Add(s.cfg.SessionTTL))
	if err != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	s.setCookie(w, secret)
	respond(w, 200, map[string]any{"user": user, "csrf_token": csrf})
}

func (s *Service) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.limit(w, r, "verify", "", 20) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	userID, email, err := s.lockToken(r.Context(), tx, in.Token, "verify")
	if err != nil {
		fail(w, 400, "invalid_token", "链接无效或已过期，请重新申请")
		return
	}
	result, err := tx.Exec(r.Context(), `UPDATE auth_users SET verified=true,updated_at=now() WHERE id=$1 AND email=$2 AND status='active'`, userID, email)
	if err != nil {
		internal(w)
		return
	}
	if result.RowsAffected() != 1 {
		fail(w, 400, "invalid_token", "链接无效或已过期，请重新申请")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_email_tokens SET consumed_at=now() WHERE user_id=$1 AND purpose='verify' AND consumed_at IS NULL`, userID); err != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]bool{"verified": true})
}

func (s *Service) resendVerification(w http.ResponseWriter, r *http.Request) {
	s.sendAccountToken(w, r, "verify", 24*time.Hour)
}
func (s *Service) forgotPassword(w http.ResponseWriter, r *http.Request) {
	s.sendAccountToken(w, r, "reset", 30*time.Minute)
}
func (s *Service) sendAccountToken(w http.ResponseWriter, r *http.Request, purpose string, ttl time.Duration) {
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, _ := normalizeEmail(in.Email)
	if !s.limit(w, r, "mail:"+purpose, email, 5) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	var verified bool
	err = tx.QueryRow(r.Context(), `SELECT id,verified FROM auth_users WHERE email=$1 AND status<>'deleted' AND (status='active' OR $2='reset') FOR UPDATE`, email, purpose).Scan(&id, &verified)
	if err == nil && !(purpose == "verify" && verified) {
		err = s.issueToken(r.Context(), tx, id, email, purpose, ttl)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	accepted(w)
}

func (s *Service) resetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validPassword(in.Password) {
		fail(w, 400, "invalid_password", "密码须为15–128个字符")
		return
	}
	if !s.limit(w, r, "reset", "", 5) {
		return
	}
	hashed, err := passwordHash(in.Password)
	if err != nil {
		internal(w)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	id, email, err := s.lockToken(r.Context(), tx, in.Token, "reset")
	if err != nil {
		fail(w, 400, "invalid_token", "链接无效或已过期，请重新申请")
		return
	}
	result, err := tx.Exec(r.Context(), `UPDATE auth_users SET password_hash=$2,updated_at=now() WHERE id=$1 AND email=$3 AND status<>'deleted'`, id, hashed, email)
	if err != nil {
		internal(w)
		return
	}
	if result.RowsAffected() != 1 {
		fail(w, 400, "invalid_token", "链接无效或已过期，请重新申请")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_email_tokens SET consumed_at=now() WHERE user_id=$1 AND purpose IN ('reset','email_change') AND consumed_at IS NULL`, id); err != nil {
		internal(w)
		return
	}
	if s.revokeAll(r.Context(), tx, id) != nil {
		internal(w)
		return
	}
	if err = s.enqueueMail(r.Context(), tx, id, email, "OpenMajiang password changed", "您的密码已通过找回流程重置，旧登录会话已退出。如果不是您操作，请立即重新找回密码并撤销 Bot 密钥。"); err != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	s.revoked(id, "", true)
	s.clearCookie(w)
	respond(w, 200, map[string]bool{"reset": true})
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	ids, err := s.revokeSelected(r.Context(), v.User.ID, v.ID, v.ID)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			fail(w, 401, "unauthenticated", "当前会话无效")
			return
		}
		internal(w)
		return
	}
	for _, id := range ids {
		s.revoked(v.User.ID, id, false)
	}
	s.clearCookie(w)
	respond(w, 200, map[string]bool{"logged_out": true})
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	respond(w, 200, map[string]any{"user": v.User, "csrf_token": v.CSRF})
}
func (s *Service) updateMe(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validName(in.Name) {
		fail(w, 400, "invalid_name", "展示名须为1–40字，不能含控制字符")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `UPDATE auth_users SET name=$2,updated_at=now() WHERE id=$1 AND status<>'deleted'`, v.User.ID, in.Name); err != nil {
		internal(w)
		return
	}
	v.User.Name = in.Name
	respond(w, 200, map[string]any{"user": v.User})
}

func (s *Service) sessions(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id,user_agent,created_at,last_seen_at,expires_at FROM auth_sessions WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>now() ORDER BY last_seen_at DESC LIMIT 100`, v.User.ID)
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	type item struct {
		ID         string    `json:"id"`
		UserAgent  string    `json:"user_agent"`
		CreatedAt  time.Time `json:"created_at"`
		LastSeenAt time.Time `json:"last_seen_at"`
		ExpiresAt  time.Time `json:"expires_at"`
		Current    bool      `json:"current"`
	}
	result := []item{}
	for rows.Next() {
		var x item
		if rows.Scan(&x.ID, &x.UserAgent, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt) != nil {
			internal(w)
			return
		}
		x.Current = x.ID == v.ID
		result = append(result, x)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"sessions": result})
}

func (s *Service) emailDelivery(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var status string
	var attempts int
	var created, available time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT status,attempts,created_at,available_at FROM auth_mail_outbox WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, v.User.ID).Scan(&status, &attempts, &created, &available)
	if errors.Is(err, pgx.ErrNoRows) {
		respond(w, 200, map[string]any{"status": "none"})
		return
	}
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"status": status, "attempts": attempts, "created_at": created, "next_attempt_at": available, "notice": "sent 表示 SMTP 已接受邮件，不代表已送达收件箱。"})
}

func (s *Service) revokeSession(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	target := r.PathValue("id")
	ids, err := s.revokeSelected(r.Context(), v.User.ID, v.ID, target)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			fail(w, 401, "unauthenticated", "当前会话无效")
			return
		}
		internal(w)
		return
	}
	if len(ids) == 0 && target != "others" {
		fail(w, 404, "session_not_found", "设备不存在")
		return
	}
	for _, id := range ids {
		s.revoked(v.User.ID, id, false)
	}
	if target == v.ID {
		s.clearCookie(w)
	}
	respond(w, 200, map[string]bool{"revoked": true})
}

func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.limit(w, r, "sensitive", v.User.Email, 5) {
		return
	}
	if !validPassword(in.NewPassword) || !s.requirePassword(r.Context(), v.User.ID, in.CurrentPassword) {
		fail(w, 400, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	hashed, err := passwordHash(in.NewPassword)
	if err != nil {
		internal(w)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.lockCredentials(r.Context(), tx, v, in.CurrentPassword) {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_users SET password_hash=$2,updated_at=now() WHERE id=$1 AND status<>'deleted'`, v.User.ID, hashed); err != nil {
		internal(w)
		return
	}
	if s.revokeAll(r.Context(), tx, v.User.ID) != nil {
		internal(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_email_tokens SET consumed_at=now() WHERE user_id=$1 AND purpose IN ('reset','email_change') AND consumed_at IS NULL`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if s.enqueueMail(r.Context(), tx, v.User.ID, v.User.Email, "OpenMajiang password changed", "您的密码已修改，所有旧登录会话已退出。如果不是您操作，请立即找回密码并撤销 Bot 密钥。") != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	s.revoked(v.User.ID, "", true)
	s.clearCookie(w)
	respond(w, 200, map[string]bool{"changed": true, "login_required": true})
}

func (s *Service) requestEmailChange(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, valid := normalizeEmail(in.Email)
	if !valid {
		fail(w, 400, "invalid_email", "邮箱格式无效")
		return
	}
	if !s.limit(w, r, "sensitive", v.User.Email, 5) {
		return
	}
	if !s.requirePassword(r.Context(), v.User.ID, in.Password) {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.lockCredentials(r.Context(), tx, v, in.Password) {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	var id string
	if tx.QueryRow(r.Context(), `SELECT id FROM auth_users WHERE id=$1 AND status='active' FOR UPDATE`, v.User.ID).Scan(&id) != nil {
		fail(w, 403, "account_restricted", "账号当前不能更换邮箱")
		return
	}
	// Always return the same response if another account owns the address.
	var occupied bool
	if tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_users WHERE email=$1 AND id<>$2)`, email, v.User.ID).Scan(&occupied) != nil {
		internal(w)
		return
	}
	if !occupied && email != v.User.Email {
		if s.issueToken(r.Context(), tx, v.User.ID, email, "email_change", 30*time.Minute) != nil {
			internal(w)
			return
		}
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	accepted(w)
}

func (s *Service) confirmEmailChange(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.limit(w, r, "email_confirm", v.User.Email, 10) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	tokenUser, email, err := s.lockToken(r.Context(), tx, in.Token, "email_change")
	if err != nil || tokenUser != v.User.ID {
		fail(w, 400, "invalid_token", "链接无效或已过期，请重新申请")
		return
	}
	var sessionActive bool
	if tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, v.ID, v.User.ID).Scan(&sessionActive) != nil || !sessionActive {
		fail(w, 401, "unauthenticated", "当前会话无效")
		return
	}
	result, err := tx.Exec(r.Context(), `UPDATE auth_users SET email=$2,verified=true,updated_at=now() WHERE id=$1 AND status='active'`, v.User.ID, email)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			fail(w, 400, "invalid_token", "链接无法使用，请重新申请")
			return
		}
		internal(w)
		return
	}
	if result.RowsAffected() != 1 {
		fail(w, 403, "account_restricted", "账号当前不能更换邮箱")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_email_tokens SET consumed_at=now() WHERE user_id=$1 AND consumed_at IS NULL`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if s.revokeAll(r.Context(), tx, v.User.ID) != nil {
		internal(w)
		return
	}
	if s.enqueueMail(r.Context(), tx, v.User.ID, v.User.Email, "OpenMajiang email changed", "您的账号邮箱刚刚被修改，旧登录会话已退出。如果不是您操作，请联系站点支持处理。") != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	s.revoked(v.User.ID, "", true)
	s.clearCookie(w)
	respond(w, 200, map[string]bool{"changed": true, "login_required": true})
}

func (s *Service) deleteAccount(w http.ResponseWriter, r *http.Request) {
	v, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.limit(w, r, "sensitive", v.User.Email, 5) {
		return
	}
	if !s.requirePassword(r.Context(), v.User.ID, in.Password) {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	if s.cfg.BeforeDelete != nil {
		if err := s.cfg.BeforeDelete(r.Context(), v.User.ID); err != nil {
			fail(w, 409, "active_match", "请先完成或退出当前比赛后再注销")
			return
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.lockCredentials(r.Context(), tx, v, in.Password) {
		fail(w, 401, "credentials_rejected", "凭据或请求不被接受")
		return
	}
	if s.cfg.BeforeDeleteTx != nil {
		if err := s.cfg.BeforeDeleteTx(r.Context(), tx, v.User.ID); err != nil {
			fail(w, 409, "active_match", "请先完成或退出当前比赛后再注销")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_users SET email=NULL,name='已注销用户',password_hash='',verified=false,status='deleted',role='user',updated_at=now() WHERE id=$1`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if s.revokeAll(r.Context(), tx, v.User.ID) != nil {
		internal(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_email_tokens WHERE user_id=$1`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_mail_outbox WHERE user_id=$1`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_sessions SET user_agent='' WHERE user_id=$1`, v.User.ID); err != nil {
		internal(w)
		return
	}
	if tx.Commit(r.Context()) != nil {
		internal(w)
		return
	}
	s.revoked(v.User.ID, "", true)
	if s.cfg.OnDelete != nil {
		s.cfg.OnDelete(v.User.ID)
	}
	s.clearCookie(w)
	respond(w, 200, map[string]bool{"deleted": true})
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
