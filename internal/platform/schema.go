package platform

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is additive and executed by the migrate command before serve.
const Schema = `
CREATE TABLE IF NOT EXISTS platform_rooms (
 id text PRIMARY KEY, name text NOT NULL, owner_id text NOT NULL,
 mode text NOT NULL CHECK(mode IN ('human_only','mixed','bot_only')),
 ruleset_id text NOT NULL, ruleset_version text NOT NULL, match_format text NOT NULL,
 online_profile text NOT NULL, capacity integer NOT NULL, invite_hash text NOT NULL DEFAULT '', self_test boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'waiting', match_id text NOT NULL DEFAULT '',
 queue_deadline timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_seats (
 participant_id text PRIMARY KEY, room_id text NOT NULL REFERENCES platform_rooms(id),
 user_id text NOT NULL, bot_id text NOT NULL DEFAULT '', name text NOT NULL, kind text NOT NULL,
 self_timeouts integer NOT NULL DEFAULT 0, reaction_timeouts integer NOT NULL DEFAULT 0, bot_version text NOT NULL DEFAULT '', builtin_strategy text NOT NULL DEFAULT 'random_legal', continuous boolean NOT NULL DEFAULT false, seat_order integer NOT NULL, ready boolean NOT NULL DEFAULT false, active boolean NOT NULL DEFAULT true,
 control_epoch bigint NOT NULL DEFAULT 0, controller text NOT NULL DEFAULT '', controller_session text NOT NULL DEFAULT '',
 connected_until timestamptz, leave_after_hand boolean NOT NULL DEFAULT false,
 UNIQUE(room_id,seat_order)
);
CREATE INDEX IF NOT EXISTS platform_pending_continuous ON platform_seats(participant_id) WHERE continuous AND NOT active;
CREATE UNIQUE INDEX IF NOT EXISTS platform_one_human_seat ON platform_seats(user_id) WHERE active AND kind='human';
CREATE UNIQUE INDEX IF NOT EXISTS platform_one_bot_seat ON platform_seats(bot_id) WHERE active AND bot_id<>'';
CREATE TABLE IF NOT EXISTS platform_matches (
 id text PRIMARY KEY, room_id text NOT NULL REFERENCES platform_rooms(id),
 ruleset_id text NOT NULL, ruleset_version text NOT NULL, match_format text NOT NULL,
 artifact_hash text NOT NULL, manifest jsonb NOT NULL, config jsonb NOT NULL, state jsonb NOT NULL, seq bigint NOT NULL DEFAULT 1, status text NOT NULL DEFAULT 'active',
 window_id text NOT NULL DEFAULT '', deadline_at timestamptz, next_run_at timestamptz NOT NULL DEFAULT now(),
 choices jsonb NOT NULL DEFAULT '{}', owner_id text NOT NULL, owner_epoch bigint NOT NULL DEFAULT 1,
 owner_until timestamptz NOT NULL, interrupted boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_commands (
 participant_id text NOT NULL, match_id text NOT NULL REFERENCES platform_matches(id),
 command_id text NOT NULL, payload_hash text NOT NULL, decision_id text NOT NULL,
 response jsonb NOT NULL, accepted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(participant_id,match_id,command_id), UNIQUE(participant_id,match_id,decision_id)
);
CREATE TABLE IF NOT EXISTS platform_views (
 match_id text NOT NULL REFERENCES platform_matches(id), seq bigint NOT NULL, hand_index integer NOT NULL,
 participant_id text NOT NULL, view jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(match_id,seq,participant_id)
);
CREATE TABLE IF NOT EXISTS platform_events (
 match_id text NOT NULL REFERENCES platform_matches(id), seq bigint NOT NULL, events jsonb NOT NULL,
 input jsonb NOT NULL, owner_epoch bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(match_id,seq)
);
CREATE TABLE IF NOT EXISTS platform_bots (
 id text PRIMARY KEY, owner_id text NOT NULL, name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, suspended boolean NOT NULL DEFAULT false, current_version text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_bot_versions (
 id text PRIMARY KEY, bot_id text NOT NULL REFERENCES platform_bots(id), label text NOT NULL,
 metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_bot_credentials (
 id text PRIMARY KEY, bot_id text NOT NULL REFERENCES platform_bots(id), secret_hash text NOT NULL UNIQUE,
 revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_bot_sessions (
 id text PRIMARY KEY, bot_id text NOT NULL REFERENCES platform_bots(id),
 credential_id text NOT NULL REFERENCES platform_bot_credentials(id), secret_hash text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL, revoked_at timestamptz, last_seen_at timestamptz, capabilities jsonb NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS platform_bot_errors (
 id bigserial PRIMARY KEY,bot_id text NOT NULL REFERENCES platform_bots(id),match_id text NOT NULL,command_id text NOT NULL,code text NOT NULL,at timestamptz NOT NULL DEFAULT now(),UNIQUE(bot_id,match_id,command_id,code)
);
CREATE TABLE IF NOT EXISTS platform_queue (
 participant_key text PRIMARY KEY, user_id text NOT NULL, bot_id text NOT NULL DEFAULT '',
 ruleset_id text NOT NULL, ruleset_version text NOT NULL, match_format text NOT NULL,
 continuous boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS platform_audit (
 id bigserial PRIMARY KEY, actor_id text NOT NULL, action text NOT NULL, target_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
`

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, Schema)
	if err != nil {
		return err
	}
	if err = AdminMigrate(ctx, pool); err != nil {
		return err
	}
	if err = StatsMigrate(ctx, pool); err != nil {
		return err
	}
	return ArchiveMigrate(ctx, pool)
}

// CheckSchema fails readiness until the required migration is present.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var n int
	return pool.QueryRow(ctx, `SELECT count(m.artifact_hash)+count(m.archived_at)+count(m.public_summary)+count(s.controller_session)+count(s.self_timeouts)+count(d.decision_id)+count(sys.singleton)+count(a.csrf_token)+count(mail.encrypted_body) FROM platform_matches m,platform_seats s,platform_stat_decisions d,platform_system sys,auth_sessions a,auth_mail_outbox mail WHERE false`).Scan(&n)
}
