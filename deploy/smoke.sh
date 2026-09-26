#!/usr/bin/env bash
set -euo pipefail
base="${SMOKE_BASE_URL:-http://127.0.0.1:8080}"
ready=false
for attempt in $(seq 1 45); do
  if curl --fail --silent --max-time 2 "$base/health/ready" >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  printf 'Application did not become ready: %s\n' "$base" >&2
  exit 1
fi
curl --fail --silent --show-error --max-time 5 "$base/health/live" >/dev/null
curl --fail --silent --show-error --max-time 5 "$base/" | python3 -c \
  'import sys; body=sys.stdin.read(); assert "<html" in body and "<script" in body, "embedded React application missing"'
printf 'Readiness, health and embedded frontend smoke passed.\n'
