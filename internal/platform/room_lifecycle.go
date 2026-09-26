package platform

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Membership changes invalidate readiness: every human/runner must consent to
// the new line-up. Builtins have no separate owner consent to solicit.
func resetReady(ctx context.Context, tx pgx.Tx, rid string) error {
	_, e := tx.Exec(ctx, `UPDATE platform_seats SET ready=(kind='builtin') WHERE room_id=$1 AND active`, rid)
	return e
}
func (s *Service) rematch(w http.ResponseWriter, r *http.Request) {
	u, e := s.user(r, true)
	if e != nil {
		failure(w, e)
		return
	}
	old, e := loadRoom(r.Context(), s.pool, r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	if old.OwnerID != u.ID || old.Status != "ended" && old.Status != "cancelled" {
		failure(w, api(403, "REMATCH_NOT_ALLOWED"))
		return
	}
	room, invite, e := s.create(r.Context(), u, createRequest{Name: old.Name, Mode: old.Mode, RuleID: old.RulesetID, RuleVersion: old.RulesetVersion, Format: old.Format, Profile: old.Profile, Capacity: old.Capacity, InviteOnly: old.InviteOnly, SelfTest: old.SelfTest})
	if e != nil {
		failure(w, e)
		return
	}
	write(w, 201, map[string]any{"room": room, "invite_code": invite})
}
func (s *Service) cancelQueue(ctx context.Context, uid, bid string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var lock string
	if e = tx.QueryRow(ctx, `SELECT id FROM auth_users WHERE id=$1 FOR UPDATE`, uid).Scan(&lock); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM platform_queue WHERE user_id=$1 AND bot_id=$2`, uid, bid)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE platform_seats SET continuous=false WHERE user_id=$1 AND bot_id=$2`, uid, bid)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT r.id FROM platform_rooms r WHERE r.status='waiting' AND r.queue_deadline IS NOT NULL AND EXISTS(SELECT 1 FROM platform_seats s WHERE s.room_id=r.id AND s.user_id=$1 AND s.bot_id=$2 AND s.active) FOR UPDATE`, uid, bid)
	if e != nil {
		return e
	}
	var rooms []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			rooms = append(rooms, id)
		}
	}
	rows.Close()
	for _, rid := range rooms {
		_, e = tx.Exec(ctx, `UPDATE platform_rooms SET status='cancelled' WHERE id=$1`, rid)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE platform_seats SET active=false WHERE room_id=$1`, rid)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) closeRoom(w http.ResponseWriter, r *http.Request) {
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
	var owner, status string
	e = tx.QueryRow(r.Context(), `SELECT owner_id,status FROM platform_rooms WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&owner, &status)
	if e != nil {
		failure(w, api(404, "ROOM_NOT_FOUND"))
		return
	}
	if owner != u.ID {
		failure(w, api(403, "FORBIDDEN"))
		return
	}
	if status != "waiting" {
		failure(w, api(409, "ROOM_NOT_WAITING"))
		return
	}
	_, e = tx.Exec(r.Context(), `UPDATE platform_rooms SET status='cancelled' WHERE id=$1`, r.PathValue("id"))
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE platform_seats SET active=false WHERE room_id=$1`, r.PathValue("id"))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		failure(w, e)
		return
	}
	s.invalidatePublicSnapshots()
	write(w, 200, map[string]any{"ok": true})
}
