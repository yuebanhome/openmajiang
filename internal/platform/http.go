package platform

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/yuebanhome/openmajiang/internal/auth"
)

func (s *Service) RegisterRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/public/rooms", s.listRooms)
	m.HandleFunc("GET /v1/public/rooms/{id}", s.getRoom)
	m.HandleFunc("GET /v1/rooms/{id}", s.getRoom)
	m.HandleFunc("POST /v1/rooms", s.mutation(s.createRoom))
	m.HandleFunc("POST /v1/practice", s.mutation(s.practice))
	m.HandleFunc("POST /v1/rooms/join", s.mutation(s.joinCode))
	m.HandleFunc("POST /v1/rooms/{id}/join", s.mutation(s.joinRoom))
	m.HandleFunc("POST /v1/rooms/{id}/ready", s.mutation(s.readyRoom))
	m.HandleFunc("POST /v1/rooms/{id}/bots", s.mutation(s.addBot))
	m.HandleFunc("POST /v1/rooms/{id}/start", s.mutation(s.startRoom))
	m.HandleFunc("POST /v1/rooms/{id}/leave", s.mutation(s.leaveRoom))
	m.HandleFunc("POST /v1/rooms/{id}/actions", s.mutation(s.actionHTTP))
	m.HandleFunc("GET /v1/rooms/{id}/view", s.playerView)
	m.HandleFunc("POST /v1/rooms/{id}/take-control", s.mutation(s.takeControl))
	m.HandleFunc("GET /v1/public/rooms/{id}/spectator", s.spectatorView)
	m.HandleFunc("POST /v1/public/rooms/{id}/spectator-ticket", s.spectatorTicket)
	m.HandleFunc("GET /v1/ws/players", s.playerWS)
	m.HandleFunc("GET /v1/ws/spectators", s.spectatorWS)
	m.HandleFunc("GET /v1/ws/bots", s.botWS)
	m.HandleFunc("GET /v1/rulesets", s.rules)
	m.HandleFunc("GET /v1/public/rules", s.rules)
	m.HandleFunc("GET /v1/rulesets/{id}/versions/{version}", s.ruleInfo)
	m.HandleFunc("GET /v1/me/active-match", s.activeMatch)
	m.HandleFunc("GET /v1/public/matches", s.matches)
	m.HandleFunc("GET /v1/public/matches/{id}", s.matchInfo)
	m.HandleFunc("GET /v1/public/matches/{id}/snapshot", s.matchSnapshot)
	m.HandleFunc("POST /v1/public/matches/{id}/spectator-tickets", s.matchTicket)
	m.HandleFunc("GET /v1/public/matches/{id}/replay", s.replay)
	m.HandleFunc("GET /v1/me/matches/{id}/replay", s.privateReplay)
	m.HandleFunc("GET /v1/public/hands/{id}/replay", s.handReplay)
	m.HandleFunc("GET /v1/me/hands/{id}/replay", s.privateHandReplay)
	m.HandleFunc("GET /v1/me/matches/{id}", s.privateMatch)
	m.HandleFunc("GET /v1/me/matches", s.myMatches)
	s.botRoutes(m)
	s.queueRoutes(m)
	s.adminRoutes(m)
}
func (s *Service) listRooms(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT id FROM platform_rooms ORDER BY CASE status WHEN 'playing' THEN 0 WHEN 'waiting' THEN 1 ELSE 2 END,created_at DESC LIMIT 100`)
	if e != nil {
		failure(w, e)
		return
	}
	var ids []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			ids = append(ids, v)
		}
	}
	rows.Close()
	result := []Room{}
	for _, v := range ids {
		room, e := loadRoom(r.Context(), s.pool, v)
		if e == nil {
			result = append(result, room)
		}
	}
	write(w, 200, map[string]any{"rooms": result})
}
func (s *Service) getRoom(w http.ResponseWriter, r *http.Request) {
	v, e := loadRoom(r.Context(), s.pool, r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	result := map[string]any{"room": v}
	if u, e := s.user(r, false); e == nil {
		for _, p := range v.Seats {
			if p.UserID == u.ID && p.Kind == "human" {
				result["own_participant_id"] = p.ParticipantID
			}
		}
	}
	write(w, 200, result)
}

type createRequest struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	RuleID      string `json:"ruleset_id"`
	RuleVersion string `json:"ruleset_version"`
	Format      string `json:"match_format"`
	Profile     string `json:"online_profile"`
	Capacity    int    `json:"seat_count"`
	InviteOnly  bool   `json:"invite_only"`
	SelfTest    bool   `json:"self_test"`
}

func (s *Service) create(ctx context.Context, u auth.User, c createRequest) (Room, string, error) {
	if c.Name == "" {
		c.Name = u.Name + "的牌桌"
	}
	if len([]rune(c.Name)) > 50 {
		return Room{}, "", api(400, "NAME_TOO_LONG")
	}
	if c.Mode == "" {
		c.Mode = "human_only"
	}
	if c.Mode != "human_only" && c.Mode != "mixed" && c.Mode != "bot_only" {
		return Room{}, "", api(400, "INVALID_MODE")
	}
	if c.RuleID == "" {
		c.RuleID = "openmajiang.mcr"
	}
	if c.RuleVersion == "" {
		c.RuleVersion = "1.0.0"
	}
	if c.Format == "" {
		c.Format = "standard_16"
	}
	rule, e := s.rule(c.RuleID, c.RuleVersion)
	if e != nil {
		return Room{}, "", e
	}
	mf := rule.Manifest()
	if len(mf.SeatCounts) == 0 {
		return Room{}, "", api(400, "INVALID_RULESET")
	}
	supported := false
	for _, f := range mf.Formats {
		if f == c.Format {
			supported = true
		}
	}
	if !supported {
		return Room{}, "", api(400, "INVALID_FORMAT")
	}
	if c.SelfTest && !c.InviteOnly {
		return Room{}, "", api(400, "SELF_TEST_REQUIRES_INVITE")
	}
	if c.Profile == "" && len(mf.OnlineProfiles) > 0 {
		c.Profile = mf.OnlineProfiles[0]
	}
	profileOK := len(mf.OnlineProfiles) == 0 && c.Profile == ""
	for _, p := range mf.OnlineProfiles {
		if p == c.Profile {
			profileOK = true
		}
	}
	if !profileOK {
		return Room{}, "", api(400, "INVALID_PROFILE")
	}
	if c.Capacity == 0 {
		c.Capacity = mf.SeatCounts[0]
	}
	capacityOK := false
	for _, n := range mf.SeatCounts {
		if n == c.Capacity {
			capacityOK = true
		}
	}
	if !capacityOK {
		return Room{}, "", api(400, "INVALID_SEAT_COUNT")
	}
	r := Room{ID: id("room"), Name: c.Name, OwnerID: u.ID, Mode: c.Mode, RulesetID: c.RuleID, RulesetVersion: c.RuleVersion, Format: c.Format, Capacity: c.Capacity, Profile: c.Profile, InviteOnly: c.InviteOnly, SelfTest: c.SelfTest, Status: "waiting"}
	invite := ""
	hash := ""
	if c.InviteOnly {
		invite = id("invite")
		hash = s.hash(invite)
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return r, "", e
	}
	defer tx.Rollback(ctx)
	if e = lockEligible(ctx, tx, u.ID); e != nil {
		return r, "", e
	}
	var busy bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active AND kind<>'builtin') OR EXISTS(SELECT 1 FROM platform_queue WHERE user_id=$1)`, u.ID).Scan(&busy); e != nil {
		return r, "", e
	}
	if busy {
		return r, "", api(409, "OWNER_ALREADY_SEATED_OR_QUEUED")
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_rooms(id,name,owner_id,mode,ruleset_id,ruleset_version,match_format,capacity,invite_hash,self_test,online_profile) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, r.ID, r.Name, u.ID, r.Mode, r.RulesetID, r.RulesetVersion, r.Format, r.Capacity, hash, r.SelfTest, r.Profile)
	if e != nil {
		return r, "", e
	}
	if c.Mode != "bot_only" {
		_, e = tx.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order) VALUES($1,$2,$3,$4,'human',0)`, id("participant"), r.ID, u.ID, u.Name)
		if e != nil {
			return r, "", api(409, "ALREADY_SEATED")
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return r, "", e
	}
	r, e = loadRoom(ctx, s.pool, r.ID)
	return r, invite, e
}
func (s *Service) createRoom(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var c createRequest
	if e = decode(w, r, &c); e != nil {
		failure(w, e)
		return
	}
	v, invite, e := s.create(r.Context(), u, c)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"room": v, "invite_code": invite})
}
func (s *Service) practice(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	v, _, e := s.create(r.Context(), u, createRequest{Mode: "mixed", Name: "国标练习", Format: "practice_1"})
	if e != nil {
		failure(w, e)
		return
	}
	for i := 1; i < v.Capacity; i++ {
		if e = s.addBotToRoom(r.Context(), v.ID, u.ID, "", "random_legal"); e != nil {
			failure(w, e)
			return
		}
	}
	_, e = s.pool.Exec(r.Context(), `UPDATE platform_seats SET ready=true,connected_until=now()+interval '30 seconds' WHERE room_id=$1`, v.ID)
	if e == nil {
		_, e = s.start(r.Context(), v.ID, u.ID)
	}
	if e != nil {
		failure(w, e)
		return
	}
	v, e = loadRoom(r.Context(), s.pool, v.ID)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"room": v})
}
func (s *Service) join(ctx context.Context, rid, invite string, u auth.User) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockEligible(ctx, tx, u.ID); e != nil {
		return e
	}
	var busy bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active AND kind<>'builtin')`, u.ID).Scan(&busy); e != nil {
		return e
	}
	if busy {
		return api(409, "ALREADY_SEATED")
	}
	var hash, status, mode string
	var capacity int
	e = tx.QueryRow(ctx, `SELECT invite_hash,status,mode,capacity FROM platform_rooms WHERE id=$1 FOR UPDATE`, rid).Scan(&hash, &status, &mode, &capacity)
	if e != nil {
		return api(404, "ROOM_NOT_FOUND")
	}
	if hash != "" && s.hash(invite) != hash {
		return api(403, "INVALID_INVITE")
	}
	if status != "waiting" || mode == "bot_only" {
		return api(409, "ROOM_NOT_JOINABLE")
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM platform_seats WHERE room_id=$1 AND active`, rid).Scan(&count)
	if e != nil {
		return e
	}
	if count >= capacity {
		return api(409, "TABLE_FULL")
	}
	var order int
	e = tx.QueryRow(ctx, `SELECT n FROM generate_series(0,$2-1) n WHERE NOT EXISTS(SELECT 1 FROM platform_seats WHERE room_id=$1 AND seat_order=n) ORDER BY n LIMIT 1`, rid, capacity).Scan(&order)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order) VALUES($1,$2,$3,$4,'human',$5)`, id("participant"), rid, u.ID, u.Name, order)
	if e != nil {
		return api(409, "ALREADY_SEATED")
	}
	_, _ = tx.Exec(ctx, `DELETE FROM platform_queue WHERE user_id=$1 AND bot_id=''`, u.ID)
	return tx.Commit(ctx)
}
func (s *Service) joinRoom(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var b struct {
		Invite string `json:"invite_code"`
	}
	if e = decode(w, r, &b); e == nil {
		e = s.join(r.Context(), r.PathValue("id"), b.Invite, u)
	}
	if e != nil {
		failure(w, e)
		return
	}
	s.getRoom(w, r)
}
func (s *Service) joinCode(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var b struct {
		Invite string `json:"invite_code"`
	}
	if e = decode(w, r, &b); e != nil {
		failure(w, e)
		return
	}
	var rid string
	e = s.pool.QueryRow(r.Context(), `SELECT id FROM platform_rooms WHERE invite_hash=$1 AND status='waiting'`, s.hash(b.Invite)).Scan(&rid)
	if e != nil {
		failure(w, api(404, "INVITE_NOT_FOUND"))
		return
	}
	if e = s.join(r.Context(), rid, b.Invite, u); e != nil {
		failure(w, e)
		return
	}
	r.SetPathValue("id", rid)
	s.getRoom(w, r)
}
func (s *Service) readyRoom(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	var b struct {
		Ready bool `json:"ready"`
	}
	if e = decode(w, r, &b); e != nil {
		failure(w, e)
		return
	}
	res, e := s.pool.Exec(r.Context(), `UPDATE platform_seats SET ready=$3,connected_until=now()+interval '30 seconds' WHERE room_id=$1 AND user_id=$2 AND kind='human' AND active AND EXISTS(SELECT 1 FROM platform_rooms WHERE id=$1 AND status='waiting')`, r.PathValue("id"), u.ID, b.Ready)
	if e != nil {
		failure(w, e)
		return
	}
	if res.RowsAffected() == 0 {
		failure(w, api(403, "FORBIDDEN_SEAT"))
		return
	}
	s.tryAutoStart(r.Context(), r.PathValue("id"))
	s.getRoom(w, r)
}
func (s *Service) startRoom(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e == nil {
		_, e = s.start(r.Context(), r.PathValue("id"), u.ID)
	}
	if e != nil {
		failure(w, e)
		return
	}
	s.getRoom(w, r)
}
func (s *Service) leaveRoom(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		failure(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	e = tx.QueryRow(r.Context(), `SELECT status FROM platform_rooms WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&status)
	if e != nil {
		failure(w, e)
		return
	}
	if status == "playing" {
		_, e = tx.Exec(r.Context(), `UPDATE platform_seats SET leave_after_hand=true WHERE room_id=$1 AND user_id=$2 AND kind='human'`, r.PathValue("id"), u.ID)
	} else {
		_, e = tx.Exec(r.Context(), `DELETE FROM platform_seats WHERE room_id=$1 AND user_id=$2 AND kind='human'`, r.PathValue("id"), u.ID)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"ok": true, "leave_after_hand": status == "playing"})
}
func (s *Service) rules(w http.ResponseWriter, r *http.Request) {
	result := []any{}
	for _, v := range s.cfg.Rules {
		result = append(result, v.Manifest())
	}
	write(w, 200, map[string]any{"rulesets": result})
}
func (s *Service) ruleInfo(w http.ResponseWriter, r *http.Request) {
	v, e := s.rule(r.PathValue("id"), r.PathValue("version"))
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"ruleset": v.Manifest()})
}
func (s *Service) participant(ctx context.Context, roomID, userID, botID string) (Seat, error) {
	var v Seat
	q := `SELECT participant_id,user_id,bot_id,name,kind,seat_order,ready,control_epoch FROM platform_seats WHERE room_id=$1 AND user_id=$2`
	if botID != "" {
		q += ` AND bot_id=$3`
	} else {
		q += ` AND kind='human'`
	}
	args := []any{roomID, userID}
	if botID != "" {
		args = append(args, botID)
	}
	e := s.pool.QueryRow(ctx, q, args...).Scan(&v.ParticipantID, &v.UserID, &v.BotID, &v.Name, &v.Kind, &v.Order, &v.Ready, &v.Epoch)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, api(403, "FORBIDDEN_SEAT")
	}
	return v, e
}
func (s *Service) BeforeDelete(ctx context.Context, userID string) error {
	var active bool
	e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_seats WHERE user_id=$1 AND active)`, userID).Scan(&active)
	if e != nil {
		return e
	}
	if active {
		return errors.New("请先结束或退出当前牌桌")
	}
	return nil
}
func (s *Service) OnDelete(userID string) {
	ctx := context.Background()
	_, _ = s.pool.Exec(ctx, `UPDATE platform_seats SET name='已注销用户' WHERE user_id=$1`, userID)
	_, _ = s.pool.Exec(ctx, `UPDATE platform_bots SET enabled=false,name='已注销 Bot' WHERE owner_id=$1`, userID)
	_, _ = s.pool.Exec(ctx, `DELETE FROM platform_queue WHERE user_id=$1`, userID)
}
func (s *Service) OnRevoke(userID, sessionID string, all bool) {
	ctx := context.Background()
	q := `UPDATE platform_seats SET control_epoch=control_epoch+1,controller='',connected_until=NULL WHERE user_id=$1 AND kind='human'`
	args := []any{userID}
	if !all {
		q += ` AND controller=$2`
		args = append(args, sessionID)
	}
	_, _ = s.pool.Exec(ctx, q, args...)
}

var _ = strings.TrimSpace
