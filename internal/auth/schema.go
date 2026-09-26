package auth

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate installs idempotent account tables. Schema evolution is versioned by the
// application's deployment migration gate; it must finish before serving traffic.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS auth_users (
 id text PRIMARY KEY, email text UNIQUE, name text NOT NULL,
 password_hash text NOT NULL, verified boolean NOT NULL DEFAULT false,
 role text NOT NULL DEFAULT 'user' CHECK(role IN ('user','operator')),
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','restricted','deleted')),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 terms_version text NOT NULL DEFAULT 'v1',terms_accepted_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS auth_sessions (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES auth_users(id),
 secret_hash text UNIQUE NOT NULL, csrf_token text NOT NULL,
 user_agent text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(),
 last_seen_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS auth_sessions_user ON auth_sessions(user_id);
CREATE TABLE IF NOT EXISTS auth_email_tokens (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES auth_users(id),
 purpose text NOT NULL CHECK(purpose IN ('verify','reset','email_change')),
 token_hash text UNIQUE NOT NULL, target_email text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
 consumed_at timestamptz
);
CREATE INDEX IF NOT EXISTS auth_email_tokens_user ON auth_email_tokens(user_id,purpose,created_at);
CREATE TABLE IF NOT EXISTS auth_mail_outbox (
 id text PRIMARY KEY, user_id text REFERENCES auth_users(id),
 recipient text NOT NULL, subject text NOT NULL, encrypted_body bytea NOT NULL,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','sending','sent','failed')),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), sent_at timestamptz,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS auth_mail_ready ON auth_mail_outbox(status,available_at);
`
