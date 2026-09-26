package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/internal/testplugins/threeplayer"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

type opsMailer struct{}

func (opsMailer) Send(context.Context, string, string, string) error { return nil }

type opsLogin struct {
	cookie    *http.Cookie
	csrf, uid string
}

func opsRequest(t *testing.T, h http.Handler, method, path string, body any, login opsLogin) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "http://localhost:8080"+path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.50:12345"
	r.Header.Set("Origin", "http://localhost:8080")
	r.Header.Set("Content-Type", "application/json")
	if login.cookie != nil {
		r.AddCookie(login.cookie)
		r.Header.Set("X-CSRF-Token", login.csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func opsStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("want %d got %d: %s", status, w.Code, w.Body.String())
	}
}
func opsFixture(t *testing.T) (*Service, *http.ServeMux, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL administration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "ops_test_" + strings.ReplaceAll(strings.ToLower(id("s")), "-", "_")
	schema = strings.ReplaceAll(schema, "_", "a")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if err = auth.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = AdminMigrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(pool, auth.Config{BaseURL: "http://localhost:8080", MailEncryptionKey: bytes.Repeat([]byte{5}, 32), Mailer: opsMailer{}})
	if err != nil {
		t.Fatal(err)
	}
	rule := threeplayer.New()
	s, err := New(pool, Config{Auth: a, BaseURL: "http://localhost:8080", TokenHashKey: bytes.Repeat([]byte{6}, 32), Rules: map[string]rulesdk.Rule{"fixture": rule}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	s.RegisterRoutes(mux)
	return s, mux, pool
}
func opsAccount(t *testing.T, mux *http.ServeMux, pool *pgxpool.Pool, email string, operator bool) opsLogin {
	t.Helper()
	w := opsRequest(t, mux, "POST", "/v1/auth/register", map[string]any{"email": email, "password": "operator testing password", "name": "测试用户", "accept_terms": true}, opsLogin{})
	opsStatus(t, w, 202)
	if _, err := pool.Exec(context.Background(), `UPDATE auth_users SET verified=true WHERE email=$1`, email); err != nil {
		t.Fatal(err)
	}
	if operator {
		if err := GrantOperator(context.Background(), pool, email, "测试部署授权"); err != nil {
			t.Fatal(err)
		}
	}
	return opsSignIn(t, mux, email)
}
func opsSignIn(t *testing.T, mux *http.ServeMux, email string) opsLogin {
	t.Helper()
	w := opsRequest(t, mux, "POST", "/v1/auth/login", map[string]string{"email": email, "password": "operator testing password"}, opsLogin{})
	opsStatus(t, w, 200)
	var data struct {
		CSRF string `json:"csrf_token"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return opsLogin{w.Result().Cookies()[0], data.CSRF, data.User.ID}
}
func TestAdministrationPostgres(t *testing.T) {
	s, mux, pool := opsFixture(t)
	ctx := context.Background()
	operator := opsAccount(t, mux, pool, "operator@example.test", true)
	player := opsAccount(t, mux, pool, "player@example.test", false)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/admin/status", nil, opsLogin{}), 401)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/admin/status", nil, player), 403)
	badCSRF := operator
	badCSRF.csrf = "incorrect"
	maintenance := map[string]any{"enabled": true, "message": "计划维护", "reason": "进行部署演练"}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/maintenance", maintenance, badCSRF), 403)
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/maintenance", maintenance, player), 403)
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/maintenance", maintenance, operator), 200)
	public := opsRequest(t, mux, "GET", "/v1/public/status", nil, opsLogin{})
	opsStatus(t, public, 200)
	if !strings.Contains(public.Body.String(), `"maintenance":true`) || strings.Contains(public.Body.String(), "进行部署演练") {
		t.Fatal("maintenance public projection leaked audit reason")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.requireAdmissionsTx(ctx, tx, "", "")
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("maintenance admitted a new table")
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/maintenance", map[string]any{"enabled": false, "reason": "部署检查完成"}, operator), 200)
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/users/"+player.uid+"/status", map[string]string{"status": "restricted", "reason": "测试账号限制"}, operator), 200)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/me", nil, player), 401)
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/users/"+player.uid+"/status", map[string]string{"status": "active", "reason": "解除测试限制"}, operator), 200)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/me", nil, player), 401)
	player = opsSignIn(t, mux, "player@example.test")
	if _, err = pool.Exec(ctx, `INSERT INTO platform_bots(id,owner_id,name) VALUES('test-bot',$1,'test')`, player.uid); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO platform_bot_credentials(id,bot_id,secret_hash) VALUES('test-key','test-bot','hashed-key')`); err != nil {
		t.Fatal(err)
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/bots/test-bot/disable", map[string]string{"reason": "测试停用 Bot"}, operator), 200)
	var blocked bool
	if err = pool.QueryRow(ctx, `SELECT suspended AND NOT enabled AND EXISTS(SELECT 1 FROM platform_bot_credentials WHERE bot_id='test-bot' AND revoked_at IS NOT NULL) FROM platform_bots WHERE id='test-bot'`).Scan(&blocked); err != nil || !blocked {
		t.Fatal("suspension did not revoke credentials", err)
	}
	opsStatus(t, opsRequest(t, mux, "PATCH", "/v1/bots/test-bot", map[string]bool{"enabled": true}, player), 403)
	if err = pool.QueryRow(ctx, `SELECT suspended FROM platform_bots WHERE id='test-bot'`).Scan(&blocked); err != nil || !blocked {
		t.Fatal("owner could lift operator suspension")
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/bots/test-bot/restore", map[string]string{"reason": "解除测试停用"}, operator), 200)
	var stillRevoked bool
	if err = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM platform_bot_credentials WHERE id='test-key'`).Scan(&stillRevoked); err != nil || !stillRevoked {
		t.Fatal("restoring bot revived old credential")
	}
	manifest := threeplayer.New().Manifest()
	rulePath := "/v1/admin/rulesets/" + manifest.ID + "/versions/" + manifest.Version + "/status"
	opsStatus(t, opsRequest(t, mux, "POST", rulePath, map[string]any{"enabled": false, "reason": "规则版本停止新桌"}, operator), 200)
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.requireAdmissionsTx(ctx, tx, manifest.ID, manifest.Version)
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("disabled rule admitted new table")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO platform_rooms(id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,online_profile,capacity,status,match_id) VALUES('fault-room','fault',$1,'bot_only','fixture','1','fixture','fixture',3,'playing','fault-match')`, operator.uid); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO platform_matches(id,room_id,ruleset_id,ruleset_version,match_format,artifact_hash,manifest,config,state,owner_id,owner_until) VALUES('fault-match','fault-room','fixture','1','fixture','fixture','{}','{}','{"settled_score_marker":42}',$1,now()+interval '5 seconds')`, s.id); err != nil {
		t.Fatal(err)
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/matches/fault-match/abort", map[string]string{"reason": "故障桌停止执行"}, operator), 200)
	var preserved bool
	if err = pool.QueryRow(ctx, `SELECT status='aborted_by_operator' AND state->>'settled_score_marker'='42' AND deadline_at IS NULL FROM platform_matches WHERE id='fault-match'`).Scan(&preserved); err != nil || !preserved {
		t.Fatal("operator abort rewrote settled state or remained active", err)
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/matches/fault-match/abort", map[string]string{"reason": "重复中止必须拒绝"}, operator), 409)
	audit := opsRequest(t, mux, "GET", "/v1/admin/audit", nil, operator)
	opsStatus(t, audit, 200)
	if !strings.Contains(audit.Body.String(), "operator_grant") || !strings.Contains(audit.Body.String(), "测试账号限制") || strings.Contains(audit.Body.String(), "hashed-key") {
		t.Fatal("audit coverage or secret isolation")
	}
	if !strings.Contains(audit.Body.String(), "http_409") || strings.Contains(audit.Body.String(), "settled_score_marker") {
		t.Fatal("rejection audit or private state isolation")
	}
	opsStatus(t, opsRequest(t, mux, "POST", "/v1/admin/users/"+operator.uid+"/status", map[string]string{"status": "restricted", "reason": "不能限制自己"}, operator), 409)
}
func TestAdminInputIsStrict(t *testing.T) {
	for _, text := range []string{"", "x", "原因\n换行", strings.Repeat("x", 201)} {
		if adminText(text, 200) {
			t.Fatalf("accepted invalid reason %q", text)
		}
	}
	if !adminText("计划维护", 200) {
		t.Fatal("valid reason rejected")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"reason":"维护"}{"extra":true}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	var v struct {
		Reason string `json:"reason"`
	}
	if decodeAdmin(w, r, &v) == nil {
		t.Fatal("accepted trailing JSON")
	}
}
