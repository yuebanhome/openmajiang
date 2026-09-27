package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/internal/database"
	"github.com/yuebanhome/openmajiang/internal/testplugins/threeplayer"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func recoveryService(t *testing.T, rule rulesdk.Rule) *Service {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for real PostgreSQL platform concurrency/recovery tests")
	}
	ctx := context.Background()
	admin, err := database.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(id("test")))
	schema := "platform_test_" + hex.EncodeToString(hash[:8])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := database.ParseConfig(dsn)
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
	manifest := rule.Manifest()
	s, err := New(pool, Config{Rules: map[string]rulesdk.Rule{manifest.ID + "@" + manifest.Version: rule}, BaseURL: "http://localhost:8080", TokenHashKey: bytes.Repeat([]byte{0x51}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func recoveryRoom(t *testing.T, s *Service, rule rulesdk.Rule, format string) (Room, string) {
	return recoveryRoomKinds(t, s, rule, format, 0)
}
func recoveryRoomKinds(t *testing.T, s *Service, rule rulesdk.Rule, format string, builtinSeats int) (Room, string) {
	t.Helper()
	ctx := context.Background()
	manifest := rule.Manifest()
	owner := auth.User{ID: id("user"), Name: "Owner", Verified: true, Status: "active"}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test-only',true)`, owner.ID, owner.ID+"@example.invalid", owner.Name); err != nil {
		t.Fatal(err)
	}
	mode := "human_only"
	if builtinSeats > 0 {
		mode = "mixed"
	}
	room, _, err := s.create(ctx, owner, createRequest{Name: "Recovery integration", Mode: mode, RuleID: manifest.ID, RuleVersion: manifest.Version, Format: format, Capacity: manifest.SeatCounts[0], InviteOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for seat := 1; seat < room.Capacity; seat++ {
		uid, pid := id("user"), id("participant")
		if _, err = s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test-only',true)`, uid, uid+"@example.invalid", fmt.Sprintf("Player %d", seat)); err != nil {
			t.Fatal(err)
		}
		kind := "human"
		if seat >= room.Capacity-builtinSeats {
			kind = "builtin"
			uid = owner.ID
		}
		if _, err = s.pool.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order,builtin_strategy) VALUES($1,$2,$3,$4,$5,$6,'basic_heuristic')`, pid, room.ID, uid, fmt.Sprintf("Player %d", seat), kind, seat); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.pool.Exec(ctx, `UPDATE platform_seats SET ready=true,control_epoch=1,controller='ctrl-'||participant_id,connected_until=now()+interval '1 hour' WHERE room_id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	mid, err := s.start(ctx, room.ID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room, err = loadRoom(ctx, s.pool, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	return room, mid
}
func recoveryMatch(t *testing.T, s *Service, mid string) match {
	t.Helper()
	m, err := loadMatch(context.Background(), s.pool, mid, false)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func recoveryFlow(t *testing.T, rule rulesdk.Rule, m match) rulesdk.Flow {
	t.Helper()
	f, err := rule.Inspect(m.State)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func recoveryAction(t *testing.T, s *Service, m match, f rulesdk.Flow, d rulesdk.Decision, typ string) Action {
	t.Helper()
	option := ""
	for _, o := range d.Options {
		if typ == "" || o.Type == typ {
			option = o.ID
			break
		}
	}
	if option == "" {
		t.Fatalf("missing %s option", typ)
	}
	var epoch int64
	if err := s.pool.QueryRow(context.Background(), `SELECT control_epoch FROM platform_seats WHERE participant_id=$1`, d.ParticipantID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return Action{Type: "submit_action", Protocol: "1.0", MatchID: m.ID, HandID: handID(m.ID, f.HandIndex), ParticipantID: d.ParticipantID, Seat: d.Seat, Assignment: f.HandIndex, Epoch: epoch, DecisionID: d.ID, WindowID: f.WindowID, CommandID: id("command"), OptionID: option}
}
func recoverySubmit(t *testing.T, s *Service, a Action) json.RawMessage {
	t.Helper()
	response, err := s.Submit(context.Background(), a.ParticipantID, "ctrl-"+a.ParticipantID, a)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return response
}
func recoveryReaction(t *testing.T, s *Service, rule rulesdk.Rule, mid string) (match, rulesdk.Flow) {
	t.Helper()
	m := recoveryMatch(t, s, mid)
	f := recoveryFlow(t, rule, m)
	a := recoveryAction(t, s, m, f, f.Decisions[0], "discard")
	recoverySubmit(t, s, a)
	m = recoveryMatch(t, s, mid)
	f = recoveryFlow(t, rule, m)
	if f.WindowKind != "reaction" {
		t.Fatal("discard did not open response window")
	}
	return m, f
}
func recoveryCode(err error) string {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestPGCommitThenLostACKAndFirstValidResponse(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	m, f := recoveryReaction(t, s, rule, mid)
	a := recoveryAction(t, s, m, f, f.Decisions[0], "pass")
	first := recoverySubmit(t, s, a)
	var ack map[string]any
	_ = json.Unmarshal(first, &ack)
	if ack["status"] != "recorded" {
		t.Fatal("reaction ACK claimed to be applied")
	}
	second := a
	second.CommandID = id("command")
	if _, err := s.Submit(context.Background(), a.ParticipantID, "ctrl-"+a.ParticipantID, second); recoveryCode(err) != "ALREADY_SUBMITTED" {
		t.Fatalf("second valid response: %v", err)
	}
	// Simulate commit succeeding while its network ACK is lost, followed by a
	// control takeover. Identical retries still retrieve the committed outcome.
	if _, err := s.pool.Exec(context.Background(), `UPDATE platform_seats SET control_epoch=control_epoch+1,controller='new-controller' WHERE participant_id=$1`, a.ParticipantID); err != nil {
		t.Fatal(err)
	}
	retried, err := s.Submit(context.Background(), a.ParticipantID, "new-controller", a)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	_ = json.Unmarshal(first, &x)
	_ = json.Unmarshal(retried, &y)
	if string(jsonBytes(x)) != string(jsonBytes(y)) {
		t.Fatal("lost-ACK retry changed response")
	}
	conflict := a
	conflict.OptionID = "changed-payload"
	if _, err = s.Submit(context.Background(), a.ParticipantID, "new-controller", conflict); recoveryCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("payload conflict: %v", err)
	}
	var n int
	if err = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform_commands WHERE match_id=$1 AND participant_id=$2`, mid, a.ParticipantID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate committed command count=%d err=%v", n, err)
	}
	m = recoveryMatch(t, s, mid)
	if m.Seq != 2 || m.Choices[a.ParticipantID] != a.OptionID {
		t.Fatal("response registration advanced or lost state")
	}
}

func TestPGSubmitAndTimerSerializeAtDeadline(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	runCtx, stop := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); s.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-runDone })
	for iteration := 0; iteration < 12; iteration++ {
		_, mid := recoveryRoom(t, s, rule, "practice_1")
		m := recoveryMatch(t, s, mid)
		f := recoveryFlow(t, rule, m)
		a := recoveryAction(t, s, m, f, f.Decisions[0], "discard")
		if _, err := s.pool.Exec(context.Background(), `UPDATE platform_matches SET deadline_at=now()+interval '1 millisecond' WHERE id=$1`, mid); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var submitted error
		go func() {
			defer wg.Done()
			_, submitted = s.Submit(context.Background(), a.ParticipantID, "ctrl-"+a.ParticipantID, a)
		}()
		go func() { defer wg.Done(); <-time.After(2 * time.Millisecond); s.tickMatch(context.Background(), mid) }()
		wg.Wait()
		if submitted != nil && recoveryCode(submitted) != "DECISION_CLOSED" {
			t.Fatalf("deadline race: %v", submitted)
		}
		// A timer may skip the row while Submit holds it and then rejects an
		// expired command. The real 25ms scheduler must rediscover that work;
		// this observation loop must not perform the retry on its behalf.
		m = func() match {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			poll := time.NewTicker(5 * time.Millisecond)
			defer poll.Stop()
			for {
				current, err := loadMatch(ctx, s.pool, mid, false)
				if err != nil {
					t.Fatalf("observe deadline adjudication: %v", err)
				}
				if current.Seq != 1 {
					return current
				}
				select {
				case <-ctx.Done():
					t.Fatal("scheduler did not rediscover the skipped deadline")
				case <-poll.C:
				}
			}
		}()
		if m.Seq != 2 {
			t.Fatalf("window transitioned %d times", m.Seq-1)
		}
		var events int
		if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform_events WHERE match_id=$1`, mid).Scan(&events); err != nil || events != 2 {
			t.Fatalf("non-atomic state/event update events=%d err=%v", events, err)
		}
	}
}

func TestPGRecoveryPreservesIntentAndFencesOldOwner(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	m, f := recoveryReaction(t, s, rule, mid)
	a := recoveryAction(t, s, m, f, f.Decisions[0], "pass")
	recoverySubmit(t, s, a)
	m = recoveryMatch(t, s, mid)
	expected, err := rule.Apply(m.State, rulesdk.Input{Type: "resolve", Choices: m.Choices})
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(s.pool, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(context.Background(), `UPDATE platform_matches SET owner_until=now()-interval '1 second',updated_at=now()-interval '2 seconds' WHERE id=$1`, mid); err != nil {
		t.Fatal(err)
	}
	other.tickMatch(context.Background(), mid)
	after := recoveryMatch(t, s, mid)
	if after.Owner != other.id || after.OwnerEpoch != m.OwnerEpoch+1 || !after.Interrupted || after.Seq != m.Seq+1 {
		t.Fatalf("recovery fence fields: %+v", after)
	}
	var actualState, expectedState any
	_ = json.Unmarshal(after.State, &actualState)
	_ = json.Unmarshal(expected.State, &expectedState)
	if string(jsonBytes(actualState)) != string(jsonBytes(expectedState)) {
		t.Fatal("recovery lost or changed recorded response")
	}
	f = recoveryFlow(t, rule, after)
	next := recoveryAction(t, s, after, f, f.Decisions[0], "discard")
	if _, err = s.Submit(context.Background(), next.ParticipantID, "ctrl-"+next.ParticipantID, next); recoveryCode(err) != "TABLE_RECOVERING" {
		t.Fatalf("old owner accepted command: %v", err)
	}
	s.tickMatch(context.Background(), mid)
	unchanged := recoveryMatch(t, s, mid)
	if unchanged.Seq != after.Seq || unchanged.OwnerEpoch != after.OwnerEpoch {
		t.Fatal("old timer mutated recovered match")
	}
	var choicesEmpty bool
	if err = s.pool.QueryRow(context.Background(), `SELECT choices='{}'::jsonb FROM platform_matches WHERE id=$1`, mid).Scan(&choicesEmpty); err != nil || !choicesEmpty {
		t.Fatalf("old choices carried into next window: %v", err)
	}
}

func TestPGUnavailableArtifactAndLongOutageAbort(t *testing.T) {
	for _, reason := range []string{"artifact", "outage"} {
		t.Run(reason, func(t *testing.T) {
			rule := mcr.New()
			s := recoveryService(t, rule)
			room, mid := recoveryRoom(t, s, rule, "practice_1")
			other, err := New(s.pool, s.cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := "missing_ruleset"
			if reason == "artifact" {
				_, err = s.pool.Exec(context.Background(), `UPDATE platform_matches SET artifact_hash='unavailable-build' WHERE id=$1`, mid)
				other = s
			} else {
				want = "aborted_by_server"
				_, err = s.pool.Exec(context.Background(), `UPDATE platform_matches SET owner_until=now()-interval '1 second',updated_at=now()-interval '61 seconds' WHERE id=$1`, mid)
			}
			if err != nil {
				t.Fatal(err)
			}
			other.tickMatch(context.Background(), mid)
			m := recoveryMatch(t, s, mid)
			if m.Status != want || m.Deadline != nil || m.Seq != 1 {
				t.Fatalf("invalid abort result status=%s seq=%d", m.Status, m.Seq)
			}
			var active int
			if err = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform_seats WHERE room_id=$1 AND active`, room.ID).Scan(&active); err != nil || active != 0 {
				t.Fatal("abort retained active occupation")
			}
		})
	}
}

func recoveryAdvance(t *testing.T, s *Service, mid string, rule rulesdk.Rule, in rulesdk.Input) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	m, err := loadMatch(ctx, tx, mid, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.advance(ctx, tx, &m, rule, in); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPGThreePlayerTwoWinnersAndRepeatDealerUseSameHost(t *testing.T) {
	rule := threeplayer.New()
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "two_hands")
	for hand := 1; hand <= 2; hand++ {
		m := recoveryMatch(t, s, mid)
		f := recoveryFlow(t, rule, m)
		if len(f.Assignments) != 3 || f.HandIndex != hand || f.Decisions[0].Seat != 0 {
			t.Fatal("Host assumed four players or rotated repeat dealer")
		}
		a := recoveryAction(t, s, m, f, f.Decisions[0], "")
		recoverySubmit(t, s, a)
		m = recoveryMatch(t, s, mid)
		f = recoveryFlow(t, rule, m)
		want := []int{5 * hand, 5 * hand, -10 * hand}
		for i, sc := range f.Scores {
			if sc.Total != want[i] {
				t.Fatal("Host rewrote multi-winner payment")
			}
		}
		if hand == 1 {
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "next_hand"})
		} else if !f.MatchEnded || m.Status != "completed" {
			t.Fatal("Host failed plugin-defined end")
		}
	}
}

func recoveryAssertPublic(t *testing.T, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid public JSON: %v", err)
	}
	var walk func(any, string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				switch key {
				case "tile_id", "hand", "melds", "flowers", "winning_hand", "winning_tile", "fan_items", "seed", "wall", "legal_actions", "drawn_tile_id", "private_marker":
					t.Fatalf("private %s leaked at %s", key, path)
				case "kind":
					if !strings.Contains(path, ".discards[") && !strings.Contains(path, ".room.seats[") {
						t.Fatalf("non-discard card kind at %s", path)
					}
				}
				walk(value, path+"."+key)
			}
		case []any:
			for i, value := range x {
				walk(value, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(value, "$")
}

func TestPGAnonymousSnapshotReplayAndWebSocketDiscardOnly(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	room, mid := recoveryRoom(t, s, rule, "practice_1")
	recoveryReaction(t, s, rule, mid)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	paths := []string{"/v1/public/rooms/" + room.ID + "/spectator", "/v1/public/matches/" + mid, "/v1/public/matches/" + mid + "/snapshot", "/v1/public/matches/" + mid + "/replay", "/v1/public/hands/" + handID(mid, 1) + "/replay"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path+"?seat_id=0&participant_id="+room.Seats[0].ParticipantID, nil)
		req.AddCookie(&http.Cookie{Name: "om_session", Value: "cannot-upgrade-public-view"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("public path %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		recoveryAssertPublic(t, rec.Body.Bytes())
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	res, err := http.Post(server.URL+"/v1/public/rooms/"+room.ID+"/spectator-ticket", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err = json.NewDecoder(res.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if ticket.Ticket == "" {
		t.Fatal("anonymous watcher could not get ticket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws/spectators?room_id="+room.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err = ws.Write(ctx, websocket.MessageText, jsonBytes(map[string]string{"type": "authenticate", "ticket": ticket.Ticket})); err != nil {
		t.Fatal(err)
	}
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAssertPublic(t, raw)
	if err = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"submit_action","option_id":"forged"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err = ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recoveryAssertPublic(t, raw)
		if bytes.Contains(raw, []byte("READ_ONLY")) {
			break
		}
	}
}

func TestPGStandardSixteenHandsRotateIdentityAndRejectOldSeat(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	room, mid := recoveryRoom(t, s, rule, "standard_16")
	initial := recoveryMatch(t, s, mid)
	initialFlow := recoveryFlow(t, rule, initial)
	old := recoveryAction(t, s, initial, initialFlow, initialFlow.Decisions[0], "discard")
	initialSeats := map[string]int{}
	for _, a := range initialFlow.Assignments {
		initialSeats[a.ParticipantID] = a.Seat
	}
	checkedRotation := false
	for step := 0; step < 10000; step++ {
		m := recoveryMatch(t, s, mid)
		f := recoveryFlow(t, rule, m)
		if f.HandIndex == 5 && !checkedRotation {
			for _, a := range f.Assignments {
				if initialSeats[a.ParticipantID] == a.Seat {
					t.Fatal("south-circle identity did not move")
				}
			}
			if _, err := s.Submit(context.Background(), old.ParticipantID, "ctrl-"+old.ParticipantID, old); err == nil {
				t.Fatal("old hand seat authorization accepted after rotation")
			}
			req := httptest.NewRequest("GET", "/", nil)
			v, err := s.snapshot(req, room.ID, old.ParticipantID)
			if err != nil {
				t.Fatal(err)
			}
			if v["participant_id"] != old.ParticipantID || v["seat_assignment_version"] != 5 {
				t.Fatal("private restore bound to old physical seat")
			}
			checkedRotation = true
		}
		if f.MatchEnded {
			if f.HandIndex != 16 || !checkedRotation || m.Status != "completed" {
				t.Fatal("incomplete standard match")
			}
			var publicViews int
			if err := s.pool.QueryRow(context.Background(), `SELECT count(DISTINCT hand_index) FROM platform_views WHERE match_id=$1 AND participant_id=''`, mid).Scan(&publicViews); err != nil || publicViews != 16 {
				t.Fatal("public history lost hand boundaries")
			}
			return
		}
		in := rulesdk.Input{Type: "timeout"}
		if f.WindowKind == "reaction" {
			in.Type = "resolve"
		}
		if f.WindowKind == "intermission" {
			in.Type = "next_hand"
		}
		recoveryAdvance(t, s, mid, rule, in)
	}
	t.Fatal("sixteen-hand host integration failed to terminate")
}

type recoverySeededRule struct{ rulesdk.Rule }

// Unlike the four-human logical-clock integration above, this exercises the
// actual builtin strategy branch in tickMatch. Only expiry timestamps are
// advanced by the test; production clock durations and rule transitions remain
// unchanged. Human moves still pass through the authenticated-seat Submit path.
func TestPGOneHumanThreeBuiltinsCompleteSixteenHands(t *testing.T) {
	rule := mcr.New()
	s := recoveryService(t, rule)
	room, mid := recoveryRoomKinds(t, s, rule, "standard_16", 3)
	human := ""
	for _, seat := range room.Seats {
		if seat.Kind == "human" {
			human = seat.ParticipantID
		}
	}
	if human == "" {
		t.Fatal("mixed fixture has no human")
	}
	expire := func() {
		if _, err := s.pool.Exec(context.Background(), `UPDATE platform_matches SET deadline_at=now()-interval '1 millisecond' WHERE id=$1`, mid); err != nil {
			t.Fatal(err)
		}
		s.tickMatch(context.Background(), mid)
	}
	builtinActions := 0
	humanActions := 0
	for step := 0; step < 10000; step++ {
		m := recoveryMatch(t, s, mid)
		f := recoveryFlow(t, rule, m)
		if f.MatchEnded {
			if m.Status != "completed" || f.HandIndex != 16 || builtinActions == 0 || humanActions == 0 {
				t.Fatal("mixed standard match did not finish through both controllers")
			}
			var hands int
			if err := s.pool.QueryRow(context.Background(), `SELECT count(DISTINCT hand_index) FROM platform_views WHERE match_id=$1 AND participant_id=''`, mid).Scan(&hands); err != nil || hands != 16 {
				t.Fatal("mixed replay lacks sixteen hands")
			}
			t.Logf("mixed sixteen hands: human actions=%d builtin self actions=%d", humanActions, builtinActions)
			return
		}
		switch f.WindowKind {
		case "intermission":
			expire()
		case "self":
			d := f.Decisions[0]
			if d.ParticipantID == human {
				a := recoveryAction(t, s, m, f, d, "")
				a.OptionID = fallback(d, "self")
				recoverySubmit(t, s, a)
				humanActions++
			} else {
				s.tickMatch(context.Background(), mid)
				builtinActions++
			}
		case "reaction":
			for _, d := range f.Decisions {
				if d.ParticipantID == human {
					a := recoveryAction(t, s, m, f, d, "")
					a.OptionID = fallback(d, "reaction")
					recoverySubmit(t, s, a)
					humanActions++
				}
			}
			s.tickMatch(context.Background(), mid)
			expire()
		default:
			t.Fatalf("unexpected mixed phase %s", f.WindowKind)
		}
		next := recoveryMatch(t, s, mid)
		if next.Seq <= m.Seq {
			t.Fatalf("mixed Host made no progress at hand %d phase %s", f.HandIndex, f.WindowKind)
		}
	}
	t.Fatal("mixed standard match exceeded step safety bound")
}

func (r recoverySeededRule) Init(c rulesdk.Config, _ []byte) (rulesdk.Snapshot, error) {
	return r.Rule.Init(c, bytes.Repeat([]byte{7}, 32))
}

func TestPGTimeoutFlowerKeepsOriginalDeadline(t *testing.T) {
	rule := recoverySeededRule{mcr.New()}
	s := recoveryService(t, rule)
	_, mid := recoveryRoom(t, s, rule, "practice_1")
	for step := 0; step < 300; step++ {
		m := recoveryMatch(t, s, mid)
		f := recoveryFlow(t, rule, m)
		if f.HandEnded {
			t.Fatal("fixed regression wall ended before a flower")
		}
		if f.WindowKind == "self" {
			hasFlower := false
			for _, o := range f.Decisions[0].Options {
				if o.Type == "replace_flower" {
					hasFlower = true
					break
				}
			}
			if hasFlower {
				if _, err := s.pool.Exec(context.Background(), `UPDATE platform_matches SET deadline_at=now()-interval '1 second' WHERE id=$1`, mid); err != nil {
					t.Fatal(err)
				}
				before := recoveryMatch(t, s, mid)
				s.tickMatch(context.Background(), mid)
				after := recoveryMatch(t, s, mid)
				if after.Seq != before.Seq+1 {
					t.Fatal("timeout did not replace flower")
				}
				if before.Deadline == nil || after.Deadline == nil || !before.Deadline.Equal(*after.Deadline) {
					t.Fatal("timeout flower replacement extended original decision deadline")
				}
				return
			}
			a := recoveryAction(t, s, m, f, f.Decisions[0], "discard")
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "action", ParticipantID: a.ParticipantID, OptionID: a.OptionID})
		} else {
			recoveryAdvance(t, s, mid, rule, rulesdk.Input{Type: "resolve"})
		}
	}
	t.Fatal("fixed regression wall did not produce a flower")
}
