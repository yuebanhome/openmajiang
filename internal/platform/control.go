package platform

import (
	"context"
)

// Controller grants and account/session revocation share the auth user row lock.
func (s *Service) acquireControl(ctx context.Context, p Seat, session string, bot bool) (int64, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	if e = lockEligible(ctx, tx, p.UserID); e != nil {
		return 0, e
	}
	var valid bool
	if bot {
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_bot_sessions ss JOIN platform_bot_credentials c ON c.id=ss.credential_id JOIN platform_bots b ON b.id=ss.bot_id WHERE ss.id=$1 AND ss.bot_id=$2 AND ss.expires_at>now() AND ss.revoked_at IS NULL AND c.revoked_at IS NULL AND b.enabled AND NOT b.suspended)`, session, p.BotID).Scan(&valid)
	} else {
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND expires_at>now() AND revoked_at IS NULL)`, session, p.UserID).Scan(&valid)
	}
	if e != nil {
		return 0, e
	}
	if !valid {
		return 0, api(401, "AUTH_EXPIRED")
	}
	var epoch int64
	e = tx.QueryRow(ctx, `UPDATE platform_seats SET control_epoch=control_epoch+1,controller=$2,connected_until=now()+interval '30 seconds' WHERE participant_id=$1 AND active RETURNING control_epoch`, p.ParticipantID, session).Scan(&epoch)
	if e != nil {
		return 0, api(403, "FORBIDDEN_SEAT")
	}
	return epoch, tx.Commit(ctx)
}
