#!/usr/bin/env bash
# Real TLS/WSS integration gate. Requires PostgreSQL, Mailpit, Docker and the
# local openmajiang:verify image. Runtime clocks are unchanged.
set -euo pipefail
cd "$(dirname "$0")/.."
cert_dir="$(mktemp -d)"
cleanup() {
  docker logs openmajiang-wss 2>&1 | tail -100 || true
  docker logs openmajiang-tls 2>&1 | tail -30 || true
  docker rm -f openmajiang-tls openmajiang-wss >/dev/null 2>&1 || true
  rm -rf "$cert_dir"
}
trap cleanup EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj '/CN=OpenMajiang CI CA' \
  -keyout "$cert_dir/ca.key" -out "$cert_dir/ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=localhost' \
  -keyout "$cert_dir/server.key" -out "$cert_dir/server.csr" >/dev/null 2>&1
cat > "$cert_dir/extensions" <<'CERT'
subjectAltName=DNS:localhost,IP:127.0.0.1
extendedKeyUsage=serverAuth
basicConstraints=critical,CA:false
CERT
openssl x509 -req -days 2 -in "$cert_dir/server.csr" \
  -CA "$cert_dir/ca.pem" -CAkey "$cert_dir/ca.key" -CAcreateserial \
  -extfile "$cert_dir/extensions" -out "$cert_dir/server.pem" >/dev/null 2>&1
cat > "$cert_dir/Caddyfile" <<'CADDY'
{
    admin off
    auto_https off
}
https://localhost:18443 {
    tls /certs/server.pem /certs/server.key
    reverse_proxy 127.0.0.1:8080
}
CADDY
docker run --rm --network host -e DATABASE_URL openmajiang:verify migrate
docker run -d --name openmajiang-wss --network host \
  -e DATABASE_URL -e AUTH_MAIL_KEY -e TOKEN_HASH_KEY \
  -e PUBLIC_BASE_URL=https://localhost:18443 -e COOKIE_SECURE=true \
  -e TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128 \
  -e SMTP_ADDR -e SMTP_FROM -e SMTP_STARTTLS openmajiang:verify >/dev/null
docker run -d --name openmajiang-tls --network host \
  -v "$cert_dir:/certs:ro" -v "$cert_dir/Caddyfile:/etc/caddy/Caddyfile:ro" \
  caddy:2.10.2-alpine >/dev/null
ready=false
for attempt in $(seq 1 60); do
  if curl --fail --silent --max-time 2 --cacert "$cert_dir/ca.pem" \
    https://localhost:18443/health/ready >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true
mkdir -p test-results
SSL_CERT_FILE="$cert_dir/ca.pem" .venv-sdk/bin/python sdk/integration/wss-smoke.py \
  --base-url https://localhost:18443 --mailpit-url http://127.0.0.1:8025 \
  --hands 100 --format standard_16 --timeout 6900 --report test-results/bot-wss.json
