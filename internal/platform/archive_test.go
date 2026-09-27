package platform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedArchiveMatch(t *testing.T, pool *pgxpool.Pool, mid, status, owner string, old bool) {
	t.Helper()
	ctx := context.Background()
	room := mid + "-room"
	_, err := pool.Exec(ctx, `INSERT INTO platform_rooms(id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,online_profile,capacity,status,match_id) VALUES($1,'retention',$2,'mixed','fixture','1','practice_1','fixture',2,'ended',$3)`, room, owner, mid)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if old {
		at = time.Now().Add(-31 * 24 * time.Hour)
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_matches(id,room_id,ruleset_id,ruleset_version,match_format,artifact_hash,manifest,config,state,status,choices,owner_id,owner_until,updated_at) VALUES($1,$2,'fixture','1','practice_1','fixture','{}','{"private_config":"erase me"}','{"concealed":"erase me"}',$3,'{"secret":"erase me"}','fixture',now(),$4)`, mid, room, status, at)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order,active) VALUES($1,$2,$3,'测试用户','human',0,false)`, mid+"-p1", room, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_stat_matches(match_id,ruleset_id,ruleset_version,online_profile,match_format,mode,clock_profile,config_hash,self_test,queue_pool) VALUES($1,'fixture','1','fixture','practice_1','mixed','fixture','fixture',false,'manual_room')`, mid)
	if err != nil {
		t.Fatal(err)
	}
	for i, score := range []int{42, -42} {
		pid := mid + "-p" + strconvI(i+1)
		_, err = pool.Exec(ctx, `INSERT INTO platform_stat_participants(match_id,participant_id,user_id,bot_id,participant_kind,bot_version) VALUES($1,$2,$3,'','human','')`, mid, pid, owner)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO platform_stat_hands(match_id,participant_id,hand_index,net_points,won,self_draw,discard_loss) VALUES($1,$2,1,$3,$4,false,false)`, mid, pid, score, score > 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO platform_stat_rankings(match_id,participant_id,rank,raw_score) VALUES($1,$2,$3,$4)`, mid, pid, i+1, score)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_views(match_id,seq,hand_index,participant_id,view) VALUES($1,1,1,'','{"discards":[]}'),($1,1,1,$2,'{"concealed":"erase me"}')`, mid, mid+"-p1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_events(match_id,seq,events,input,owner_epoch) VALUES($1,1,'[{"private":"erase me"}]','{"option":"erase me"}',1)`, mid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO platform_commands(participant_id,match_id,command_id,payload_hash,decision_id,response) VALUES($2,$1,'old-command','fixture','old-decision','{"type":"command_ack"}')`, mid, mid+"-p1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRetentionPostgres(t *testing.T) {
	s, mux, pool := opsFixture(t)
	ctx := context.Background()
	if err := ArchiveMigrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	player := opsAccount(t, mux, pool, "retention@example.test", false)
	seedArchiveMatch(t, pool, "old-terminal", "completed", player.uid, true)
	seedArchiveMatch(t, pool, "recent-terminal", "completed", player.uid, false)
	seedArchiveMatch(t, pool, "old-active", "active", player.uid, true)
	s.publicMu.Lock()
	s.publicSnapshots = map[string]publicCacheEntry{"fixture": {body: []byte(`{"private":"erase me"}`), expires: time.Now().Add(time.Hour)}}
	s.publicMu.Unlock()
	n, err := s.PurgeArchives(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged=%d err=%v", n, err)
	}
	var cleared bool
	err = pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL AND state='{}' AND choices='{}' AND config='{}'
AND NOT EXISTS(SELECT 1 FROM platform_views WHERE match_id=$1)
AND NOT EXISTS(SELECT 1 FROM platform_events WHERE match_id=$1)
AND NOT EXISTS(SELECT 1 FROM platform_commands WHERE match_id=$1)
FROM platform_matches WHERE id=$1`, "old-terminal").Scan(&cleared)
	if err != nil || !cleared {
		t.Fatal("restricted retention data remained", err)
	}
	s.publicMu.Lock()
	cached := len(s.publicSnapshots)
	s.publicMu.Unlock()
	if cached != 0 {
		t.Fatal("projection cache retained expired data")
	}
	for _, mid := range []string{"old-active", "recent-terminal"} {
		var preserved bool
		err = pool.QueryRow(ctx, `SELECT archived_at IS NULL AND state<>'{}' AND EXISTS(SELECT 1 FROM platform_views WHERE match_id=$1) FROM platform_matches WHERE id=$1`, mid).Scan(&preserved)
		if err != nil || !preserved {
			t.Fatalf("unexpected purge of %s: %v", mid, err)
		}
	}
	raw, err := s.archivedSummary(ctx, "old-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "erase me") || strings.Contains(string(raw), "concealed") || strings.Contains(string(raw), "private_config") {
		t.Fatal("archive summary copied secret state")
	}
	var summary archiveSummary
	if err = json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.CompletedHands != 1 || len(summary.Scores) != 2 || summary.Scores[0].Total != 42 || summary.Scores[1].Total != -42 {
		t.Fatalf("settled summary lost: %s", raw)
	}
	var statCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM platform_stat_rankings WHERE match_id='old-terminal'`).Scan(&statCount); err != nil || statCount != 2 {
		t.Fatal("retention removed statistics", err)
	}
	if err = requireNotArchived(ctx, pool, "old-terminal"); err == nil {
		t.Fatal("archive access remained allowed")
	}
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/public/matches/old-terminal/replay", nil, opsLogin{}), 410)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/me/matches/old-terminal/replay", nil, player), 410)
	opsStatus(t, opsRequest(t, mux, "GET", "/v1/public/matches/old-terminal/snapshot", nil, opsLogin{}), 410)
	info := opsRequest(t, mux, "GET", "/v1/public/matches/old-terminal", nil, opsLogin{})
	opsStatus(t, info, 200)
	if !strings.Contains(info.Body.String(), "42") || strings.Contains(info.Body.String(), "erase me") {
		t.Fatal("archive summary unavailable or leaked state")
	}
	_, err = s.Submit(ctx, "old-terminal-p1", "stale-control", Action{CommandID: "old-command", MatchID: "old-terminal", ParticipantID: "old-terminal-p1"})
	if ae, ok := err.(*APIError); !ok || ae.Code != "MATCH_ARCHIVED" {
		t.Fatalf("archived retry must never execute: %v", err)
	}
	if n, err = s.PurgeArchives(ctx); err != nil || n != 0 {
		t.Fatalf("archive is not idempotent: %d %v", n, err)
	}
}
