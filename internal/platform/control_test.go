package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPGSecondWindowReadOnlyAndExplicitControlFencesOldTab(t *testing.T) {
	s := recoveryService(t, mcr.New())
	room, mid := recoveryRoom(t, s, mcr.New(), "practice_1")
	p := room.Seats[0]
	ctx := context.Background()
	session := id("session")
	if _, e := s.pool.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,secret_hash,csrf_token,expires_at) VALUES($1,$2,$3,'test',now()+interval '1 hour')`, session, p.UserID, id("hash")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.pool.Exec(ctx, `UPDATE platform_seats SET controller='',connected_until=NULL WHERE participant_id=$1`, p.ParticipantID); e != nil {
		t.Fatal(e)
	}
	token, epoch, granted, e := s.acquireControl(ctx, p, session, false, false)
	if e != nil || !granted || token == "" {
		t.Fatalf("first claim: %t %v", granted, e)
	}
	second, secondEpoch, granted, e := s.acquireControl(ctx, p, session, false, false)
	if e != nil || granted || second != "" || secondEpoch != epoch {
		t.Fatalf("second tab stole control: %t %v", granted, e)
	}
	replacement, newEpoch, granted, e := s.acquireControl(ctx, p, session, false, true)
	if e != nil || !granted || replacement == token || newEpoch <= epoch {
		t.Fatalf("explicit takeover: %t %v", granted, e)
	}
	if e = s.readyCommand(ctx, p.ParticipantID, room.ID, token, epoch); recoveryCode(e) != "STALE_CONTROL" {
		t.Fatalf("old tab changed readiness: %v", e)
	}
	m := recoveryMatch(t, s, mid)
	rule, _ := s.rule(m.RulesetID, m.RulesetVersion)
	f, e := rule.Inspect(m.State)
	if e != nil {
		t.Fatal(e)
	}
	a := Action{Type: "submit_action", Protocol: "1.0", MatchID: mid, ParticipantID: p.ParticipantID, HandID: handID(mid, f.HandIndex), Assignment: f.HandIndex, Epoch: epoch, CommandID: id("command"), WindowID: f.WindowID}
	if _, e = s.Submit(ctx, p.ParticipantID, token, a); recoveryCode(e) != "STALE_CONTROL" {
		t.Fatalf("old tab accepted: %v", e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, session); e != nil {
		t.Fatal(e)
	}
	s.OnRevoke(p.UserID, session, false)
	if _, _, _, e = s.acquireControl(ctx, p, session, false, true); recoveryCode(e) != "AUTH_EXPIRED" {
		t.Fatalf("revoked session resurrected: %v", e)
	}
	var current string
	if e = s.pool.QueryRow(ctx, `SELECT controller FROM platform_seats WHERE participant_id=$1`, p.ParticipantID).Scan(&current); e != nil || current != "" {
		t.Fatalf("revocation did not clear controller: %q %v", current, e)
	}
}

func TestPGWaitingSnapshotRetainsCurrentControlEpoch(t *testing.T) {
	s := recoveryService(t, mcr.New())
	ctx := context.Background()
	u := auth.User{ID: id("user"), Name: "Waiting", Verified: true, Status: "active"}
	if _, e := s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test',true)`, u.ID, u.ID+"@example.invalid", u.Name); e != nil {
		t.Fatal(e)
	}
	room, _, e := s.create(ctx, u, createRequest{Mode: "human_only", Format: "practice_1"})
	if e != nil {
		t.Fatal(e)
	}
	pid := room.Seats[0].ParticipantID
	if _, e = s.pool.Exec(ctx, `UPDATE platform_seats SET control_epoch=7 WHERE participant_id=$1`, pid); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "http://localhost:8080/v1/rooms/"+room.ID+"/view", nil)
	private, e := s.snapshotUncached(r, room.ID, pid)
	if e != nil {
		t.Fatal(e)
	}
	if wireInt64(private["control_epoch"]) != 7 || private["participant_id"] != pid {
		t.Fatalf("waiting snapshot lost authority: %+v", private)
	}
	public, e := s.snapshotUncached(r, room.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, leaked := public["control_epoch"]; leaked {
		t.Fatal("public waiting room leaked controller metadata")
	}
	if _, e = s.pool.Exec(ctx, `UPDATE platform_seats SET active=false WHERE participant_id=$1`, pid); e != nil {
		t.Fatal(e)
	}
	departed, e := s.snapshotUncached(r, room.ID, pid)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := departed["participant_id"]; ok {
		t.Fatal("departed waiting-room participant was assigned a seat")
	}
}

type controlTestMailer struct{}

func (controlTestMailer) Send(context.Context, string, string, string) error { return nil }
func controlTestCookie(t *testing.T, s *Service, uid string) *http.Cookie {
	t.Helper()
	secret := strings.Repeat("z", 42) + "Q"
	sum := sha256.Sum256([]byte(secret))
	_, e := s.pool.Exec(context.Background(), `INSERT INTO auth_sessions(id,user_id,secret_hash,csrf_token,expires_at) VALUES($1,$2,$3,'csrf-test',now()+interval '1 hour')`, id("session"), uid, hex.EncodeToString(sum[:]))
	if e != nil {
		t.Fatal(e)
	}
	return &http.Cookie{Name: "omj_session", Value: secret}
}
func TestPGStrangerCannotLeaveWaitingRoomAndEndedSeatsSurvive(t *testing.T) {
	s := recoveryService(t, mcr.New())
	ctx := context.Background()
	owner := auth.User{ID: id("user"), Name: "Owner", Verified: true, Status: "active"}
	stranger := auth.User{ID: id("user"), Name: "Stranger", Verified: true, Status: "active"}
	for _, u := range []auth.User{owner, stranger} {
		if _, e := s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test',true)`, u.ID, u.ID+"@example.invalid", u.Name); e != nil {
			t.Fatal(e)
		}
	}
	room, _, e := s.create(ctx, owner, createRequest{Mode: "human_only", Format: "practice_1"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE platform_seats SET ready=true WHERE room_id=$1`, room.ID); e != nil {
		t.Fatal(e)
	}
	svc, e := auth.New(s.pool, auth.Config{BaseURL: "http://localhost:8080", MailEncryptionKey: make([]byte, 32), Mailer: controlTestMailer{}})
	if e != nil {
		t.Fatal(e)
	}
	s.SetAuth(svc)
	cookie := controlTestCookie(t, s, stranger.ID)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://localhost:8080/v1/rooms/"+room.ID+"/leave", strings.NewReader(`{}`))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://localhost:8080")
		req.Header.Set("X-CSRF-Token", "csrf-test")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(); rec.Code != 403 {
		t.Fatalf("stranger leave got %d %s", rec.Code, rec.Body.String())
	}
	var ready bool
	if e = s.pool.QueryRow(ctx, `SELECT ready FROM platform_seats WHERE room_id=$1`, room.ID).Scan(&ready); e != nil || !ready {
		t.Fatalf("stranger reset readiness: %v", e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE platform_rooms SET status='ended' WHERE id=$1`, room.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE auth_sessions SET user_id=$1 WHERE user_id=$2`, owner.ID, stranger.ID); e != nil {
		t.Fatal(e)
	}
	if rec := request(); rec.Code != 200 {
		t.Fatalf("terminal leave got %d", rec.Code)
	}
	var count int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_seats WHERE room_id=$1 AND user_id=$2`, room.ID, owner.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("terminal history seat erased: %v", e)
	}
}
