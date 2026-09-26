package platform

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type ticketClaim struct {
	Room   string `json:"room_id"`
	Expiry int64  `json:"expires_at"`
	Nonce  string `json:"nonce"`
}

func (s *Service) spectatorTicket(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.limit("ticket:"+host, 60) {
		failure(w, api(429, "RATE_LIMITED"))
		return
	}
	room, e := loadRoom(r.Context(), s.pool, r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	c := ticketClaim{room.ID, time.Now().Add(5 * time.Minute).Unix(), id("watch")}
	body := base64.RawURLEncoding.EncodeToString(jsonBytes(c))
	write(w, 200, map[string]any{"ticket": body + "." + s.hash(body), "expires_at": time.Unix(c.Expiry, 0), "view_policy": "spectator_discard_only@1"})
}
func (s *Service) matchTicket(w http.ResponseWriter, r *http.Request) {
	m, e := loadMatch(r.Context(), s.pool, r.PathValue("id"), false)
	if e != nil {
		failure(w, e)
		return
	}
	r.SetPathValue("id", m.RoomID)
	s.spectatorTicket(w, r)
}
func (s *Service) verifyTicket(token, room string) bool {
	p := strings.Split(token, ".")
	if len(p) != 2 || !hmac.Equal([]byte(s.hash(p[0])), []byte(p[1])) {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return false
	}
	var c ticketClaim
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	return c.Room == room && time.Now().Unix() < c.Expiry
}
func (s *Service) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if e == nil {
		c.SetReadLimit(32 << 10)
	}
	return c, e
}
func (s *Service) spectatorWS(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("room_id")
	if rid == "" {
		failure(w, api(400, "ROOM_REQUIRED"))
		return
	}
	c, e := s.accept(w, r)
	if e != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "closed")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	timeout, stop := context.WithTimeout(ctx, 5*time.Second)
	_, b, e := c.Read(timeout)
	stop()
	var hello struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}
	if e != nil || json.Unmarshal(b, &hello) != nil || hello.Type != "authenticate" || !s.verifyTicket(hello.Ticket, rid) {
		_ = c.Close(websocket.StatusPolicyViolation, "invalid spectator ticket")
		return
	}
	s.socketLoop(ctx, c, r, rid, "", "", true, nil)
}
func (s *Service) playerWS(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	rid := r.URL.Query().Get("room_id")
	p, e := s.participant(r.Context(), rid, u.ID, "")
	if e != nil {
		failure(w, e)
		return
	}
	session, e := s.cfg.Auth.SessionID(r)
	if e != nil {
		failure(w, api(401, "AUTH_EXPIRED"))
		return
	}
	c, e := s.accept(w, r)
	if e != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "closed")
	epoch, e := s.acquireControl(r.Context(), p, session, false)
	if e != nil {
		return
	}
	defer s.disconnect(p.ParticipantID, session, epoch)
	s.socketLoop(r.Context(), c, r, rid, p.ParticipantID, session, false, func() bool { _, err := s.cfg.Auth.Authenticate(r); return err == nil })
}
func (s *Service) disconnect(pid, controller string, epoch int64) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = s.pool.Exec(ctx, `UPDATE platform_seats SET connected_until=now() WHERE participant_id=$1 AND controller=$2 AND control_epoch=$3`, pid, controller, epoch)
}
func (s *Service) socketLoop(parent context.Context, c *websocket.Conn, r *http.Request, rid, pid, controller string, readonly bool, valid func() bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	incoming := make(chan []byte, 4)
	go func() {
		defer cancel()
		for {
			_, b, e := c.Read(ctx)
			if e != nil {
				return
			}
			select {
			case incoming <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	stream := id("stream")
	var sent int64
	var previousMatch, previousHand string
	var lastSeq int64 = -1
	var lastEpoch int64 = -1
	var lastRoom string
	timer := time.NewTicker(150 * time.Millisecond)
	defer timer.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	send := func(v any) bool {
		out, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		return c.Write(out, websocket.MessageText, jsonBytes(v)) == nil
	}
	snapshot := func(force bool) bool {
		if valid != nil && !valid() {
			return false
		}
		v, e := s.snapshot(r, rid, pid)
		if e != nil {
			return send(map[string]any{"type": "error", "error": map[string]string{"code": "SNAPSHOT_UNAVAILABLE"}})
		}
		seq, _ := v["seq"].(int64)
		epoch, _ := v["control_epoch"].(int64)
		matchID, _ := v["match_id"].(string)
		hand, _ := v["hand_id"].(string)
		roomHash := string(jsonBytes(v["room"]))
		if !force && seq == lastSeq && epoch == lastEpoch && matchID == previousMatch && hand == previousHand && roomHash == lastRoom {
			return true
		}
		if epoch != lastEpoch && lastEpoch >= 0 && pid != "" {
			return send(map[string]any{"type": "control_changed", "control_epoch": epoch}) && false
		}
		if previousMatch != matchID || previousHand != hand {
			stream = id("stream")
			sent = 0
		}
		lastSeq = seq
		lastEpoch = epoch
		lastRoom = roomHash
		previousMatch = matchID
		previousHand = hand
		sent++
		v["stream_id"] = stream
		v["view_seq"] = sent
		delete(v, "seq")
		decision := v["decision"]
		delete(v, "decision")
		if !send(v) {
			return false
		}
		if d, ok := decision.(map[string]any); ok {
			d["observation_ref"] = map[string]any{"stream_id": stream, "view_seq": sent}
			sent++
			d["stream_id"] = stream
			d["view_seq"] = sent
			return send(d)
		}
		return true
	}
	if !snapshot(true) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !snapshot(false) {
				return
			}
		case <-heartbeat.C:
			if valid != nil && !valid() {
				_ = c.Close(websocket.StatusPolicyViolation, "authentication expired")
				return
			}
			if pid != "" {
				res, e := s.pool.Exec(ctx, `UPDATE platform_seats SET connected_until=now()+interval '30 seconds' WHERE participant_id=$1 AND controller=$2 AND control_epoch=$3`, pid, controller, lastEpoch)
				if e != nil || res.RowsAffected() != 1 {
					return
				}
			}
			if !send(map[string]any{"type": "heartbeat", "server_time": time.Now().UTC()}) {
				return
			}
		case raw := <-incoming:
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &head) != nil {
				return
			}
			switch head.Type {
			case "heartbeat", "ping":
				if !send(map[string]any{"type": "pong"}) {
					return
				}
			case "hello", "resume":
				if !snapshot(true) {
					return
				}
			case "ready":
				if readonly {
					continue
				}
				if valid != nil && !valid() {
					return
				}
				_, e := s.pool.Exec(ctx, `UPDATE platform_seats SET ready=true WHERE participant_id=$1 AND active AND EXISTS(SELECT 1 FROM platform_rooms WHERE id=$2 AND status='waiting')`, pid, rid)
				if e != nil {
					return
				}
				s.tryAutoStart(ctx, rid)
			case "submit_action":
				if readonly {
					if !send(map[string]any{"type": "error", "error": map[string]string{"code": "READ_ONLY"}}) {
						return
					}
					continue
				}
				if valid != nil && !valid() {
					return
				}
				var a Action
				if json.Unmarshal(raw, &a) != nil {
					return
				}
				response, e := s.Submit(ctx, pid, controller, a)
				if e != nil {
					code := "ACTION_FAILED"
					if ae, ok := e.(*APIError); ok {
						code = ae.Code
					}
					if !send(map[string]any{"type": "command_error", "command_id": a.CommandID, "error": map[string]string{"code": code}}) {
						return
					}
				} else {
					if !send(response) {
						return
					}
				}
			default:
				if !send(map[string]any{"type": "error", "error": map[string]string{"code": "UNKNOWN_MESSAGE"}}) {
					return
				}
			}
		}
	}
}
