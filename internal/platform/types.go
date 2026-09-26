package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"golang.org/x/sync/singleflight"
)

type Config struct {
	ObserveTimerLag func(time.Duration) // optional nonblocking measurement hook; no state or private data
	Rules           map[string]rulesdk.Rule
	Auth            *auth.Service
	BaseURL         string
	TokenHashKey    []byte
}
type Service struct {
	publicMu        sync.Mutex
	publicSnapshots map[string]publicCacheEntry
	publicLoads     singleflight.Group
	pool            *pgxpool.Pool
	cfg             Config
	id              string
	stop            context.CancelFunc
	mu              sync.Mutex
	limits          map[string]rateEntry
	sockets         map[string]int
}
type rateEntry struct {
	start time.Time
	count int
}
type Room struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OwnerID        string `json:"owner_id"`
	Mode           string `json:"mode"`
	RulesetID      string `json:"ruleset_id"`
	RulesetVersion string `json:"ruleset_version"`
	Format         string `json:"match_format"`
	Profile        string `json:"online_profile"`
	Capacity       int    `json:"capacity"`
	InviteOnly     bool   `json:"invite_only"`
	SelfTest       bool   `json:"self_test"`
	Status         string `json:"status"`
	MatchID        string `json:"match_id"`
	Seats          []Seat `json:"seats"`
}
type Seat struct {
	ParticipantID string `json:"participant_id"`
	UserID        string `json:"-"`
	BotID         string `json:"bot_id,omitempty"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Order         int    `json:"seat_id"`
	Ready         bool   `json:"ready"`
	Epoch         int64  `json:"-"`
	Connected     bool   `json:"connected"`
	Leave         bool   `json:"leave_after_hand"`
}
type match struct {
	ID, RoomID, RulesetID, RulesetVersion, Format, Status, WindowID, Owner, Artifact string
	State                                                                            json.RawMessage
	Seq, OwnerEpoch                                                                  int64
	Deadline                                                                         *time.Time
	Choices                                                                          map[string]string
	Updated                                                                          time.Time
	Interrupted                                                                      bool
}
type Action struct {
	Type          string `json:"type"`
	Protocol      string `json:"protocol_version,omitempty"`
	MatchID       string `json:"match_id"`
	HandID        string `json:"hand_id"`
	ParticipantID string `json:"participant_id"`
	Seat          int    `json:"seat_id"`
	Assignment    int    `json:"seat_assignment_version"`
	Epoch         int64  `json:"control_epoch"`
	DecisionID    string `json:"decision_id"`
	WindowID      string `json:"window_id"`
	CommandID     string `json:"command_id"`
	OptionID      string `json:"option_id"`
}
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string       { return e.Code }
func api(status int, code string) error { return &APIError{status, code, code} }
func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	if pool == nil || len(cfg.Rules) == 0 || len(cfg.TokenHashKey) < 32 {
		return nil, errors.New("platform requires database, rules and 32-byte token hash key")
	}
	return &Service{pool: pool, cfg: cfg, id: id("node"), limits: map[string]rateEntry{}, sockets: map[string]int{}}, nil
}
func (s *Service) SetAuth(a *auth.Service) { s.cfg.Auth = a }
func id(prefix string) string {
	b := make([]byte, 18)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) hash(value string) string {
	h := hmac.New(sha256.New, s.cfg.TokenHashKey)
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}
func jsonBytes(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	var e *APIError
	if errors.As(err, &e) {
		write(w, e.Status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
	} else {
		write(w, 500, map[string]any{"error": map[string]string{"code": "INTERNAL_ERROR", "message": "请求未完成，请重试"}})
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return api(400, "INVALID_REQUEST")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return api(400, "INVALID_REQUEST")
	}
	return nil
}
func (s *Service) rule(id, version string) (rulesdk.Rule, error) {
	r := s.cfg.Rules[id+"@"+version]
	if r == nil {
		for _, v := range s.cfg.Rules {
			m := v.Manifest()
			if m.ID == id && m.Version == version {
				return v, nil
			}
		}
		return nil, api(400, "UNSUPPORTED_RULESET")
	}
	return r, nil
}
func (s *Service) user(r *http.Request, verified bool) (auth.User, error) {
	if s.cfg.Auth == nil {
		return auth.User{}, api(503, "AUTH_UNAVAILABLE")
	}
	u, e := s.cfg.Auth.Authenticate(r)
	if e != nil {
		return u, api(401, "AUTH_EXPIRED")
	}
	if verified && (!u.Verified || u.Status != "active") {
		return u, api(403, "ACCOUNT_NOT_ELIGIBLE")
	}
	return u, nil
}
func (s *Service) limit(key string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	e := s.limits[key]
	if now.Sub(e.start) > time.Minute {
		e = rateEntry{start: now}
	}
	e.count++
	s.limits[key] = e
	if len(s.limits) > 10000 {
		for k, v := range s.limits {
			if now.Sub(v.start) > time.Minute {
				delete(s.limits, k)
			}
		}
	}
	return e.count <= n
}
func (s *Service) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Auth == nil || !s.cfg.Auth.ValidateCSRF(r) {
			failure(w, api(403, "CSRF_FAILED"))
			return
		}
		if !s.limit("write:"+s.clientIP(r), 120) {
			failure(w, api(429, "RATE_LIMITED"))
			return
		}
		next(w, r)
	}
}
func handID(m string, i int) string { return m + "_hand_" + strconvI(i) }
func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
