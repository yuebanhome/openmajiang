package platform

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	host := s.clientIP(r)
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
func (s *Service) accept(w http.ResponseWriter, r *http.Request, identity string) (*websocket.Conn, func(), error) {
	ip := "ip:" + s.clientIP(r)
	roomKey := ""
	if identity == "" {
		roomKey = "watch-room:" + r.URL.Query().Get("room_id")
	}
	s.mu.Lock()
	if s.sockets["all"] >= 2048 || s.sockets[ip] >= 32 || (roomKey != "" && s.sockets[roomKey] >= 500) || (identity != "" && s.sockets[identity] >= 4) {
		s.mu.Unlock()
		failure(w, api(429, "CONNECTION_LIMIT"))
		return nil, func() {}, api(429, "CONNECTION_LIMIT")
	}
	s.sockets["all"]++
	s.sockets[ip]++
	if roomKey != "" {
		s.sockets[roomKey]++
	}
	if identity != "" {
		s.sockets[identity]++
	}
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, k := range []string{"all", ip, identity, roomKey} {
			if k != "" {
				s.sockets[k]--
				if s.sockets[k] <= 0 {
					delete(s.sockets, k)
				}
			}
		}
	}
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if e != nil {
		release()
		return nil, func() {}, e
	}
	c.SetReadLimit(32 << 10)
	return c, release, nil
}

func (s *Service) spectatorWS(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("room_id")
	if rid == "" {
		failure(w, api(400, "ROOM_REQUIRED"))
		return
	}
	c, release, e := s.accept(w, r, "")
	if e != nil {
		return
	}
	defer release()
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
	c, release, e := s.accept(w, r, "user:"+u.ID)
	if e != nil {
		return
	}
	defer release()
	defer c.Close(websocket.StatusNormalClosure, "closed")
	control, epoch, granted, e := s.acquireControl(r.Context(), p, session, false, false)
	if e != nil {
		return
	}
	message := map[string]any{"type": "control_readonly", "control_epoch": epoch}
	if granted {
		message["type"] = "control_granted"
		message["control_token"] = control
	}
	out, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	e = c.Write(out, websocket.MessageText, jsonBytes(message))
	cancel()
	if e != nil {
		return
	}
	s.socketLoop(r.Context(), c, r, rid, p.ParticipantID, control, !granted, func() bool { _, err := s.cfg.Auth.Authenticate(r); return err == nil })
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
	var previousMatch, previousHand, previousStatus string
	var lastSeq int64 = -1
	var lastEpoch int64 = -1
	var lastRoom string
	var lastPublicDigest [sha256.Size]byte
	var havePublic bool
	timer := time.NewTicker(150 * time.Millisecond)
	defer timer.Stop()
	defer func() {
		if pid != "" && controller != "" {
			s.disconnect(pid, controller, lastEpoch)
		}
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	send := func(v any) bool {
		out, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		return c.Write(out, websocket.MessageText, jsonBytes(v)) == nil
	}
	snapshot := func(force bool) bool {
		if !force && pid != "" && previousMatch != "" && lastSeq >= 0 {
			var seq, epoch int64
			var status string
			err := s.pool.QueryRow(ctx, `SELECT m.seq,m.status,s.control_epoch FROM platform_matches m JOIN platform_seats s ON s.room_id=m.room_id WHERE m.id=$1 AND s.participant_id=$2`, previousMatch, pid).Scan(&seq, &status, &epoch)
			if err == nil && seq == lastSeq && epoch == lastEpoch && status == previousStatus {
				return true
			}
		}
		if valid != nil && !valid() {
			return false
		}
		var v map[string]any
		var e error
		if pid == "" {
			var entry publicCacheEntry
			entry, e = s.cachedPublicEntry(r, rid)
			if e == nil {
				// Thousands of viewers may poll an unchanged table. Comparing an
				// immutable public digest avoids decoding the same complete JSON
				// for every viewer on every tick. Changed frames still get their
				// own object before any recipient-specific fields are attached.
				if !force && havePublic && entry.digest == lastPublicDigest {
					return true
				}
				v, e = decodePublicSnapshot(entry.body)
				if e == nil {
					lastPublicDigest, havePublic = entry.digest, true
				}
			}
		} else {
			v, e = s.snapshot(r, rid, pid)
		}
		if e != nil {
			return send(map[string]any{"type": "error", "error": map[string]string{"code": "SNAPSHOT_UNAVAILABLE"}})
		}
		seq := wireInt64(v["seq"])
		epoch := wireInt64(v["control_epoch"])
		matchID, _ := v["match_id"].(string)
		hand, _ := v["hand_id"].(string)
		var roomHash string
		if pid != "" {
			roomHash = string(jsonBytes(v["room"]))
		}
		status, _ := v["status"].(string)
		if pid != "" && !force && seq == lastSeq && epoch == lastEpoch && matchID == previousMatch && hand == previousHand && roomHash == lastRoom && status == previousStatus {
			return true
		}
		if epoch != lastEpoch && lastEpoch >= 0 && pid != "" && !readonly {
			return send(map[string]any{"type": "control_changed", "control_epoch": epoch}) && false
		}
		if previousMatch != matchID || previousHand != hand {
			if pid != "" && hand != "" {
				if !send(map[string]any{"type": "seat_assigned", "match_id": matchID, "hand_id": hand, "participant_id": pid, "seat_id": v["seat_id"], "seat_assignment_version": v["seat_assignment_version"], "control_epoch": epoch}) {
					return false
				}
			}
			stream = id("stream")
			sent = 0
		}
		lastSeq = seq
		lastEpoch = epoch
		lastRoom = roomHash
		previousMatch = matchID
		previousHand = hand
		previousStatus, _ = v["status"].(string)
		sent++
		v["stream_id"] = stream
		v["view_seq"] = sent
		delete(v, "seq")
		decision := v["decision"]
		if readonly {
			decision = nil
			if pid != "" {
				v["control_status"] = "readonly"
			}
		} else if pid != "" {
			v["control_status"] = "owner"
		}
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
			if pid != "" && !readonly {
				res, e := s.pool.Exec(ctx, `UPDATE platform_seats SET connected_until=now()+interval '30 seconds' WHERE participant_id=$1 AND controller=$2 AND control_epoch=$3`, pid, controller, lastEpoch)
				if e != nil || res.RowsAffected() != 1 {
					return
				}
			}
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			pingErr := c.Ping(pingCtx)
			pingCancel()
			if pingErr != nil {
				return
			}
			if !send(map[string]any{"type": "heartbeat", "server_time": time.Now().UTC()}) {
				return
			}
		case raw := <-incoming:
			if !s.limit("wsmsg:"+s.clientIP(r)+":"+pid, 600) {
				_ = c.Close(websocket.StatusPolicyViolation, "rate limit")
				return
			}
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
			case "resume_control":
				if pid == "" {
					continue
				}
				if valid != nil && !valid() {
					return
				}
				var claim struct {
					Token string `json:"control_token"`
				}
				if json.Unmarshal(raw, &claim) != nil || claim.Token == "" {
					continue
				}
				var epoch int64
				err := s.pool.QueryRow(ctx, `SELECT control_epoch FROM platform_seats WHERE participant_id=$1 AND controller=$2 AND active`, pid, claim.Token).Scan(&epoch)
				if err != nil {
					if !send(map[string]any{"type": "control_readonly"}) {
						return
					}
					continue
				}
				controller = claim.Token
				readonly = false
				lastEpoch = epoch
				if !send(map[string]any{"type": "control_granted", "control_token": controller, "control_epoch": epoch}) || !snapshot(true) {
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
				if e := s.readyCommand(ctx, pid, rid, controller, lastEpoch); e != nil {
					_ = send(map[string]any{"type": "error", "error": map[string]string{"code": "STALE_CONTROL"}})
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
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&a) != nil {
					if !send(map[string]any{"type": "command_error", "command_id": a.CommandID, "error": map[string]string{"code": "INVALID_REQUEST"}}) {
						return
					}
					continue
				}
				response, e := s.Submit(ctx, pid, controller, a)
				if e != nil {
					code := "ACTION_FAILED"
					if ae, ok := e.(*APIError); ok {
						code = ae.Code
					}
					s.recordBotError(ctx, pid, a.MatchID, a.CommandID, code)
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

func wireInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		x, _ := n.Int64()
		return x
	}
	return 0
}
