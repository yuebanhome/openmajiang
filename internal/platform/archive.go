package platform

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

const archiveRetentionDays = 30
const terminalArchiveStatuses = `('completed','ended_early','aborted_by_server','aborted_by_operator','missing_ruleset','invalid_state')`

func ArchiveMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `ALTER TABLE platform_matches ADD COLUMN IF NOT EXISTS archived_at timestamptz;
ALTER TABLE platform_matches ADD COLUMN IF NOT EXISTS public_summary jsonb NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS platform_archive_due ON platform_matches(updated_at) WHERE archived_at IS NULL AND status<>'active';`)
	return err
}

type archivedScore struct {
	ParticipantID string            `json:"participant_id"`
	Kind          string            `json:"kind"`
	Total         int64             `json:"total"`
	Rank          *int              `json:"rank,omitempty"`
	Standard      *rulesdk.Rational `json:"standard_points,omitempty"`
}
type archiveSummary struct {
	Version        string          `json:"version"`
	CompletedHands int             `json:"completed_hands"`
	Scores         []archivedScore `json:"scores"`
}

func buildArchiveSummary(ctx context.Context, tx pgx.Tx, mid string) (json.RawMessage, error) {
	summary := archiveSummary{Version: "settled-scores@1", Scores: []archivedScore{}}
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT hand_index) FROM platform_stat_hands WHERE match_id=$1`, mid).Scan(&summary.CompletedHands); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.participant_id,p.participant_kind,
COALESCE(z.raw_score,h.total,0),z.rank,z.standard_n,z.standard_d
FROM platform_stat_participants p
LEFT JOIN (SELECT participant_id,sum(net_points) AS total FROM platform_stat_hands WHERE match_id=$1 GROUP BY participant_id) h USING(participant_id)
LEFT JOIN platform_stat_rankings z ON z.match_id=p.match_id AND z.participant_id=p.participant_id
WHERE p.match_id=$1 ORDER BY p.participant_id`, mid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var score archivedScore
		var numerator, denominator *int
		if err = rows.Scan(&score.ParticipantID, &score.Kind, &score.Total, &score.Rank, &numerator, &denominator); err != nil {
			return nil, err
		}
		if numerator != nil && denominator != nil && *denominator > 0 {
			score.Standard = &rulesdk.Rational{Numerator: *numerator, Denominator: *denominator}
		}
		summary.Scores = append(summary.Scores, score)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(summary)
}

// PurgeArchives atomically removes every replay-capable representation of up to
// 100 terminal matches. Active matches and already-settled non-card statistics
// are untouched. Call on startup and periodically; repeat a batch if count=100.
func (s *Service) PurgeArchives(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM platform_matches WHERE archived_at IS NULL
AND status IN `+terminalArchiveStatuses+` AND updated_at<now()-($1::int * interval '1 day')
ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED`, archiveRetentionDays)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, mid := range ids {
		summary, e := buildArchiveSummary(ctx, tx, mid)
		if e != nil {
			return 0, e
		}
		for _, q := range []string{
			`DELETE FROM platform_commands WHERE match_id=$1`,
			`DELETE FROM platform_events WHERE match_id=$1`,
			`DELETE FROM platform_views WHERE match_id=$1`,
			`UPDATE platform_seats SET controller='',controller_session='',connected_until=NULL,control_epoch=control_epoch+1 WHERE room_id=(SELECT room_id FROM platform_matches WHERE id=$1)`,
			`UPDATE platform_rooms SET invite_hash='' WHERE match_id=$1`,
		} {
			if _, e = tx.Exec(ctx, q, mid); e != nil {
				return 0, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE platform_matches SET state='{}',choices='{}',config='{}',window_id='',deadline_at=NULL,owner_id='',owner_epoch=owner_epoch+1,archived_at=now(),public_summary=$2 WHERE id=$1`, mid, summary); e != nil {
			return 0, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		s.invalidatePublicSnapshots()
	}
	return len(ids), nil
}

func requireNotArchived(ctx context.Context, q queryer, mid string) error {
	var at *time.Time
	if err := q.QueryRow(ctx, `SELECT archived_at FROM platform_matches WHERE id=$1`, mid).Scan(&at); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return api(404, "MATCH_NOT_FOUND")
		}
		return err
	}
	if at != nil {
		return &APIError{Status: 410, Code: "MATCH_ARCHIVED", Message: "牌谱已超过30天保留期，已完成比分仍可查看"}
	}
	return nil
}

// The retained payload is generated from typed non-card statistics only, never
// from an engine snapshot, event payload or arbitrary plugin metadata.
func (s *Service) archivedSummary(ctx context.Context, mid string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT public_summary FROM platform_matches WHERE id=$1 AND archived_at IS NOT NULL`, mid).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api(404, "ARCHIVE_NOT_FOUND")
	}
	return raw, err
}
