# Statistics contract — settled-facts@1

Statistics contain settled results and decision timings, never a tile, seed, wall,
concealed hand, private projection or rejected payload. The host consumes only
`rulesdk.Flow.HandResult` and `Rankings`; it does not inspect MCR state.

## HTTP

* `GET /v1/public/statistics`: public aggregate cohorts; no participant identity or individual match results.
* `GET /v1/me/statistics`: authenticated user's human seats only.
* `GET /v1/bots/{id}/statistics`: authenticated Bot owner only; another owner gets `BOT_NOT_FOUND`.

All three return `groups`, `next_group`, `statistics_version`, and
`comparable_policy`. Each group has `dimensions`, `comparable`, and `metrics`.
Each metric has `{ "value": number | null, "samples": integer }`. Ratios are
0–1, timings are milliseconds. A missing sample is `null`, never a fabricated zero.
The two private endpoints also return `matches` and `next_before`.

Dimensions are `ruleset_id`, `ruleset_version`, `online_profile`, `match_format`,
`mode`, `clock_profile`, `config_hash`, `bot_version`, `participant_kind`,
`queue_pool`, `self_test`, `platform_interrupted`, `status`, and `trustee_used`.
All except `trustee_used` are optional exact-match query filters. The two boolean
filters accept `true`/`false`; malformed values produce `INVALID_STATISTICS_FILTER`.
Each response includes at most 100 groups. To retrieve the next page pass the
opaque `next_group` string as URL-encoded `group_after`, keeping other filters.
Private match results use a stable `(created_at, match_id)` descending cursor:
pass `next_before` as `before`. Results are live aggregates, not a point-in-time
snapshot across pages; a match completing between requests can change cohorts.

`config_hash` hashes canonical rule/version/profile/format/mode/clock/options;
it deliberately excludes participant identities, display names and secret entropy.
Participants, Bot version and dimensions are frozen at match start, independent of
later room edits or rematches. Built-ins carry `strategy@1` as their version.

## Metric definitions

| Metric | Value | Sample denominator |
|---|---|---|
| `completed_hands` | Completed participant-hand count, including draws | Same count |
| `win_rate` | Winning participant-hands / completed participant-hands | Completed participant-hands |
| `discard_loss_rate` | Discard/rob-kong losses / completed participant-hands | Completed participant-hands |
| `self_draw_rate` | Self-drawn wins / completed participant-hands | Completed participant-hands |
| `average_net_points` | Mean settled hand delta | Completed participant-hands |
| `average_nonflower_points` | Mean non-flower points, winners only | Wins with a rule-provided non-flower value |
| `completed_matches` | Participant-match results with final rankings | Same count |
| `average_rank` | Mean final rank | Final ranked participant-match results |
| `average_raw_score` | Mean final accumulated match score | Final ranked participant-match results |
| `average_standard_points` | Mean exact rational points converted to a display number | Completed, uninterrupted `standard_16` results with standard points |
| `average_decision_ms` | Mean time from new decision registration to valid acceptance | Accepted human/Bot decisions and built-in decisions |
| `p95_decision_ms` | PostgreSQL continuous 95th percentile of the same latency samples | Same accepted decision count |
| `illegal_action_rate` | Invalid legal-option attempts / (valid accepted commands + invalid attempts) | Accepted commands plus distinct invalid command IDs |
| `timeout_rate` | Deadline fallback decisions / resolved decisions | All resolved decisions |
| `trustee_rate` | Deadline or recovery fallback decisions / resolved decisions | All resolved decisions |

Public cohorts sum participant observations. For example, four participants in
one completed hand contribute four participant-hand samples, not four physical
hands. No league strength or ranking inference follows from these aggregates.
Individual private match results include `match_id`, `created_at`, `status`,
`rank`, `raw_score`, and exact `standard_points: {numerator, denominator} | null`.

A reaction's latency stops when the valid choice is durably recorded, not when
the shared response window eventually closes. Deadline/recovery latency is not
mixed into thinking-time samples; those decisions remain in timeout/trustee rates.
Consecutive flower decisions each start at their own registration; this metric
does not claim to measure a whole self-turn budget.
Identical command retries do not increase counts. Invalid attempts are recorded
only after identity, current window and control authorization are established;
stale/control/auth failures do not penalize strategy legality. Built-ins do not
submit network commands, so their decisions are excluded from the illegal-action
denominator. Rejected action payloads are never retained by this statistics table.

## Cohort boundaries and lifecycle

`comparable=true` requires a complete `standard_16` match from the public Bot
queue, with no self-test flag, platform interruption or trustee use. A manual
Bot room is distinct even if its speed matches the public queue. Any trustee
decision anywhere in a match puts its observations in `trustee_used=true`.
Short practice, mixed/human rooms, early endings and server interruptions retain
completed hands in their own groups. They cannot silently enter the comparable
public Bot cohort. This is a test-statistics eligibility label, not an official
competitive rating or evidence of stronger strategy.

Hand rows are unique by `(match, participant, hand_index)`, decision rows by
`(match, participant, decision_id)`, invalid attempts by command ID, and final
rank rows by `(match, participant)`. The host writes these facts in the same
transaction as the rule transition. A retried projection cannot double-count.
No unfinished hand receives a synthetic win, loss, score or rank when a server
aborts a match. Statistics begin when this schema is deployed; no private state
is retroactively parsed to invent missing historical decision metrics.

## Host hooks

`StatsMigrate` runs through platform migrations. `freezeStatistics` runs once
after inserting a match; `persistStatistics` runs with each accepted projection.
`recordDecision` records `accepted`, `builtin`, `timeout`, or `recovery` in the
transition transaction. `recordInvalidAction` records only an authorized
`INVALID_OPTION`, separately from the rejected command's rolled-back transaction.
The schema stores no emails or display names and needs no name-redaction rewrite.
