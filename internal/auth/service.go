package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnauthenticated = errors.New("authentication required")

type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

type Config struct {
	BaseURL           string
	CookieSecure      bool
	CookieName        string
	SessionTTL        time.Duration
	TrustedProxyCIDRs []string
	SMTP              SMTPConfig
	Mailer            Mailer
	// MailEncryptionKey is exactly 32 random bytes, stable across deployments.
	// It protects one-time link plaintext while waiting for SMTP delivery.
	MailEncryptionKey []byte
	OnRevoke          func(userID, sessionID string, all bool)
	BeforeDelete      func(ctx context.Context, userID string) error
	// BeforeDeleteTx runs under the user row lock in the anonymization transaction.
	// Every platform admission transaction must acquire that same row lock.
	BeforeDeleteTx func(ctx context.Context, tx pgx.Tx, userID string) error
	OnDelete       func(userID string)
}

type Service struct {
	pool           *pgxpool.Pool
	cfg            Config
	origin         string
	aead           cipher.AEAD
	dummyHash      string
	limiter        rateLimiter
	trustedProxies []netip.Prefix
}

type session struct {
	ID   string
	User User
	CSRF string
}

func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if pool == nil {
		return nil, errors.New("auth requires PostgreSQL")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid auth BaseURL")
	}
	if u.Scheme == "https" && !cfg.CookieSecure {
		return nil, errors.New("HTTPS requires secure cookies")
	}
	if cfg.CookieName == "" {
		cfg.CookieName = "omj_session"
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	var proxies []netip.Prefix
	for _, cidr := range cfg.TrustedProxyCIDRs {
		p, e := netip.ParsePrefix(strings.TrimSpace(cidr))
		if e != nil {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		proxies = append(proxies, p)
	}
	block, err := aes.NewCipher(cfg.MailEncryptionKey)
	if err != nil || len(cfg.MailEncryptionKey) != 32 {
		return nil, errors.New("MailEncryptionKey must be 32 bytes")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if cfg.Mailer == nil {
		if _, _, err := net.SplitHostPort(cfg.SMTP.Address); err != nil {
			return nil, errors.New("SMTP address must be host:port")
		}
		if _, err := mail.ParseAddress(cfg.SMTP.From); err != nil {
			return nil, errors.New("SMTP sender is required")
		}
		if cfg.SMTP.Username != "" && !cfg.SMTP.StartTLS {
			return nil, errors.New("SMTP credentials require STARTTLS")
		}
		cfg.Mailer = &SMTPMailer{Config: cfg.SMTP}
	}
	dummy, err := passwordHash("nonexistent-account-dummy-password")
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, cfg: cfg, origin: u.Scheme + "://" + u.Host, aead: aead, dummyHash: dummy, limiter: rateLimiter{entries: map[string]rateEntry{}}, trustedProxies: proxies}, nil
}

func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/register", s.mutation(s.register, false))
	mux.HandleFunc("POST /v1/auth/login", s.mutation(s.login, false))
	mux.HandleFunc("POST /v1/auth/verify-email", s.mutation(s.verifyEmail, false))
	mux.HandleFunc("POST /v1/auth/resend-verification", s.mutation(s.resendVerification, false))
	mux.HandleFunc("POST /v1/auth/forgot-password", s.mutation(s.forgotPassword, false))
	mux.HandleFunc("POST /v1/auth/reset-password", s.mutation(s.resetPassword, false))
	mux.HandleFunc("POST /v1/auth/logout", s.mutation(s.logout, true))
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("PATCH /v1/me", s.mutation(s.updateMe, true))
	mux.HandleFunc("GET /v1/me/sessions", s.sessions)
	mux.HandleFunc("GET /v1/me/email-delivery", s.emailDelivery)
	mux.HandleFunc("DELETE /v1/me/sessions/{id}", s.mutation(s.revokeSession, true))
	mux.HandleFunc("POST /v1/me/password", s.mutation(s.changePassword, true))
	mux.HandleFunc("POST /v1/me/email-change/request", s.mutation(s.requestEmailChange, true))
	mux.HandleFunc("POST /v1/me/email-change/confirm", s.mutation(s.confirmEmailChange, true))
	mux.HandleFunc("POST /v1/me/delete-account", s.mutation(s.deleteAccount, true))
}

func (s *Service) Authenticate(r *http.Request) (User, error) {
	v, err := s.getSession(r)
	return v.User, err
}

func (s *Service) SessionID(r *http.Request) (string, error) {
	v, err := s.getSession(r)
	return v.ID, err
}

// ValidateCSRF is shared by authenticated platform mutations. Browser WS
// upgrades separately validate Origin and bind SessionID to their lease.
func (s *Service) ValidateCSRF(r *http.Request) bool {
	if r.Header.Get("Origin") != s.origin {
		return false
	}
	v, err := s.getSession(r)
	if err != nil {
		return false
	}
	t := r.Header.Get("X-CSRF-Token")
	return len(t) == len(v.CSRF) && subtle.ConstantTimeCompare([]byte(t), []byte(v.CSRF)) == 1
}

func (s *Service) PublicName(ctx context.Context, userID string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT CASE WHEN status='deleted' THEN '已注销用户' ELSE name END FROM auth_users WHERE id=$1`, userID).Scan(&name)
	return name, err
}

func (s *Service) getSession(r *http.Request) (session, error) {
	var v session
	cookie, err := r.Cookie(s.cfg.CookieName)
	if err != nil || len(cookie.Value) != 43 {
		return v, ErrUnauthenticated
	}
	err = s.pool.QueryRow(r.Context(), `SELECT s.id,s.csrf_token,u.id,u.name,COALESCE(u.email,''),u.verified,u.role,u.status FROM auth_sessions s JOIN auth_users u ON u.id=s.user_id WHERE s.secret_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.status<>'deleted'`, hashSecret(cookie.Value)).Scan(&v.ID, &v.CSRF, &v.User.ID, &v.User.Name, &v.User.Email, &v.User.Verified, &v.User.Role, &v.User.Status)
	if err != nil {
		return session{}, ErrUnauthenticated
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE auth_sessions SET last_seen_at=now() WHERE id=$1 AND last_seen_at<now()-interval '5 minutes'`, v.ID)
	return v, nil
}

func (s *Service) mutation(next http.HandlerFunc, authenticated bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != s.origin {
			fail(w, http.StatusForbidden, "origin_not_allowed", "请求来源不被接受")
			return
		}
		if authenticated {
			v, err := s.getSession(r)
			if err != nil {
				fail(w, 401, "unauthenticated", "当前会话无效")
				return
			}
			token := r.Header.Get("X-CSRF-Token")
			if len(token) != len(v.CSRF) || subtle.ConstantTimeCompare([]byte(token), []byte(v.CSRF)) != 1 {
				fail(w, 403, "csrf_failed", "请求验证失败")
				return
			}
		}
		next(w, r)
	}
}

func (s *Service) requireSession(w http.ResponseWriter, r *http.Request) (session, bool) {
	v, err := s.getSession(r)
	if err != nil {
		fail(w, 401, "unauthenticated", "当前会话无效")
		return v, false
	}
	return v, true
}
func (s *Service) requirePassword(ctx context.Context, userID, password string) bool {
	var encoded string
	if s.pool.QueryRow(ctx, `SELECT password_hash FROM auth_users WHERE id=$1 AND status<>'deleted'`, userID).Scan(&encoded) != nil {
		return false
	}
	return verifyPassword(encoded, password)
}

// Lock the credential row and recheck both password and session at mutation time.
// This fences an in-flight request against a concurrent password reset/revoke.
func (s *Service) lockCredentials(ctx context.Context, tx pgx.Tx, v session, password string) bool {
	var encoded string
	if tx.QueryRow(ctx, `SELECT password_hash FROM auth_users WHERE id=$1 AND status<>'deleted' FOR UPDATE`, v.User.ID).Scan(&encoded) != nil {
		return false
	}
	var active bool
	if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, v.ID, v.User.ID).Scan(&active) != nil || !active {
		return false
	}
	return verifyPassword(encoded, password)
}

// Token consumers and creators use the same user -> token lock order, avoiding
// resend/consume and reset/change-password deadlocks. The second query fences
// expiry or consumption while the first query was waiting for the user lock.
func (s *Service) lockToken(ctx context.Context, tx pgx.Tx, token, purpose string) (userID, email string, err error) {
	err = tx.QueryRow(ctx, `SELECT user_id FROM auth_email_tokens WHERE token_hash=$1 AND purpose=$2 AND consumed_at IS NULL AND expires_at>now()`, hashSecret(token), purpose).Scan(&userID)
	if err != nil {
		return
	}
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM auth_users WHERE id=$1 AND status<>'deleted' AND (status='active' OR $2='reset') FOR UPDATE`, userID, purpose).Scan(&locked)
	if err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT target_email FROM auth_email_tokens WHERE token_hash=$1 AND user_id=$2 AND purpose=$3 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, hashSecret(token), userID, purpose).Scan(&email)
	return
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if content := r.Header.Get("Content-Type"); !strings.HasPrefix(content, "application/json") {
		fail(w, 415, "json_required", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		fail(w, 400, "invalid_request", "请求内容无效")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid_request", "请求内容无效")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func accepted(w http.ResponseWriter) {
	respond(w, 202, map[string]any{"message": "如该请求符合条件，邮件将加入发送队列，请检查收件箱。"})
}
func internal(w http.ResponseWriter) { fail(w, 500, "internal_error", "请求暂时无法完成") }
func normalizeEmail(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) > 254 || strings.ContainsAny(v, "\r\n") {
		return "", false
	}
	a, err := mail.ParseAddress(v)
	return v, err == nil && a.Address == v && strings.Contains(v, "@")
}
func validName(v string) bool {
	n := utf8.RuneCountInString(v)
	if !utf8.ValidString(v) || n < 1 || n > 40 {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func newID() string {
	v, err := randomToken()
	if err != nil {
		panic("system random source failed")
	}
	return v
}

type rateEntry struct {
	Start time.Time
	Count int
}
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
}

func (l *rateLimiter) allow(key string, limit int, period time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.entries) > 10000 {
		for k, v := range l.entries {
			if now.Sub(v.Start) > time.Hour {
				delete(l.entries, k)
			}
		}
		if len(l.entries) > 20000 {
			return false
		}
	}
	e := l.entries[key]
	if now.Sub(e.Start) >= period {
		e = rateEntry{Start: now}
	}
	e.Count++
	l.entries[key] = e
	return e.Count <= limit
}
func (s *Service) limit(w http.ResponseWriter, r *http.Request, operation, email string, limit int) bool {
	ip := s.clientIP(r)
	if !s.limiter.allow(operation+":ip:"+ip, limit, time.Minute) || email != "" && !s.limiter.allow(operation+":account:"+hashSecret(email), limit, time.Minute) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limited", "请求过于频繁，请稍后重试")
		return false
	}
	return true
}

// ClientIP applies the configured trusted-proxy chain policy for all services.
func (s *Service) ClientIP(r *http.Request) string { return s.clientIP(r) }

func (s *Service) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, p := range s.trustedProxies {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return peer.String()
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(parts) > 20 {
		return peer.String()
	}
	current := peer
	for i := len(parts) - 1; i >= 0; i-- {
		if !trusted(current) {
			break
		}
		next, e := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if e != nil {
			return peer.String()
		}
		current = next.Unmap()
	}
	return current.String()
}

func (s *Service) setCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{Name: s.cfg.CookieName, Value: secret, Path: "/", Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(s.cfg.SessionTTL.Seconds()), Expires: time.Now().Add(s.cfg.SessionTTL)})
}
func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cfg.CookieName, Value: "", Path: "/", Secure: s.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Service) revokeAll(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	return err
}
func (s *Service) revoked(userID, sessionID string, all bool) {
	if s.cfg.OnRevoke != nil {
		s.cfg.OnRevoke(userID, sessionID, all)
	}
}

func (s *Service) revokeSelected(ctx context.Context, userID, currentID, target string) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM auth_users WHERE id=$1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		return nil, err
	}
	var actorActive bool
	if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, currentID, userID).Scan(&actorActive) != nil || !actorActive {
		return nil, ErrUnauthenticated
	}
	query := `UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND id=$2 AND revoked_at IS NULL RETURNING id`
	if target == "others" {
		query = `UPDATE auth_sessions SET revoked_at=now() WHERE user_id=$1 AND id<>$2 AND revoked_at IS NULL RETURNING id`
		target = currentID
	}
	rows, err := tx.Query(ctx, query, userID, target)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return ids, tx.Commit(ctx)
}

// RevokeUser is the administration entry point for durable all-device revocation.
func (s *Service) RevokeUser(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM auth_users WHERE id=$1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		return err
	}
	if err = s.revokeAll(ctx, tx, userID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	s.revoked(userID, "", true)
	return nil
}
