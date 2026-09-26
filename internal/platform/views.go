package platform

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

func (s *Service) snapshot(r *http.Request, roomID, pid string) (map[string]any, error) {
	if pid == "" {
		return s.cachedPublicSnapshot(r, roomID)
	}
	return s.snapshotUncached(r, roomID, pid)
}

func (s *Service) snapshotUncached(r *http.Request, roomID, pid string) (map[string]any, error) {
	room, e := loadRoom(r.Context(), s.pool, roomID)
	if e != nil {
		return nil, e
	}
	if room.MatchID == "" {
		return map[string]any{"type": "room_snapshot", "room": room, "view": nil}, nil
	}
	m, e := loadMatch(r.Context(), s.pool, room.MatchID, false)
	if e != nil {
		return nil, e
	}
	var view json.RawMessage
	var index int
	e = s.pool.QueryRow(r.Context(), `SELECT view,hand_index FROM platform_views WHERE match_id=$1 AND participant_id=$2 AND seq=$3`, m.ID, pid, m.Seq).Scan(&view, &index)
	if e != nil {
		return nil, e
	}
	deleted, e := s.deletedParticipants(r.Context(), m.ID)
	if e != nil {
		return nil, e
	}
	view, e = anonymizeView(view, deleted)
	if e != nil {
		return nil, e
	}
	result := map[string]any{"type": "snapshot", "room": room, "view": view, "match_id": m.ID, "hand_id": handID(m.ID, index), "hand_index": index, "seq": m.Seq, "status": m.Status, "deadline_at": m.Deadline, "platform_interrupted": m.Interrupted, "ruleset": map[string]string{"id": m.RulesetID, "version": m.RulesetVersion}, "match_format": m.Format}
	if pid == "" {
		result["type"] = "spectator_snapshot"
		result["view_policy"] = "spectator_discard_only@1"
		return result, nil
	}
	var epoch int64
	_ = s.pool.QueryRow(r.Context(), `SELECT control_epoch FROM platform_seats WHERE participant_id=$1`, pid).Scan(&epoch)
	var selfTimeouts, reactionTimeouts int
	_ = s.pool.QueryRow(r.Context(), `SELECT self_timeouts,reaction_timeouts FROM platform_seats WHERE participant_id=$1`, pid).Scan(&selfTimeouts, &reactionTimeouts)
	result["self_timeout_count"] = selfTimeouts
	result["reaction_timeout_count"] = reactionTimeouts
	result["participant_id"] = pid
	result["control_epoch"] = epoch
	result["seat_assignment_version"] = index
	rule, e := s.rule(m.RulesetID, m.RulesetVersion)
	if e != nil {
		return nil, e
	}
	flow, e := rule.Inspect(m.State)
	if e != nil {
		return nil, e
	}
	for _, a := range flow.Assignments {
		if a.ParticipantID == pid {
			result["seat_id"] = a.Seat
		}
	}
	for _, d := range flow.Decisions {
		if d.ParticipantID == pid {
			var recorded json.RawMessage
			e = s.pool.QueryRow(r.Context(), `SELECT response FROM platform_commands WHERE participant_id=$1 AND match_id=$2 AND decision_id=$3`, pid, m.ID, d.ID).Scan(&recorded)
			if e == nil {
				result["recorded"] = recorded
			} else if m.Status == "active" && m.Deadline != nil && time.Now().Before(*m.Deadline) {
				result["decision"] = map[string]any{"type": "decision_request", "protocol_version": "1.0", "match_id": m.ID, "hand_id": handID(m.ID, index), "participant_id": pid, "seat_id": d.Seat, "seat_assignment_version": index, "control_epoch": epoch, "decision_id": d.ID, "window_id": flow.WindowID, "phase": flow.Phase, "server_time": time.Now().UTC(), "deadline_at": m.Deadline, "legal_actions": d.Options, "ruleset": result["ruleset"], "match_format": m.Format}
			}
		}
	}
	return result, nil
}
func (s *Service) spectatorView(w http.ResponseWriter, r *http.Request) {
	v, e := s.snapshot(r, r.PathValue("id"), "")
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, v)
}
func (s *Service) playerView(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.participant(r.Context(), r.PathValue("id"), u.ID, "")
	if e != nil {
		failure(w, e)
		return
	}
	v, e := s.snapshot(r, r.PathValue("id"), p.ParticipantID)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, v)
}
func (s *Service) takeControl(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.participant(r.Context(), r.PathValue("id"), u.ID, "")
	if e != nil {
		failure(w, e)
		return
	}
	session, e := s.cfg.Auth.SessionID(r)
	if e != nil {
		failure(w, api(401, "AUTH_EXPIRED"))
		return
	}
	control, _, _, e := s.acquireControl(r.Context(), p, session, false, true)
	if e != nil {
		failure(w, e)
		return
	}
	v, e := s.snapshot(r, r.PathValue("id"), p.ParticipantID)
	if e != nil {
		failure(w, e)
		return
	}
	v["control_token"] = control
	v["control_status"] = "owner"
	write(w, 200, v)
}

func (s *Service) actionHTTP(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.participant(r.Context(), r.PathValue("id"), u.ID, "")
	if e != nil {
		failure(w, e)
		return
	}
	_, e = s.cfg.Auth.SessionID(r)
	if e != nil {
		failure(w, api(401, "AUTH_EXPIRED"))
		return
	}
	var a Action
	if e = decode(w, r, &a); e != nil {
		failure(w, e)
		return
	}
	response, e := s.Submit(r.Context(), p.ParticipantID, r.Header.Get("X-Control-Token"), a)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, response)
}
func (s *Service) activeMatch(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	var room string
	e = s.pool.QueryRow(r.Context(), `SELECT room_id FROM platform_seats WHERE user_id=$1 AND kind='human' AND active LIMIT 1`, u.ID).Scan(&room)
	if e != nil {
		write(w, 200, map[string]any{"room": nil, "match": nil})
		return
	}
	v, e := loadRoom(r.Context(), s.pool, room)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"room": v, "match_id": v.MatchID})
}
func (s *Service) matches(w http.ResponseWriter, r *http.Request) { s.listMatches(w, r, "") }
func (s *Service) myMatches(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	s.listMatches(w, r, u.ID)
}
func (s *Service) listMatches(w http.ResponseWriter, r *http.Request, uid string) {
	q := `SELECT m.id,m.room_id,m.status,m.ruleset_id,m.ruleset_version,m.match_format,m.interrupted,m.created_at FROM platform_matches m`
	args := []any{}
	if uid != "" {
		q += ` WHERE EXISTS(SELECT 1 FROM platform_seats s WHERE s.room_id=m.room_id AND s.user_id=$1)`
		args = append(args, uid)
	}
	q += ` ORDER BY m.created_at DESC LIMIT 100`
	rows, e := s.pool.Query(r.Context(), q, args...)
	if e != nil {
		failure(w, e)
		return
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		var id, rid, status, rule, version, format string
		var interrupted bool
		var created time.Time
		if e = rows.Scan(&id, &rid, &status, &rule, &version, &format, &interrupted, &created); e != nil {
			failure(w, e)
			return
		}
		result = append(result, map[string]any{"id": id, "room_id": rid, "status": status, "ruleset_id": rule, "ruleset_version": version, "match_format": format, "platform_interrupted": interrupted, "created_at": created})
	}
	write(w, 200, map[string]any{"matches": result})
}
func (s *Service) matchInfo(w http.ResponseWriter, r *http.Request) {
	m, e := loadMatch(r.Context(), s.pool, r.PathValue("id"), false)
	if e != nil {
		failure(w, e)
		return
	}
	v, e := s.snapshot(r, m.RoomID, "")
	if e != nil {
		failure(w, e)
		return
	}
	v["match"] = map[string]any{"id": m.ID, "room_id": m.RoomID, "status": m.Status, "match_format": m.Format}
	write(w, 200, v)
}
func (s *Service) matchSnapshot(w http.ResponseWriter, r *http.Request) { s.matchInfo(w, r) }
func (s *Service) privateMatch(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	m, e := loadMatch(r.Context(), s.pool, r.PathValue("id"), false)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.participant(r.Context(), m.RoomID, u.ID, r.URL.Query().Get("bot_id"))
	if e != nil {
		failure(w, e)
		return
	}
	v, e := s.snapshot(r, m.RoomID, p.ParticipantID)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, v)
}
func (s *Service) replay(w http.ResponseWriter, r *http.Request) { s.replayFor(w, r, "") }
func (s *Service) privateReplay(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, false)
	if e != nil {
		failure(w, e)
		return
	}
	m, e := loadMatch(r.Context(), s.pool, r.PathValue("id"), false)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.participant(r.Context(), m.RoomID, u.ID, r.URL.Query().Get("bot_id"))
	if e != nil {
		failure(w, e)
		return
	}
	s.replayFor(w, r, p.ParticipantID)
}
func (s *Service) replayFor(w http.ResponseWriter, r *http.Request, pid string) {
	if _, e := loadMatch(r.Context(), s.pool, r.PathValue("id"), false); e != nil {
		failure(w, e)
		return
	}
	deleted, err := s.deletedParticipants(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	hand, _ := strconv.Atoi(r.URL.Query().Get("hand_index"))
	q := `SELECT seq,hand_index,view,created_at FROM platform_views WHERE match_id=$1 AND participant_id=$2 AND seq>$3`
	args := []any{r.PathValue("id"), pid, after}
	if r.URL.Query().Has("hand_index") {
		q += ` AND hand_index=$4`
		args = append(args, hand)
	}
	q += ` ORDER BY seq LIMIT ` + strconvI(limit)
	rows, e := s.pool.Query(r.Context(), q, args...)
	if e != nil {
		failure(w, e)
		return
	}
	defer rows.Close()
	frames := []any{}
	last := after
	for rows.Next() {
		var seq int64
		var idx int
		var view json.RawMessage
		var at time.Time
		if e = rows.Scan(&seq, &idx, &view, &at); e != nil {
			failure(w, e)
			return
		}
		view, e = anonymizeView(view, deleted)
		if e != nil {
			failure(w, e)
			return
		}
		frames = append(frames, map[string]any{"seq": seq, "hand_index": idx, "hand_id": handID(r.PathValue("id"), idx), "view": view, "at": at})
		last = seq
	}
	if e = rows.Err(); e != nil {
		failure(w, e)
		return
	}
	write(w, 200, map[string]any{"frames": frames, "next_after": last, "view_policy": map[bool]string{true: "spectator_discard_only@1", false: "participant_private"}[pid == ""]})
}
func (s *Service) handReplay(w http.ResponseWriter, r *http.Request) {
	if !parseHandRequest(r) {
		failure(w, api(400, "INVALID_HAND_ID"))
		return
	}
	s.replay(w, r)
}
func (s *Service) privateHandReplay(w http.ResponseWriter, r *http.Request) {
	if !parseHandRequest(r) {
		failure(w, api(400, "INVALID_HAND_ID"))
		return
	}
	s.privateReplay(w, r)
}
func parseHandRequest(r *http.Request) bool {
	parts := strings.Split(r.PathValue("id"), "_hand_")
	if len(parts) != 2 {
		return false
	}
	if _, e := strconv.Atoi(parts[1]); e != nil {
		return false
	}
	r.SetPathValue("id", parts[0])
	q := r.URL.Query()
	q.Set("hand_index", parts[1])
	r.URL.RawQuery = q.Encode()
	return true
}

var _ rulesdk.Rule
