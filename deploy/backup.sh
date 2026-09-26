#!/usr/bin/env bash
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
mkdir -p backups
destination="backups/openmajiang-$(date -u +%Y%m%dT%H%M%SZ).dump"
temporary="${destination}.partial"
trap 'rm -f "$temporary"' EXIT
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T db \
  pg_dump --format=custom --no-owner --username=openmajiang --dbname=openmajiang > "$temporary"
test -s "$temporary"
mv "$temporary" "$destination"
sha256sum "$destination" > "${destination}.sha256"
printf 'Database backup: %s\n' "$destination"
printf 'Keep matching image digest and runtime encryption keys separately.\n'
