package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) botStatus(w http.ResponseWriter, r *http.Request) {
	b, ok := s.ownBot(w, r)
	if !ok {
		return
	}
	result, e := s.botRuntimeStatus(r.Context(), b.ID)
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 200, result)
}
func (s *Service) botRuntimeStatus(ctx context.Context, bid string) (map[string]any, error) {
	result := map[string]any{"online": false, "connected": false, "presence": "offline", "active_room_id": "", "active_match_id": "", "queued": false, "continuous": false, "last_error": nil, "last_error_at": nil, "recent_errors": []any{}}
	var online bool
	e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_bot_sessions ss JOIN platform_bot_credentials c ON c.id=ss.credential_id JOIN platform_bots b ON b.id=ss.bot_id WHERE ss.bot_id=$1 AND ss.expires_at>now() AND ss.revoked_at IS NULL AND c.revoked_at IS NULL AND ss.last_seen_at>now()-interval '30 seconds' AND b.enabled AND NOT b.suspended)`, bid).Scan(&online)
	if e != nil {
		return nil, e
	}
	result["online"] = online
	if online {
		result["presence"] = "idle"
	}
	var rid, mid string
	var connected, continuous bool
	e = s.pool.QueryRow(ctx, `SELECT s.room_id,r.match_id,COALESCE(s.connected_until>now(),false),s.continuous FROM platform_seats s JOIN platform_rooms r ON r.id=s.room_id WHERE s.bot_id=$1 AND s.active`, bid).Scan(&rid, &mid, &connected, &continuous)
	if e == nil {
		result["active_room_id"] = rid
		result["active_match_id"] = mid
		result["connected"] = connected && online
		result["continuous"] = continuous
		if online {
			result["presence"] = "seated"
			if mid != "" {
				result["presence"] = "playing"
			}
		}
	} else if e != pgx.ErrNoRows {
		return nil, e
	}
	var queued bool
	var queueContinuous bool
	e = s.pool.QueryRow(ctx, `SELECT continuous FROM platform_queue WHERE bot_id=$1`, bid).Scan(&queueContinuous)
	if e == nil {
		queued = true
		result["continuous"] = queueContinuous
	} else if e != pgx.ErrNoRows {
		return nil, e
	}
	result["queued"] = queued
	rows, e := s.pool.Query(ctx, `SELECT code,at,match_id FROM platform_bot_errors WHERE bot_id=$1 ORDER BY id DESC LIMIT 20`, bid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	errors := []any{}
	for rows.Next() {
		var code, mid string
		var at time.Time
		if e = rows.Scan(&code, &at, &mid); e != nil {
			return nil, e
		}
		if len(errors) == 0 {
			result["last_error"] = code
			result["last_error_at"] = at
		}
		errors = append(errors, map[string]any{"code": code, "at": at, "match_id": mid})
	}
	result["recent_errors"] = errors
	return result, rows.Err()
}
func (s *Service) recordBotError(ctx context.Context, pid, mid, command, code string) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	if e = s.recordBotErrorTx(ctx, tx, pid, mid, command, code); e == nil {
		_ = tx.Commit(ctx)
	}
}
func (s *Service) recordBotErrorTx(ctx context.Context, tx pgx.Tx, pid, mid, command, code string) error {
	_, e := tx.Exec(ctx, `INSERT INTO platform_bot_errors(bot_id,match_id,command_id,code) SELECT bot_id,$2,$3,$4 FROM platform_seats WHERE participant_id=$1 AND kind='bot' ON CONFLICT(bot_id,match_id,command_id,code) DO NOTHING`, pid, mid, command, code)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM platform_bot_errors WHERE bot_id=(SELECT bot_id FROM platform_seats WHERE participant_id=$1 AND kind='bot') AND id NOT IN(SELECT id FROM platform_bot_errors WHERE bot_id=(SELECT bot_id FROM platform_seats WHERE participant_id=$1 AND kind='bot') ORDER BY id DESC LIMIT 100)`, pid)
	return e
}

// BotRuntimeStatus is the public schema of the authenticated owner's runtime panel.
type BotRuntimeStatus struct {
	Online        bool             `json:"online"`
	Connected     bool             `json:"connected"`
	Presence      string           `json:"presence"`
	ActiveRoomID  string           `json:"active_room_id"`
	ActiveMatchID string           `json:"active_match_id"`
	Queued        bool             `json:"queued"`
	Continuous    bool             `json:"continuous"`
	LastError     *string          `json:"last_error"`
	LastErrorAt   *time.Time       `json:"last_error_at"`
	RecentErrors  []BotRecentError `json:"recent_errors"`
}
type BotRecentError struct {
	Code    string    `json:"code"`
	At      time.Time `json:"at"`
	MatchID string    `json:"match_id"`
}
