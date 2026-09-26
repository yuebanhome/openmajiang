package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPasswordHashAndValidation(t *testing.T) {
	if validPassword("too short") || validPassword(strings.Repeat("x", 129)) || !validPassword("密码可以包含 空格 与 Unicode 字符") {
		t.Fatal("password length policy")
	}
	h, err := passwordHash("long test password 123")
	if err != nil {
		t.Fatal(err)
	}
	if h == "long test password 123" || !verifyPassword(h, "long test password 123") || verifyPassword(h, "long wrong password 123") {
		t.Fatal("password hash verification")
	}
	if verifyPassword("$argon2id$v=19$m=4294967295,t=9,p=1$abc$def", "whatever") {
		t.Fatal("unbounded parameter accepted")
	}
}

func TestInputAndLimiter(t *testing.T) {
	e, ok := normalizeEmail("  Player@Example.COM  ")
	if !ok || e != "player@example.com" {
		t.Fatal("email normalization")
	}
	for _, e := range []string{"Name <user@example.com>", "foo\r\n@example.com", "no-email"} {
		if _, ok := normalizeEmail(e); ok {
			t.Errorf("accepted %q", e)
		}
	}
	l := rateLimiter{entries: map[string]rateEntry{}}
	if !l.allow("key", 1, time.Minute) || l.allow("key", 1, time.Minute) {
		t.Fatal("limit")
	}
	if validName("bad\nname") || !validName("麻将玩家") {
		t.Fatal("name policy")
	}
}

func TestOriginAndCSRFRejectWithoutDatabase(t *testing.T) {
	s := &Service{origin: "https://mahjong.example"}
	h := s.mutation(func(w http.ResponseWriter, r *http.Request) { t.Fatal("handler reached") }, true)
	r := httptest.NewRequest("POST", "https://mahjong.example/v1/auth/logout", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

type captureMailer struct {
	mu     sync.Mutex
	bodies []string
}

func (m *captureMailer) Send(_ context.Context, _, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	return nil
}
func (m *captureMailer) lastToken(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		t.Fatal("no message delivered")
	}
	match := regexp.MustCompile(`#token=([A-Za-z0-9_-]+)`).FindStringSubmatch(m.bodies[len(m.bodies)-1])
	if len(match) != 2 {
		t.Fatal("token link missing")
	}
	return match[1]
}

func integrationService(t *testing.T) (*Service, *http.ServeMux, *captureMailer) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL account lifecycle integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_test_" + hashSecret(newID())[:16]
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
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
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	mailer := &captureMailer{}
	service, err := New(pool, Config{BaseURL: "http://localhost:8080", MailEncryptionKey: bytes.Repeat([]byte{17}, 32), Mailer: mailer})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	service.RegisterRoutes(mux)
	return service, mux, mailer
}

func call(t *testing.T, mux http.Handler, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "http://localhost:8080"+path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.1:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:8080")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected %d got %d: %s", status, w.Code, w.Body.String())
	}
}
func loginTest(t *testing.T, mux http.Handler, email, password string) (*http.Cookie, string) {
	t.Helper()
	w := call(t, mux, "POST", "/v1/auth/login", map[string]string{"email": email, "password": password}, nil, "")
	expectStatus(t, w, 200)
	var v struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode || v.CSRF == "" {
		t.Fatal("cookie or csrf policy")
	}
	return cs[0], v.CSRF
}

func TestAccountLifecyclePostgres(t *testing.T) {
	s, mux, mailer := integrationService(t)
	ctx := context.Background()
	email := "player@example.com"
	password := "first long test password"
	reg := map[string]any{"email": email, "password": password, "name": "测试玩家", "accept_terms": true}
	w := call(t, mux, "POST", "/v1/auth/register", reg, nil, "")
	expectStatus(t, w, 202)
	duplicate := call(t, mux, "POST", "/v1/auth/register", reg, nil, "")
	expectStatus(t, duplicate, 202)
	if duplicate.Body.String() != w.Body.String() {
		t.Fatal("registration enumerates account")
	}
	var encrypted []byte
	if err := s.pool.QueryRow(ctx, `SELECT encrypted_body FROM auth_mail_outbox`).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("#token=")) {
		t.Fatal("outbox contains plaintext link")
	}
	cookie, csrf := loginTest(t, mux, email, password)
	me := call(t, mux, "GET", "/v1/me", nil, cookie, "")
	expectStatus(t, me, 200)
	if !strings.Contains(me.Body.String(), `"verified":false`) {
		t.Fatal("unverified status missing")
	}
	expectStatus(t, call(t, mux, "PATCH", "/v1/me", map[string]string{"name": "changed"}, cookie, ""), 403)
	if !s.deliverOne(ctx) {
		t.Fatal("mail was not claimed")
	}
	token := mailer.lastToken(t)
	var cipherAfter []byte
	if err := s.pool.QueryRow(ctx, `SELECT encrypted_body FROM auth_mail_outbox`).Scan(&cipherAfter); err != nil {
		t.Fatal(err)
	}
	if len(cipherAfter) != 0 {
		t.Fatal("sent message secret retained")
	}
	expectStatus(t, call(t, mux, "GET", "/v1/auth/verify-email", map[string]string{"token": token}, nil, ""), 405)
	// Concurrent consumers must produce exactly one successful verification.
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(t, mux, "POST", "/v1/auth/verify-email", map[string]string{"token": token}, nil, "").Code
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 400 {
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	if success != 1 {
		t.Fatal("verification token is not single-use")
	}
	expectStatus(t, call(t, mux, "PATCH", "/v1/me", map[string]string{"name": "新展示名"}, cookie, csrf), 200)
	expectStatus(t, call(t, mux, "GET", "/v1/me/sessions", nil, cookie, ""), 200)
	expectStatus(t, call(t, mux, "POST", "/v1/auth/forgot-password", map[string]string{"email": email}, nil, ""), 202)
	if !s.deliverOne(ctx) {
		t.Fatal("reset mail missing")
	}
	resetToken := mailer.lastToken(t)
	nextPassword := "second long test password"
	results = make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(t, mux, "POST", "/v1/auth/reset-password", map[string]string{"token": resetToken, "password": nextPassword}, nil, "").Code
		}()
	}
	wg.Wait()
	close(results)
	success = 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 400 {
			t.Fatalf("unexpected reset status %d", status)
		}
	}
	if success != 1 {
		t.Fatal("reset token is not single-use")
	}
	expectStatus(t, call(t, mux, "GET", "/v1/me", nil, cookie, ""), 401)
	expectStatus(t, call(t, mux, "POST", "/v1/auth/login", map[string]string{"email": email, "password": password}, nil, ""), 401)
	cookie, csrf = loginTest(t, mux, email, nextPassword)
	otherCookie, _ := loginTest(t, mux, email, nextPassword)
	expectStatus(t, call(t, mux, "DELETE", "/v1/me/sessions/others", nil, cookie, csrf), 200)
	expectStatus(t, call(t, mux, "GET", "/v1/me", nil, otherCookie, ""), 401)
	expectStatus(t, call(t, mux, "GET", "/v1/me", nil, cookie, ""), 200)
	expectStatus(t, call(t, mux, "POST", "/v1/me/email-change/request", map[string]string{"email": "changed@example.com", "password": nextPassword}, cookie, csrf), 202)
	for s.deliverOne(ctx) {
	}
	emailToken := mailer.lastToken(t)
	expectStatus(t, call(t, mux, "POST", "/v1/me/email-change/confirm", map[string]string{"token": emailToken}, cookie, csrf), 200)
	expectStatus(t, call(t, mux, "GET", "/v1/me", nil, cookie, ""), 401)
	cookie, csrf = loginTest(t, mux, "changed@example.com", nextPassword)
	deleted := false
	s.cfg.OnDelete = func(string) { deleted = true }
	expectStatus(t, call(t, mux, "POST", "/v1/me/delete-account", map[string]string{"password": nextPassword}, cookie, csrf), 200)
	if !deleted {
		t.Fatal("delete callback not called")
	}
	expectStatus(t, call(t, mux, "GET", "/v1/me", nil, cookie, ""), 401)
	var piiGone bool
	if err := s.pool.QueryRow(ctx, `SELECT email IS NULL AND name='已注销用户' AND password_hash='' AND status='deleted' FROM auth_users`).Scan(&piiGone); err != nil || !piiGone {
		t.Fatal("deletion did not anonymize account", err)
	}
}

func TestSMTPDelivery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		p := textproto.NewConn(conn)
		_ = p.PrintfLine("220 localhost ESMTP")
		for {
			line, e := p.ReadLine()
			if e != nil {
				errs <- e
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				_ = p.PrintfLine("250 OK")
			case line == "DATA":
				_ = p.PrintfLine("354 send message")
				data, e := io.ReadAll(p.DotReader())
				if e != nil {
					errs <- e
					return
				}
				result <- string(data)
				_ = p.PrintfLine("250 queued")
			case line == "QUIT":
				_ = p.PrintfLine("221 bye")
				return
			default:
				errs <- io.ErrUnexpectedEOF
				return
			}
		}
	}()
	m := &SMTPMailer{Config: SMTPConfig{Address: listener.Addr().String(), From: "OpenMajiang <noreply@example.com>"}}
	if err = m.Send(context.Background(), "test@example.com", "Account test", "confirmation body"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-result:
		if !strings.Contains(body, "confirmation body") {
			t.Fatal("body missing")
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP timed out")
	}
}
