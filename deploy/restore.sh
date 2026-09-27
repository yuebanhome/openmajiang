#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
backup="${1:?Usage: RESTORE_CONFIRM=replace-openmajiang ./deploy/restore.sh backups/file.dump}"
if [[ "${RESTORE_CONFIRM:-}" != replace-openmajiang ]]; then
  printf 'Restore replaces the database. Set RESTORE_CONFIRM=replace-openmajiang after checking the target.\n' >&2
  exit 2
fi
test -s "$backup"
if [[ -f "${backup}.sha256" ]]; then
  sha256sum -c "${backup}.sha256"
fi
compose=(docker compose --env-file deploy/.env -f deploy/compose.yaml)
"${compose[@]}" stop app
"${compose[@]}" exec -T db pg_restore --list < "$backup" >/dev/null
"${compose[@]}" exec -T db pg_restore --clean --if-exists --no-owner \
  --exit-on-error --single-transaction --username=openmajiang --dbname=openmajiang < "$backup"
printf 'Restore complete. App remains stopped. Verify the image version and keys before starting it.\n'
