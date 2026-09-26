#!/usr/bin/env bash
# Restore a real PostgreSQL custom-format backup into an isolated CI database.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${CI:-}" = true
: "${POSTGRES_CONTAINER:?Pass the GitHub Actions PostgreSQL service container ID}"
restore_db=openmajiang_ci_restore
backup_file="$(mktemp)"
cleanup() {
  docker logs openmajiang-restore 2>&1 | tail -50 || true
  docker rm -f openmajiang-restore >/dev/null 2>&1 || true
  docker exec "$POSTGRES_CONTAINER" dropdb --if-exists --username=openmajiang "$restore_db" >/dev/null 2>&1 || true
  rm -f "$backup_file"
}
trap cleanup EXIT
# Quiesce the source before comparing row counts; a running game would otherwise
# legitimately advance after pg_dump's consistent snapshot was taken.
docker stop --time 30 openmajiang-smoke >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' openmajiang-smoke)" = 0
docker exec "$POSTGRES_CONTAINER" pg_dump --format=custom --no-owner \
  --username=openmajiang --dbname=openmajiang > "$backup_file"
test -s "$backup_file"
docker exec "$POSTGRES_CONTAINER" createdb --username=openmajiang "$restore_db"
docker exec -i "$POSTGRES_CONTAINER" pg_restore --exit-on-error --single-transaction --no-owner \
  --username=openmajiang --dbname="$restore_db" < "$backup_file"
query="SELECT (SELECT count(*) FROM auth_users),(SELECT count(*) FROM platform_matches),(SELECT count(*) FROM platform_views),(SELECT count(*) FROM platform_events),(SELECT count(*) FROM platform_audit)"
source_counts="$(docker exec "$POSTGRES_CONTAINER" psql -At --username=openmajiang --dbname=openmajiang -c "$query")"
restore_counts="$(docker exec "$POSTGRES_CONTAINER" psql -At --username=openmajiang --dbname="$restore_db" -c "$query")"
test "$source_counts" = "$restore_counts"
printf '%s' "$source_counts" | python3 -c 'import sys; counts=[int(v) for v in sys.stdin.read().split("|")]; assert all(v > 0 for v in counts[:4]), "restore drill requires real account, match, view and event rows"'
restore_dsn="$(python3 - <<'PY'
import os
import urllib.parse
v = urllib.parse.urlsplit(os.environ['DATABASE_URL'])
print(urllib.parse.urlunsplit((v.scheme, v.netloc, '/openmajiang_ci_restore', v.query, v.fragment)))
PY
)"
docker run --rm --network host -e DATABASE_URL="$restore_dsn" openmajiang:verify migrate
docker run -d --name openmajiang-restore --network host \
  -e DATABASE_URL="$restore_dsn" -e HTTP_ADDR=:8081 -e PUBLIC_BASE_URL=http://127.0.0.1:8081 \
  -e AUTH_MAIL_KEY -e TOKEN_HASH_KEY -e COOKIE_SECURE=false \
  -e SMTP_ADDR -e SMTP_FROM -e SMTP_STARTTLS openmajiang:verify >/dev/null
SMOKE_BASE_URL=http://127.0.0.1:8081 ./deploy/smoke.sh
docker stop --time 30 openmajiang-restore >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' openmajiang-restore)" = 0
test "$(docker inspect --format '{{.State.OOMKilled}}' openmajiang-restore)" = false
docker start openmajiang-restore >/dev/null
SMOKE_BASE_URL=http://127.0.0.1:8081 ./deploy/smoke.sh
printf 'Real PostgreSQL backup/restore, migration reentry, graceful SIGTERM and restart passed.\n'
