package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
)

// Statistics retain settled, non-card facts. They never decode a rule's opaque
// state or participant projections. Dimensions and identities are frozen at start.
const statisticsSchema = `
CREATE TABLE IF NOT EXISTS platform_stat_matches (
 match_id text PRIMARY KEY REFERENCES platform_matches(id),
 ruleset_id text NOT NULL, ruleset_version text NOT NULL, online_profile text NOT NULL,
 match_format text NOT NULL, mode text NOT NULL, clock_profile text NOT NULL,
 config_hash text NOT NULL, self_test boolean NOT NULL, queue_pool text NOT NULL
);
CREATE TABLE IF NOT EXISTS platform_stat_participants (
 match_id text NOT NULL REFERENCES platform_stat_matches(match_id), participant_id text NOT NULL,
 user_id text NOT NULL, bot_id text NOT NULL, participant_kind text NOT NULL, bot_version text NOT NULL,
 PRIMARY KEY(match_id,participant_id)
);
CREATE INDEX IF NOT EXISTS platform_stat_user ON platform_stat_participants(user_id,participant_kind);
CREATE INDEX IF NOT EXISTS platform_stat_bot ON platform_stat_participants(bot_id);
CREATE TABLE IF NOT EXISTS platform_stat_hands (
 match_id text NOT NULL, participant_id text NOT NULL, hand_index integer NOT NULL,
 net_points integer NOT NULL, won boolean NOT NULL, self_draw boolean NOT NULL,
 discard_loss boolean NOT NULL, nonflower_points integer,
 PRIMARY KEY(match_id,participant_id,hand_index),
 FOREIGN KEY(match_id,participant_id) REFERENCES platform_stat_participants(match_id,participant_id)
);
CREATE TABLE IF NOT EXISTS platform_stat_rankings (
 match_id text NOT NULL, participant_id text NOT NULL, rank integer NOT NULL,
 raw_score integer NOT NULL, standard_n integer, standard_d integer CHECK(standard_d>0),
 PRIMARY KEY(match_id,participant_id),
 FOREIGN KEY(match_id,participant_id) REFERENCES platform_stat_participants(match_id,participant_id)
);
CREATE TABLE IF NOT EXISTS platform_stat_decisions (
 match_id text NOT NULL, participant_id text NOT NULL, decision_id text NOT NULL,
 opened_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz,
 outcome text CHECK(outcome IN ('accepted','builtin','timeout','recovery')),
 latency_ms double precision,
 PRIMARY KEY(match_id,participant_id,decision_id),
 FOREIGN KEY(match_id,participant_id) REFERENCES platform_stat_participants(match_id,participant_id)
);
CREATE TABLE IF NOT EXISTS platform_stat_invalid_actions (
 match_id text NOT NULL, participant_id text NOT NULL, command_id text NOT NULL,
 code text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(match_id,participant_id,command_id),
 FOREIGN KEY(match_id,participant_id) REFERENCES platform_stat_participants(match_id,participant_id)
);`

func StatsMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, statisticsSchema)
	return err
}

func (s *Service) statisticsRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/public/statistics", s.publicStatistics)
	m.HandleFunc("GET /v1/me/statistics", s.myStatistics)
	m.HandleFunc("GET /v1/bots/{id}/statistics", s.botStatistics)
}

func (s *Service) freezeStatistics(ctx context.Context, tx pgx.Tx, m match) error {
	var mode, profile string
	var selfTest, human, queued bool
	var options json.RawMessage
	err := tx.QueryRow(ctx, `SELECT r.mode,r.online_profile,r.self_test,EXISTS(SELECT 1 FROM platform_seats WHERE room_id=r.id AND active AND kind='human'),COALESCE(m.config->'options','{}'::jsonb),r.queue_deadline IS NOT NULL FROM platform_rooms r JOIN platform_matches m ON m.room_id=r.id WHERE m.id=$1`, m.ID).Scan(&mode, &profile, &selfTest, &human, &options, &queued)
	if err != nil {
		return err
	}
	clock := "bot-2s-500ms-1s@1"
	if human {
		clock = "human-15s-4s-15s@1"
	}
	pool := "manual_room"
	if queued {
		pool = "public_human_queue"
		if mode == "bot_only" {
			pool = "public_bot_queue"
		}
	}
	// Identity, display names and secret entropy do not define a comparison pool.
	var canonicalOptions any
	if err = json.Unmarshal(options, &canonicalOptions); err != nil {
		return err
	}
	hash := sha256.Sum256(jsonBytes(map[string]any{"ruleset_id": m.RulesetID, "ruleset_version": m.RulesetVersion, "profile": profile, "format": m.Format, "mode": mode, "clock": clock, "options": canonicalOptions}))
	_, err = tx.Exec(ctx, `INSERT INTO platform_stat_matches(match_id,ruleset_id,ruleset_version,online_profile,match_format,mode,clock_profile,config_hash,self_test,queue_pool) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, m.ID, m.RulesetID, m.RulesetVersion, profile, m.Format, mode, clock, hex.EncodeToString(hash[:]), selfTest, pool)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO platform_stat_participants(match_id,participant_id,user_id,bot_id,participant_kind,bot_version) SELECT $1,participant_id,user_id,bot_id,kind,CASE WHEN kind='builtin' THEN builtin_strategy||'@1' ELSE bot_version END FROM platform_seats WHERE room_id=$2 AND active ON CONFLICT DO NOTHING`, m.ID, m.RoomID)
	}
	return err
}

func (s *Service) persistStatistics(ctx context.Context, tx pgx.Tx, m match, f rulesdk.Flow) error {
	for _, d := range f.Decisions {
		if _, err := tx.Exec(ctx, `INSERT INTO platform_stat_decisions(match_id,participant_id,decision_id) SELECT match_id,participant_id,$3 FROM platform_stat_participants WHERE match_id=$1 AND participant_id=$2 ON CONFLICT DO NOTHING`, m.ID, d.ParticipantID, d.ID); err != nil {
			return err
		}
	}
	if f.HandEnded && f.HandResult != nil {
		h := f.HandResult
		if h.HandIndex != f.HandIndex || h.HandIndex < 1 {
			return errors.New("invalid statistics hand index")
		}
		winners := map[string]rulesdk.Winner{}
		for _, w := range h.Winners {
			winners[w.ParticipantID] = w
		}
		for _, score := range h.Scores {
			w, won := winners[score.ParticipantID]
			if _, err := tx.Exec(ctx, `INSERT INTO platform_stat_hands(match_id,participant_id,hand_index,net_points,won,self_draw,discard_loss,nonflower_points) SELECT match_id,participant_id,$3,$4,$5,$6,$7,$8 FROM platform_stat_participants WHERE match_id=$1 AND participant_id=$2 ON CONFLICT DO NOTHING`, m.ID, score.ParticipantID, h.HandIndex, score.Delta, won, won && w.SelfDraw, score.ParticipantID == h.DiscarderID, w.NonFlowerPoints); err != nil {
				return err
			}
		}
	}
	if f.MatchEnded {
		for _, rank := range f.Rankings {
			var numerator, denominator *int
			if rank.StandardPoints != nil {
				if rank.StandardPoints.Denominator <= 0 {
					return errors.New("invalid statistics standard points denominator")
				}
				numerator, denominator = &rank.StandardPoints.Numerator, &rank.StandardPoints.Denominator
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform_stat_rankings(match_id,participant_id,rank,raw_score,standard_n,standard_d) SELECT match_id,participant_id,$3,$4,$5,$6 FROM platform_stat_participants WHERE match_id=$1 AND participant_id=$2 ON CONFLICT DO NOTHING`, m.ID, rank.ParticipantID, rank.Rank, rank.RawScore, numerator, denominator); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) recordDecision(ctx context.Context, tx pgx.Tx, m match, decisionID, pid, outcome string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE platform_stat_decisions SET resolved_at=$4,outcome=$5,latency_ms=GREATEST(0,EXTRACT(EPOCH FROM ($4::timestamptz-opened_at))*1000) WHERE match_id=$1 AND participant_id=$2 AND decision_id=$3 AND resolved_at IS NULL`, m.ID, pid, decisionID, at, outcome)
	return err
}

// Called only after authorization, window and legal-option checks established a
// genuine INVALID_OPTION attempt. Stale frames/auth failures do not poison stats.
// It does not acquire a match lock and survives rollback of a rejected command.
func (s *Service) recordInvalidAction(ctx context.Context, m match, pid, commandID, code string) error {
	if code != "INVALID_OPTION" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO platform_stat_invalid_actions(match_id,participant_id,command_id,code) SELECT match_id,participant_id,$3,$4 FROM platform_stat_participants WHERE match_id=$1 AND participant_id=$2 ON CONFLICT DO NOTHING`, m.ID, pid, commandID, code)
	return err
}

type statisticMetric struct {
	Value   *float64 `json:"value"`
	Samples int64    `json:"samples"`
}
type statisticGroup struct {
	Dimensions map[string]any             `json:"dimensions"`
	Comparable bool                       `json:"comparable"`
	Metrics    map[string]statisticMetric `json:"metrics"`
}

func metric(value float64, samples int64) statisticMetric {
	if samples == 0 {
		return statisticMetric{Samples: 0}
	}
	return statisticMetric{Value: &value, Samples: samples}
}
func metricRate(n, total int64) statisticMetric {
	if total == 0 {
		return metric(0, 0)
	}
	return metric(float64(n)/float64(total), total)
}
func optionalMetric(value *float64, samples int64) statisticMetric {
	return statisticMetric{Value: value, Samples: samples}
}

func (s *Service) publicStatistics(w http.ResponseWriter, r *http.Request) {
	s.statistics(w, r, "", "", false)
}
func (s *Service) myStatistics(w http.ResponseWriter, r *http.Request) {
	u, err := s.user(r, false)
	if err != nil {
		failure(w, err)
		return
	}
	s.statistics(w, r, u.ID, "", true)
}
func (s *Service) botStatistics(w http.ResponseWriter, r *http.Request) {
	u, err := s.user(r, false)
	if err != nil {
		failure(w, err)
		return
	}
	var own bool
	if err = s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM platform_bots WHERE id=$1 AND owner_id=$2)`, r.PathValue("id"), u.ID).Scan(&own); err != nil {
		failure(w, err)
		return
	}
	if !own {
		failure(w, api(404, "BOT_NOT_FOUND"))
		return
	}
	s.statistics(w, r, u.ID, r.PathValue("id"), true)
}

// Fields are an allowlist of SQL identifiers; no user string becomes SQL text.
func statisticsFilter(r *http.Request, uid, bid string) (string, []any, error) {
	where := []string{"true"}
	args := []any{}
	add := func(column string, value any) {
		args = append(args, value)
		where = append(where, column+"=$"+strconv.Itoa(len(args)))
	}
	if uid != "" {
		add("p.user_id", uid)
		if bid == "" {
			add("p.participant_kind", "human")
		}
	}
	if bid != "" {
		add("p.bot_id", bid)
	}
	for _, field := range []string{"ruleset_id", "ruleset_version", "online_profile", "match_format", "mode", "clock_profile", "config_hash", "bot_version", "status", "participant_kind", "queue_pool"} {
		value := r.URL.Query().Get(field)
		if len(value) > 200 {
			return "", nil, api(400, "INVALID_STATISTICS_FILTER")
		}
		if value != "" {
			prefix := "sm."
			if field == "bot_version" || field == "participant_kind" {
				prefix = "p."
			} else if field == "status" {
				prefix = "m."
			}
			add(prefix+field, value)
		}
	}
	for _, field := range []string{"self_test", "platform_interrupted"} {
		if r.URL.Query().Has(field) {
			value, err := strconv.ParseBool(r.URL.Query().Get(field))
			if err != nil {
				return "", nil, api(400, "INVALID_STATISTICS_FILTER")
			}
			column := "sm.self_test"
			if field == "platform_interrupted" {
				column = "m.interrupted"
			}
			add(column, value)
		}
	}
	return strings.Join(where, " AND "), args, nil
}

const statisticsBase = `SELECT p.*,m.status,m.interrupted,m.created_at,
 jsonb_build_object('ruleset_id',sm.ruleset_id,'ruleset_version',sm.ruleset_version,
 'online_profile',sm.online_profile,'match_format',sm.match_format,'mode',sm.mode,
 'clock_profile',sm.clock_profile,'config_hash',sm.config_hash,'bot_version',p.bot_version,'queue_pool',sm.queue_pool,
 'self_test',sm.self_test,'platform_interrupted',m.interrupted,'status',m.status,
 'trustee_used',EXISTS(SELECT 1 FROM platform_stat_decisions dd WHERE dd.match_id=p.match_id AND dd.outcome IN ('timeout','recovery')),
 'participant_kind',p.participant_kind) AS dimensions
 FROM platform_stat_participants p JOIN platform_stat_matches sm ON sm.match_id=p.match_id
 JOIN platform_matches m ON m.id=p.match_id WHERE `

func (s *Service) statistics(w http.ResponseWriter, r *http.Request, uid, bid string, private bool) {
	where, args, err := statisticsFilter(r, uid, bid)
	if err != nil {
		failure(w, err)
		return
	}
	base := statisticsBase + where
	groupArgs := append([]any(nil), args...)
	after := r.URL.Query().Get("group_after")
	if len(after) > 4000 {
		failure(w, api(400, "INVALID_CURSOR"))
		return
	}
	groupArgs = append(groupArgs, after)
	query := `WITH b AS (` + base + `),
 k AS (SELECT DISTINCT dimensions FROM b),
 h AS (SELECT dimensions,count(*) AS hands,count(*) FILTER(WHERE won) AS wins,
 count(*) FILTER(WHERE self_draw) AS selfdraws,count(*) FILTER(WHERE discard_loss) AS losses,
 avg(net_points) AS net,avg(nonflower_points) AS fan,count(nonflower_points) AS fan_samples
 FROM b JOIN platform_stat_hands h USING(match_id,participant_id) GROUP BY dimensions),
 d AS (SELECT dimensions,count(*) FILTER(WHERE outcome IS NOT NULL) AS decisions,
 count(*) FILTER(WHERE outcome='accepted') AS accepted,
 count(*) FILTER(WHERE outcome='timeout') AS timeouts,
 count(*) FILTER(WHERE outcome IN ('timeout','recovery')) AS trustee,
 count(*) FILTER(WHERE outcome IN ('accepted','builtin')) AS latency_samples,
 avg(latency_ms) FILTER(WHERE outcome IN ('accepted','builtin')) AS latency,
 percentile_cont(0.95) WITHIN GROUP(ORDER BY latency_ms) FILTER(WHERE outcome IN ('accepted','builtin')) AS p95
 FROM b JOIN platform_stat_decisions d USING(match_id,participant_id) GROUP BY dimensions),
 a AS (SELECT dimensions,count(*) AS invalid FROM b JOIN platform_stat_invalid_actions a USING(match_id,participant_id) GROUP BY dimensions),
 z AS (SELECT dimensions,count(*) AS matches,avg(rank) AS rank,avg(raw_score) AS raw,
 avg(standard_n::double precision/standard_d) FILTER(WHERE status='completed' AND NOT interrupted AND dimensions->>'match_format'='standard_16') AS standard,
 count(standard_n) FILTER(WHERE status='completed' AND NOT interrupted AND dimensions->>'match_format'='standard_16') AS standard_samples
 FROM b JOIN platform_stat_rankings z USING(match_id,participant_id) GROUP BY dimensions)
 SELECT k.dimensions,COALESCE(h.hands,0),COALESCE(h.wins,0),COALESCE(h.selfdraws,0),COALESCE(h.losses,0),h.net,h.fan,COALESCE(h.fan_samples,0),
 COALESCE(d.decisions,0),COALESCE(d.accepted,0),COALESCE(d.timeouts,0),COALESCE(d.trustee,0),COALESCE(d.latency_samples,0),d.latency,d.p95,COALESCE(a.invalid,0),
 COALESCE(z.matches,0),z.rank,z.raw,z.standard,COALESCE(z.standard_samples,0)
 FROM k LEFT JOIN h USING(dimensions) LEFT JOIN d USING(dimensions) LEFT JOIN a USING(dimensions) LEFT JOIN z USING(dimensions)
 WHERE k.dimensions::text>$` + strconv.Itoa(len(groupArgs)) + ` ORDER BY k.dimensions::text LIMIT 101`
	rows, err := s.pool.Query(r.Context(), query, groupArgs...)
	if err != nil {
		failure(w, err)
		return
	}
	groups := []statisticGroup{}
	nextGroup := ""
	lastDimension := ""
	for rows.Next() {
		if len(groups) == 100 {
			nextGroup = lastDimension
			break
		}
		var raw json.RawMessage
		var hands, wins, selfDraws, losses, fanSamples, decisions, accepted, timeouts, trustee, latencySamples, invalid, matches, standardSamples int64
		var net, fan, latency, p95, rank, score, standard *float64
		err = rows.Scan(&raw, &hands, &wins, &selfDraws, &losses, &net, &fan, &fanSamples, &decisions, &accepted, &timeouts, &trustee, &latencySamples, &latency, &p95, &invalid, &matches, &rank, &score, &standard, &standardSamples)
		if err != nil {
			break
		}
		var dimensions map[string]any
		if err = json.Unmarshal(raw, &dimensions); err != nil {
			break
		}
		// jsonb output is already its stable PostgreSQL text ordering for cursors.
		lastDimension = string(raw)
		comparable := dimensions["status"] == "completed" && dimensions["match_format"] == "standard_16" && dimensions["mode"] == "bot_only" && dimensions["queue_pool"] == "public_bot_queue" && dimensions["self_test"] == false && dimensions["platform_interrupted"] == false && dimensions["trustee_used"] == false
		groups = append(groups, statisticGroup{Dimensions: dimensions, Comparable: comparable, Metrics: map[string]statisticMetric{
			"completed_hands": metric(float64(hands), hands), "win_rate": metricRate(wins, hands), "discard_loss_rate": metricRate(losses, hands), "self_draw_rate": metricRate(selfDraws, hands),
			"average_net_points": optionalMetric(net, hands), "average_nonflower_points": optionalMetric(fan, fanSamples),
			"completed_matches": metric(float64(matches), matches), "average_rank": optionalMetric(rank, matches), "average_raw_score": optionalMetric(score, matches), "average_standard_points": optionalMetric(standard, standardSamples),
			"average_decision_ms": optionalMetric(latency, latencySamples), "p95_decision_ms": optionalMetric(p95, latencySamples), "illegal_action_rate": metricRate(invalid, accepted+invalid), "timeout_rate": metricRate(timeouts, decisions), "trustee_rate": metricRate(trustee, decisions),
		}})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		failure(w, err)
		return
	}
	response := map[string]any{"groups": groups, "next_group": nextGroup, "statistics_version": "settled-facts@1", "comparable_policy": "completed-standard16-public-bot-no-interruption-no-trustee"}
	if private {
		results, cursor, e := s.statisticsMatches(r, base, args)
		if e != nil {
			failure(w, e)
			return
		}
		response["matches"], response["next_before"] = results, cursor
	}
	write(w, 200, response)
}

func (s *Service) statisticsMatches(r *http.Request, base string, args []any) ([]any, string, error) {
	before := r.URL.Query().Get("before")
	if len(before) > 200 {
		return nil, "", api(400, "INVALID_CURSOR")
	}
	q := `WITH b AS (` + base + `) SELECT b.match_id,b.created_at,b.status,z.rank,z.raw_score,z.standard_n,z.standard_d FROM b JOIN platform_stat_rankings z USING(match_id,participant_id)`
	if before != "" {
		args = append(append([]any(nil), args...), before)
		q += ` WHERE (b.created_at,b.match_id)<(SELECT created_at,id FROM platform_matches WHERE id=$` + strconv.Itoa(len(args)) + `)`
	}
	q += ` ORDER BY b.created_at DESC,b.match_id DESC LIMIT 101`
	rows, err := s.pool.Query(r.Context(), q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := []any{}
	next, last := "", ""
	for rows.Next() {
		if len(result) == 100 {
			next = last
			break
		}
		var id, status string
		var at time.Time
		var rank, score int
		var numerator, denominator *int
		if err = rows.Scan(&id, &at, &status, &rank, &score, &numerator, &denominator); err != nil {
			return nil, "", err
		}
		var standard *rulesdk.Rational
		if numerator != nil && denominator != nil {
			standard = &rulesdk.Rational{Numerator: *numerator, Denominator: *denominator}
		}
		result = append(result, map[string]any{"match_id": id, "created_at": at, "status": status, "rank": rank, "raw_score": score, "standard_points": standard})
		last = id
	}
	return result, next, rows.Err()
}
