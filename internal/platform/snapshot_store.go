package platform

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// A single MVCC read keeps the selected projection and metadata at the same
// committed sequence. Public reads never fetch the authoritative rule state.
type snapshotRecord struct {
	room                                           Room
	match                                          match
	view                                           json.RawMessage
	index, ownSeat, selfTimeouts, reactionTimeouts int
	epoch                                          int64
	deleted                                        map[string]bool
}

func (s *Service) loadSnapshot(ctx context.Context, roomID, pid string) (snapshotRecord, error) {
	var out snapshotRecord
	var seats json.RawMessage
	var deleted []string
	r, m := &out.room, &out.match
	err := s.pool.QueryRow(ctx, `SELECT
 r.id,r.name,r.owner_id,r.mode,r.ruleset_id,r.ruleset_version,r.match_format,r.capacity,r.invite_hash<>'',r.self_test,r.status,r.match_id,r.online_profile,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('participant_id',p.participant_id,'bot_id',p.bot_id,'name',p.name,'kind',p.kind,'seat_id',p.seat_order,'ready',p.ready,'connected',COALESCE(p.connected_until>now(),false),'leave_after_hand',p.leave_after_hand) ORDER BY p.seat_order) FROM platform_seats p WHERE p.room_id=r.id AND p.active),'[]'::jsonb),
 COALESCE(m.id,''),COALESCE(m.ruleset_id,''),COALESCE(m.ruleset_version,''),COALESCE(m.match_format,''),
 CASE WHEN $2<>'' THEN m.state ELSE NULL END,COALESCE(m.seq,0),COALESCE(m.status,''),m.deadline_at,COALESCE(m.interrupted,false),m.archived_at,
 v.view,COALESCE(v.hand_index,0),COALESCE(own.control_epoch,0),COALESCE(own.seat_order,0),COALESCE(own.self_timeouts,0),COALESCE(own.reaction_timeouts,0),
 ARRAY(SELECT p.participant_id FROM platform_seats p JOIN auth_users u ON u.id=p.user_id WHERE p.room_id=r.id AND u.status='deleted')
 FROM platform_rooms r
 LEFT JOIN platform_matches m ON m.id=r.match_id
 LEFT JOIN platform_views v ON v.match_id=m.id AND v.seq=m.seq AND v.participant_id=$2
 LEFT JOIN platform_seats own ON own.room_id=r.id AND own.participant_id=$2
 WHERE r.id=$1`, roomID, pid).Scan(
		&r.ID, &r.Name, &r.OwnerID, &r.Mode, &r.RulesetID, &r.RulesetVersion, &r.Format, &r.Capacity, &r.InviteOnly, &r.SelfTest, &r.Status, &r.MatchID, &r.Profile, &seats,
		&m.ID, &m.RulesetID, &m.RulesetVersion, &m.Format, &m.State, &m.Seq, &m.Status, &m.Deadline, &m.Interrupted, &m.ArchivedAt,
		&out.view, &out.index, &out.epoch, &out.ownSeat, &out.selfTimeouts, &out.reactionTimeouts, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, api(404, "ROOM_NOT_FOUND")
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(seats, &r.Seats); err != nil {
		return out, err
	}
	m.RoomID = r.ID
	out.deleted = make(map[string]bool, len(deleted))
	for _, id := range deleted {
		out.deleted[id] = true
	}
	if r.MatchID != "" && m.ID == "" {
		return out, api(404, "MATCH_NOT_FOUND")
	}
	return out, nil
}
