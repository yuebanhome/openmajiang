package platform

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Admission always takes this database lock before users/rooms. Quotas must be
// checked atomically with inserts, including rooms created by the matchmaker.
func lockQuota(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(918735035)`)
	return e
}
func (s *Service) waitingQuota(ctx context.Context, tx pgx.Tx, owner string) error {
	var all, owned int
	e := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE owner_id=$1) FROM platform_rooms WHERE status='waiting'`, owner).Scan(&all, &owned)
	if e != nil {
		return e
	}
	if owned >= s.cfg.MaxOwnerWaitingRooms {
		return &APIError{429, "WAITING_ROOM_LIMIT", "你的等待房间数量已达上限，请先关闭不再使用的房间"}
	}
	if all >= s.cfg.MaxWaitingRooms {
		return &APIError{503, "ROOM_CAPACITY_REACHED", "等待房间容量已满，请稍后重试"}
	}
	return nil
}
func (s *Service) activeQuota(ctx context.Context, tx pgx.Tx) error {
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM platform_matches WHERE status='active'`).Scan(&count); e != nil {
		return e
	}
	if count >= s.cfg.MaxActiveMatches {
		return &APIError{503, "MATCH_CAPACITY_REACHED", "正在进行的牌局已达容量上限，请稍后开始"}
	}
	return nil
}
func (s *Service) queueQuota(ctx context.Context, tx pgx.Tx, key string) error {
	var count int
	var exists bool
	if e := tx.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(participant_key=$1),false) FROM platform_queue`, key).Scan(&count, &exists); e != nil {
		return e
	}
	if !exists && count >= s.cfg.MaxQueuedParticipants {
		return &APIError{503, "QUEUE_CAPACITY_REACHED", "匹配队列已满，请稍后重试"}
	}
	return nil
}
