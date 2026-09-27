# Account service

`Migrate(ctx, pool)` runs before serving. `New(pool, Config)` requires PostgreSQL,
an absolute public base URL, a stable random 32-byte mail encryption key, and SMTP
settings (or an injected `Mailer` for tests). Run `go service.StartMailWorker(ctx)`
with the application's cancellation context. Configuration does not contain a
development authentication bypass.

Passwords use Argon2id, random salts, 64 MiB memory, three iterations, and two
lanes. At most two password operations run concurrently. Parameters are embedded
in the encoded hash for future migration. Passwords contain 15–128 Unicode
characters and are not trimmed. Normalized lowercase email is the login key;
display names need not be unique. Public identity remains the immutable user ID.

Sessions and email tokens are 256-bit random secrets. Only SHA-256 hashes are
stored in their credential tables. One-time mail links are temporarily stored
inside AES-GCM encrypted outbox bodies; successful delivery erases the body. The
mail encryption key must survive deployment and backup restoration. Lost keys
invalidate pending messages and require issuing new links. SMTP retries use a
two-minute claim lease and bounded exponential backoff; ten failed attempts
become `failed`, visible to the account through its delivery endpoint. The sender
never logs token-bearing links, message bodies, raw SMTP responses, or passwords.

Email links carry their token in a URL fragment, keeping it out of HTTP access
logs. The React page reads the fragment and requires a POST confirmation, so a
mail scanner's GET does not consume the token. Verification lasts 24 hours;
reset and email change last 30 minutes; resends are cooled down for 60 seconds.
Token consumption is atomic. User-row locks precede token locks; login and
password updates share that lock to prevent old credentials creating a late
session after password reset. Password changes and resets revoke all human
sessions, while bot credentials remain independent until explicitly revoked.

The server cookie is HttpOnly, SameSite=Strict, and Secure in HTTPS deployments.
All state changes require an exact Origin match. Authenticated state changes also
require `X-CSRF-Token` from `/v1/me` or the login response. `ValidateCSRF` provides
the same check to the platform's endpoints. Browser WS upgrades must separately
validate Origin, authenticate the cookie and associate `SessionID` with their
connection; the `OnRevoke` callback must close these connections. Cookie values,
CSRF tokens and session metadata are never public profile fields.

## Routes

| Method and path | Body or result |
|---|---|
| POST `/v1/auth/register` | `{email,password,name,accept_terms}`; generic 202 |
| POST `/v1/auth/login` | `{email,password}` → `{user,csrf_token}` plus cookie |
| POST `/v1/auth/verify-email` | `{token}` |
| POST `/v1/auth/resend-verification` | `{email}`; generic 202 |
| POST `/v1/auth/forgot-password` | `{email}`; generic 202 |
| POST `/v1/auth/reset-password` | `{token,password}`; revokes sessions |
| POST `/v1/auth/logout` | Revokes current session |
| GET `/v1/me` | `{user,csrf_token}` |
| PATCH `/v1/me` | `{name}` |
| GET `/v1/me/sessions` | Current and other live devices |
| DELETE `/v1/me/sessions/{id}` | Revokes own selected device |
| DELETE `/v1/me/sessions/others` | Preserves current device |
| GET `/v1/me/email-delivery` | Own latest delivery state, never message body |
| POST `/v1/me/password` | `{current_password,new_password}` |
| POST `/v1/me/email-change/request` | `{email,password}` |
| POST `/v1/me/email-change/confirm` | `{token}`; revokes sessions |
| POST `/v1/me/delete-account` | `{password}`; anonymizes account |

Private `User` fields are `id,name,email,verified,role,status`. Do not expose that
structure from public lobby/profile endpoints. `PublicName` returns only the
display name (or `已注销用户`). Anonymous and unverified accounts cannot join tables
or administer bots; that authorization belongs to the platform. Restricted users
can authenticate for account recovery but must be denied game actions. Public
registration cannot set a role; operator bootstrap is a deployment CLI concern.

The platform implements `BeforeDeleteTx(ctx, tx, userID)` while the account row
is locked: reject active matches, revoke bot credentials and clear queue entries
in the same transaction as anonymization. All admission transactions acquire the
same account row lock. `OnDelete` is a post-commit notification for connections,
not the durable credential cleanup. Stable user IDs and match records are retained.
`OnRevoke(userID,sessionID,all)` runs after the database commit; connected players
must also re-check authorization on subsequent commands for crash-safe denial.

Rate limits apply per resolved client and hashed normalized account identifier.
`TrustedProxyCIDRs` is empty by default. If the immediate peer is trusted, the
X-Forwarded-For chain is traversed from right to left up to its first untrusted
hop. Untrusted clients cannot choose a bucket through forged headers. `ClientIP`
exports this same policy to other services. Limits are process-local for the
single-node deployment; the ingress should add connection/body limits. A scaled
deployment needs a shared
limiter before increasing replicas. Registration and mail requests use generic
responses for existing and absent accounts; only correct password authentication
reveals account verification/restriction state.

## Validation

`go test -race ./internal/auth` exercises hashing, malformed parameters, Origin
rejection and a real local SMTP protocol exchange. Set `TEST_DATABASE_URL` to run
the PostgreSQL lifecycle test. It creates and drops an isolated random schema,
then tests registration, encrypted outbox, unverified login, CSRF rejection,
concurrent single-use verification and reset, revoked sessions, device logout,
email change and account anonymization. No database means this integration test
is explicitly skipped; CI provides PostgreSQL and must not omit it.
