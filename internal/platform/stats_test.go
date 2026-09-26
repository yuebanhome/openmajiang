package platform

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func TestStatisticsMetricSamplesAndFilters(t *testing.T) {
	if got := metricRate(0, 0); got.Value != nil || got.Samples != 0 {
		t.Fatalf("empty samples fabricated a value: %+v", got)
	}
	if got := metricRate(1, 4); got.Value == nil || *got.Value != .25 || got.Samples != 4 {
		t.Fatalf("rate: %+v", got)
	}
	r := httptest.NewRequest("GET", "/?ruleset_id=x%27%3Bdrop&self_test=false&platform_interrupted=true&bot_version=v1&queue_pool=public_bot_queue", nil)
	where, args, err := statisticsFilter(r, "user_1", "bot_1")
	if err != nil || strings.Contains(where, "drop") || !strings.Contains(where, "m.interrupted=") || len(args) != 7 {
		t.Fatalf("allowlisted parameterized filters: %q %v %v", where, args, err)
	}
	_, _, err = statisticsFilter(httptest.NewRequest("GET", "/?self_test=perhaps", nil), "", "")
	if err == nil {
		t.Fatal("accepted invalid boolean")
	}
	where, args, err = statisticsFilter(httptest.NewRequest("GET", "/", nil), "user_1", "")
	if err != nil || !strings.Contains(where, "p.participant_kind=") || args[1] != "human" {
		t.Fatalf("my statistics include owned Bots: %q %v %v", where, args, err)
	}
	empty := []StatisticMatch{}
	cursor := ""
	encoded, err := json.Marshal(StatisticResponse{Groups: []StatisticGroup{}, Matches: &empty, NextBefore: &cursor})
	if err != nil || !strings.Contains(string(encoded), `"matches":[]`) || !strings.Contains(string(encoded), `"next_before":""`) {
		t.Fatalf("private empty collections must remain explicit: %s %v", encoded, err)
	}
}

// Run with TEST_DATABASE_URL against PostgreSQL. The fixture explicitly skips
// when absent; these queries are not claimed validated by mock SQL matching.
func TestStatisticsSettledFactsAndPermissions(t *testing.T) {
	s, authMux, pool := opsFixture(t)
	ctx := context.Background()
	if err := StatsMigrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := StatsMigrate(ctx, pool); err != nil {
		t.Fatal("repeat migration", err)
	}
	owner := opsAccount(t, authMux, pool, "statistics-owner@example.test", false)
	other := opsAccount(t, authMux, pool, "statistics-other@example.test", false)
	m := match{ID: "stats-match", RoomID: "stats-room", RulesetID: "test.rule", RulesetVersion: "1", Format: "standard_16"}
	pids := []string{"stats-p1", "stats-p2", "stats-p3", "stats-p4"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO platform_rooms(id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,online_profile,capacity,status,queue_deadline) VALUES($1,'stats',$2,'bot_only',$3,$4,$5,'test-profile',4,'playing',now())`, m.RoomID, owner.uid, m.RulesetID, m.RulesetVersion, m.Format)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform_bots(id,owner_id,name,current_version) VALUES('stats-bot',$1,'stats','version-1')`, owner.uid)
	if err != nil {
		t.Fatal(err)
	}
	for i, pid := range pids {
		uid := owner.uid
		bid := "stats-bot"
		if i > 0 {
			uid = "other-owner-" + strconvI(i)
			bid = "stats-bot-" + strconvI(i)
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,bot_id,name,kind,seat_order,bot_version) VALUES($1,$2,$3,$4,'stats','bot',$5,'version-1')`, pid, m.RoomID, uid, bid, i)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform_matches(id,room_id,ruleset_id,ruleset_version,match_format,artifact_hash,manifest,config,state,owner_id,owner_until) VALUES($1,$2,$3,$4,$5,'fixture','{}','{}','{}','fixture',now()+interval '1 hour')`, m.ID, m.RoomID, m.RulesetID, m.RulesetVersion, m.Format)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.freezeStatistics(ctx, tx, m); err != nil {
		t.Fatal(err)
	}
	if err = s.freezeStatistics(ctx, tx, m); err != nil {
		t.Fatal("idempotent freeze", err)
	}
	// Changes after freeze cannot rewrite cohorts or the strategy-version record.
	if _, err = tx.Exec(ctx, `UPDATE platform_rooms SET self_test=true,online_profile='changed' WHERE id=$1`, m.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_bots SET current_version='version-2' WHERE id='stats-bot'`); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 16; index++ {
		scores := []rulesdk.Score{}
		for i, pid := range pids {
			delta := 0
			if index == 1 {
				delta = []int{48, -32, -8, -8}[i]
			}
			scores = append(scores, rulesdk.Score{ParticipantID: pid, Delta: delta})
		}
		hand := &rulesdk.HandResult{HandIndex: index, Scores: scores}
		if index == 1 {
			fan := 16
			hand.Winners = []rulesdk.Winner{{ParticipantID: pids[0], NonFlowerPoints: &fan}}
			hand.DiscarderID = pids[1]
		}
		f := rulesdk.Flow{HandIndex: index, HandEnded: true, HandResult: hand}
		if index == 16 {
			f.MatchEnded = true
			f.Rankings = []rulesdk.Ranking{
				{ParticipantID: pids[0], Rank: 1, RawScore: 48, StandardPoints: &rulesdk.Rational{Numerator: 4, Denominator: 1}},
				{ParticipantID: pids[1], Rank: 4, RawScore: -32, StandardPoints: &rulesdk.Rational{Numerator: 0, Denominator: 1}},
				{ParticipantID: pids[2], Rank: 2, RawScore: -8, StandardPoints: &rulesdk.Rational{Numerator: 3, Denominator: 2}},
				{ParticipantID: pids[3], Rank: 2, RawScore: -8, StandardPoints: &rulesdk.Rational{Numerator: 3, Denominator: 2}},
			}
		}
		if err = s.persistStatistics(ctx, tx, m, f); err != nil {
			t.Fatal(err)
		}
		if err = s.persistStatistics(ctx, tx, m, f); err != nil {
			t.Fatal("duplicate hand facts", err)
		}
	}
	opened := time.Now().UTC().Add(-time.Second)
	for i, outcome := range []string{"accepted", "accepted", "timeout", "recovery"} {
		did := "decision-" + strconvI(i)
		f := rulesdk.Flow{Decisions: []rulesdk.Decision{{ID: did, ParticipantID: pids[0]}}}
		if err = s.persistStatistics(ctx, tx, m, f); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `UPDATE platform_stat_decisions SET opened_at=$3 WHERE match_id=$1 AND decision_id=$2`, m.ID, did, opened)
		if err != nil {
			t.Fatal(err)
		}
		at := opened.Add(time.Duration([]int{100, 300, 500, 600}[i]) * time.Millisecond)
		if err = s.recordDecision(ctx, tx, m, did, pids[0], outcome, at); err != nil {
			t.Fatal(err)
		}
		if err = s.recordDecision(ctx, tx, m, did, pids[0], "timeout", at.Add(time.Second)); err != nil {
			t.Fatal("duplicate resolution", err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_matches SET status='completed' WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"invalid-1", "invalid-1", "invalid-2"} {
		if err = s.recordInvalidAction(ctx, m, pids[0], command, "INVALID_OPTION"); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.recordInvalidAction(ctx, m, pids[0], "stale-1", "STALE_CONTROL"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.statisticsRoutes(mux)
	w := opsRequest(t, mux, "GET", "/v1/bots/stats-bot/statistics", nil, owner)
	opsStatus(t, w, 200)
	var response struct {
		Groups  []statisticGroup `json:"groups"`
		Matches []struct {
			Standard *rulesdk.Rational `json:"standard_points"`
		} `json:"matches"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Groups) != 1 || len(response.Matches) != 1 || response.Matches[0].Standard == nil || response.Matches[0].Standard.Numerator != 4 {
		t.Fatalf("result: %s", w.Body.String())
	}
	g := response.Groups[0]
	if g.Comparable || !g.Dimensions.TrusteeUsed || g.Dimensions.SelfTest || g.Dimensions.OnlineProfile != "test-profile" || g.Dimensions.BotVersion != "version-1" || g.Dimensions.QueuePool != "public_bot_queue" {
		t.Fatalf("frozen dimensions or trustee boundary: %+v", g)
	}
	for name, expected := range map[string]struct {
		value   float64
		samples int64
	}{
		"completed_hands": {16, 16}, "win_rate": {1.0 / 16, 16}, "self_draw_rate": {0, 16}, "discard_loss_rate": {0, 16},
		"average_net_points": {3, 16}, "average_nonflower_points": {16, 1}, "completed_matches": {1, 1}, "average_rank": {1, 1}, "average_raw_score": {48, 1}, "average_standard_points": {4, 1},
		"average_decision_ms": {200, 2}, "p95_decision_ms": {290, 2}, "illegal_action_rate": {.5, 4}, "timeout_rate": {.25, 4}, "trustee_rate": {.5, 4},
	} {
		got := g.Metrics[name]
		if got.Value == nil || math.Abs(*got.Value-expected.value) > .001 || got.Samples != expected.samples {
			t.Errorf("%s: %+v want %+v", name, got, expected)
		}
	}
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/bots/stats-bot/statistics", nil, other), 404)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/bots/stats-bot/statistics", nil, opsLogin{}), 401)
	w = opsRequest(t, mux, "GET", "/v1/me/statistics", nil, owner)
	opsStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"groups":[]`) {
		t.Fatal("owner's human metrics silently included Bots", w.Body.String())
	}
	w = opsRequest(t, mux, "GET", "/v1/public/statistics", nil, opsLogin{})
	opsStatus(t, w, 200)
	for _, secret := range []string{owner.uid, "stats-p1", "stats-bot", "matches", "hand", "tile", "seed"} {
		// Metric names legitimately contain 'hand'; exact private keys do not.
		if strings.Contains(w.Body.String(), `"`+secret+`"`) {
			t.Fatalf("public statistics expose %q: %s", secret, w.Body.String())
		}
	}
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/public/statistics?self_test=nope", nil, opsLogin{}), 400)
	w = opsRequest(t, mux, "GET", "/v1/public/profiles/stats-bot", nil, opsLogin{})
	opsStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"display_name":"stats"`) || strings.Contains(w.Body.String(), `"email"`) || strings.Contains(w.Body.String(), `"illegal_action_rate"`) || strings.Contains(w.Body.String(), `"matches"`) {
		t.Fatal("public profile leaked diagnostics or omitted identity", w.Body.String())
	}
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/public/profiles/unknown", nil, opsLogin{}), 404)
	// An interruption changes the cohort and removes standard-point samples;
	// already completed hands remain available instead of being erased.
	if _, err = pool.Exec(ctx, `UPDATE platform_matches SET interrupted=true,status='aborted_by_server' WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	w = opsRequest(t, mux, "GET", "/v1/bots/stats-bot/statistics?platform_interrupted=true", nil, owner)
	opsStatus(t, w, 200)
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Groups) != 1 {
		t.Fatal(w.Body.String(), err)
	}
	if response.Groups[0].Metrics["average_standard_points"].Value != nil || response.Groups[0].Metrics["completed_hands"].Samples != 16 {
		t.Fatal("interrupted standard points or lost hand history", w.Body.String())
	}
	// Group cursor roundtrips use jsonb's canonical textual key.
	cursor := url.QueryEscape(`{"ruleset_id":"zzzz"}`)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/public/statistics?group_after="+cursor, nil, opsLogin{}), 200)
}
