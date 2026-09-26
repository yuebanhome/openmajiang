package platform

// This is an opt-in, real-clock, real-PostgreSQL release gate. A short run is
// deliberately not accepted as the one-hour capacity result.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

type capacityMailer struct{}

func (capacityMailer) Send(context.Context, string, string, string) error { return nil }

type capacityUser struct {
	User         auth.User
	Secret, CSRF string
}
type capacityTarget struct {
	mu        sync.Mutex
	room      Room
	change    chan struct{}
	users     []capacityUser
	owner     capacityUser
	human     bool
	client    *http.Client
	watchers  atomic.Int64
	connected map[string]int
}

func (t *capacityTarget) current() (Room, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.room, t.change
}
func (t *capacityTarget) replace(room Room) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.room = room
	close(t.change)
	t.change = make(chan struct{})
}

func (t *capacityTarget) audience(roomID string, delta int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.connected == nil {
		t.connected = map[string]int{}
	}
	t.connected[roomID] += delta
	return t.connected[roomID]
}

type capacityMetrics struct {
	mu                               sync.Mutex
	measure                          atomic.Bool
	humans, spectators               atomic.Int64
	minimumHumans, minimumSpectators atomic.Int64
	acks, lags                       []int64
	errors                           []string
	commandErrors                    map[string]int
	frames, reconnects               int64
	peakHeap, peakRSS                uint64
	peakGoroutines, peakSockets      int
	peakPoolAcquired                 int32
	samples, belowPopulation         int
}

func (m *capacityMetrics) disconnected(human bool) {
	var n int64
	minimum := &m.minimumSpectators
	if human {
		n = m.humans.Add(-1)
		minimum = &m.minimumHumans
	} else {
		n = m.spectators.Add(-1)
	}
	if m.measure.Load() {
		for {
			old := minimum.Load()
			if n >= old || minimum.CompareAndSwap(old, n) {
				break
			}
		}
	}
}

func (m *capacityMetrics) failure(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errors) < 100 {
		m.errors = append(m.errors, err.Error())
	}
}
func (m *capacityMetrics) ack(d time.Duration) {
	if !m.measure.Load() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.acks) < 2_000_000 {
		m.acks = append(m.acks, int64(d))
	} else if len(m.errors) == 0 {
		m.errors = append(m.errors, "ACK sample safety bound exceeded")
	}
}
func (m *capacityMetrics) lag(d time.Duration) {
	if !m.measure.Load() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lags) < 2_000_000 {
		m.lags = append(m.lags, int64(d))
	} else if len(m.errors) == 0 {
		m.errors = append(m.errors, "timer sample safety bound exceeded")
	}
}
func capacityPercentile(values []int64, p float64) float64 {
	if len(values) == 0 {
		return -1
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	i := int(float64(len(values)-1) * p)
	return float64(values[i]) / float64(time.Millisecond)
}

// Distribution includes every recorded latency; no warm requests or slow
// samples inside the measurement interval are discarded.
func capacityDistribution(values []int64) map[string]any {
	result := map[string]any{"samples": len(values)}
	if len(values) == 0 {
		return result
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for name, p := range map[string]float64{"p50_ms": .5, "p90_ms": .9, "p95_ms": .95, "p99_ms": .99} {
		result[name] = float64(values[int(float64(len(values)-1)*p)]) / float64(time.Millisecond)
	}
	result["min_ms"] = float64(values[0]) / float64(time.Millisecond)
	result["max_ms"] = float64(values[len(values)-1]) / float64(time.Millisecond)
	bounds := []int64{10, 25, 50, 100, 250, 500, 1000}
	names := []string{"less_than_10_ms", "10_to_25_ms", "25_to_50_ms", "50_to_100_ms", "100_to_250_ms", "250_to_500_ms", "500_to_1000_ms", "at_least_1000_ms"}
	buckets := map[string]int{}
	for _, name := range names {
		buckets[name] = 0
	}
	over100 := 0
	for _, value := range values {
		bucket := len(bounds)
		for i, bound := range bounds {
			if value < bound*int64(time.Millisecond) {
				bucket = i
				break
			}
		}
		buckets[names[bucket]]++
		if value >= 100*int64(time.Millisecond) {
			over100++
		}
	}
	result["at_least_100_ms"] = over100
	result["histogram"] = buckets
	return result
}

type capacityReport struct {
	Passed                                    bool           `json:"passed"`
	StartedAt                                 time.Time      `json:"started_at"`
	RequestedSeconds                          int            `json:"requested_seconds"`
	MeasuredSeconds                           float64        `json:"measured_seconds"`
	ElapsedSeconds                            float64        `json:"elapsed_seconds"`
	Machine                                   map[string]any `json:"machine"`
	Population                                map[string]any `json:"population"`
	ACKSamples                                int            `json:"ack_samples"`
	ACKP95MS                                  float64        `json:"ack_p95_ms"`
	TimerSamples                              int            `json:"timer_samples"`
	TimerP99MS                                float64        `json:"adjudication_lag_p99_ms"`
	CompletedHands                            int64          `json:"completed_hands"`
	CompletedMatches                          int64          `json:"completed_matches"`
	Rematches                                 int64          `json:"rematches"`
	DBBytesBefore, DBBytesAfter               int64
	PeakHeapBytes, PeakRSSBytes               uint64
	PeakGoroutines, PeakSockets               int
	PopulationSamples, BelowPopulationSamples int
	SpectatorFrames                           int64 `json:"spectator_frames"`
	SlowConsumers, SlowConsumersReleased      int
	HotspotLimitRejected, IPLimitRejected     bool
	CPUSeconds                                float64           `json:"process_cpu_seconds_including_generator"`
	Pool                                      map[string]any    `json:"postgres_pool"`
	LatencyDistributions                      map[string]any    `json:"latency_distributions"`
	Profiles                                  map[string]string `json:"profiles,omitempty"`
	CommandErrors                             map[string]int    `json:"command_errors"`
	Errors                                    []string          `json:"errors"`
}

func capacityNewUser(t *testing.T, s *Service, name string) capacityUser {
	t.Helper()
	u := capacityUser{User: auth.User{ID: id("capacity_user"), Name: name, Verified: true, Status: "active"}, CSRF: id("csrf")}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	u.Secret = base64.RawURLEncoding.EncodeToString(b)
	digest := sha256.Sum256([]byte(u.Secret))
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'fixture-credential-not-used',true)`, u.User.ID, u.User.ID+"@example.invalid", u.User.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,secret_hash,csrf_token,expires_at) VALUES($1,$2,$3,$4,now()+interval '4 hours')`, id("capacity_session"), u.User.ID, hex.EncodeToString(digest[:]), u.CSRF); err != nil {
		t.Fatal(err)
	}
	return u
}
func capacityClient(ip net.IP, slow bool) *http.Client {
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, LocalAddr: &net.TCPAddr{IP: ip}}
	transport := &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32, IdleConnTimeout: 30 * time.Second, DisableCompression: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		c, e := dialer.DialContext(ctx, network, address)
		if e == nil && slow {
			if tcp, ok := c.(*net.TCPConn); ok {
				_ = tcp.SetReadBuffer(1024)
			}
		}
		return c, e
	}}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}
}
func capacityIP(group, index int) net.IP {
	return net.IPv4(127, byte(group), byte(index/250), byte(index%250+1))
}
func capacityRequest(ctx context.Context, client *http.Client, base, path string, u *capacityUser, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	if u != nil {
		req.AddCookie(&http.Cookie{Name: "omj_session", Value: u.Secret})
		req.Header.Set("X-CSRF-Token", u.CSRF)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s status %d: %s", path, res.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	_, err = io.Copy(io.Discard, res.Body)
	return err
}
func capacityPopulate(ctx context.Context, s *Service, target *capacityTarget, room Room) error {
	start := 0
	if target.human {
		start = 1
	}
	for i := start; i < 4; i++ {
		u := target.owner
		kind := "builtin"
		strategy := "random_legal"
		name := fmt.Sprintf("Builtin %d", i)
		if i%2 == 1 {
			strategy = "basic_heuristic"
		}
		if target.human {
			u = target.users[i]
			kind = "human"
			name = u.User.Name
		}
		_, err := s.pool.Exec(ctx, `INSERT INTO platform_seats(participant_id,room_id,user_id,name,kind,seat_order,ready,builtin_strategy,connected_until) VALUES($1,$2,$3,$4,$5,$6,true,$7,now()+interval '30 seconds')`, id("capacity_participant"), room.ID, u.User.ID, name, kind, i, strategy)
		if err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE platform_seats SET ready=true,controller='',controller_session='',connected_until=now()+interval '30 seconds' WHERE room_id=$1`, room.ID)
	return err
}
func capacityStart(ctx context.Context, s *Service, target *capacityTarget, base string, rematch bool) error {
	var room Room
	var oldRoomID string
	if rematch {
		old, _ := target.current()
		oldRoomID = old.ID
		var response struct {
			Room Room `json:"room"`
		}
		if err := capacityRequest(ctx, target.client, base, "/v1/rooms/"+old.ID+"/rematch", &target.owner, &response); err != nil {
			return err
		}
		room = response.Room
	} else {
		mode := "bot_only"
		if target.human {
			mode = "human_only"
		}
		created, _, err := s.create(ctx, target.owner.User, createRequest{Name: "Capacity standard 16", Mode: mode, RuleID: "openmajiang.mcr", RuleVersion: "1.0.0", Format: "standard_16", Capacity: 4})
		if err != nil {
			return err
		}
		room = created
	}
	if err := capacityPopulate(ctx, s, target, room); err != nil {
		return err
	}
	if err := capacityRequest(ctx, target.client, base, "/v1/rooms/"+room.ID+"/start", &target.owner, nil); err != nil {
		return err
	}
	loaded, err := loadRoom(ctx, s.pool, room.ID)
	if err != nil {
		return err
	}
	target.replace(loaded)
	if rematch {
		// Migrate one table at a time. Every healthy viewer keeps reading the old
		// socket until its replacement has authenticated and produced a valid
		// public snapshot. This bounds temporary overlap even at the hot table.
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			current := target.audience(loaded.ID, 0)
			s.mu.Lock()
			oldSockets := s.sockets["watch-room:"+oldRoomID]
			s.mu.Unlock()
			if int64(current) >= target.watchers.Load() && oldSockets == 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("rematch audience did not migrate: room=%s connected=%d expected=%d", loaded.ID, current, target.watchers.Load())
			case <-ticker.C:
			}
		}
	}
	return nil
}

func capacityTicket(ctx context.Context, client *http.Client, base, rid string) (string, error) {
	var response struct {
		Ticket string `json:"ticket"`
	}
	err := capacityRequest(ctx, client, base, "/v1/public/rooms/"+rid+"/spectator-ticket", nil, &response)
	if err == nil && response.Ticket == "" {
		err = fmt.Errorf("missing anonymous ticket")
	}
	return response.Ticket, err
}
func capacityDial(ctx context.Context, client *http.Client, base, path string, user *capacityUser) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	if user != nil {
		headers.Set("Cookie", (&http.Cookie{Name: "omj_session", Value: user.Secret}).String())
	}
	c, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+path, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers})
	if err == nil {
		c.SetReadLimit(256 << 10)
	}
	return c, res, err
}
func capacityPublicCheck(raw []byte) error {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	for _, key := range []string{"decision", "legal_actions", "control_token", "recorded"} {
		if _, ok := message[key]; ok {
			return fmt.Errorf("private outer field %s in spectator frame", key)
		}
	}
	var typ string
	_ = json.Unmarshal(message["type"], &typ)
	if typ == "error" || typ == "decision_request" || typ == "command_ack" {
		return fmt.Errorf("unexpected spectator frame %s", typ)
	}
	view, ok := message["view"]
	if !ok {
		if typ == "heartbeat" || typ == "pong" {
			return nil
		}
		return fmt.Errorf("spectator frame %s has no view", typ)
	}
	if bytes.Equal(view, []byte("null")) {
		return fmt.Errorf("active spectator has no table view")
	}
	if _, err := ValidateSpectator(view); err != nil {
		return fmt.Errorf("spectator privacy boundary: %w", err)
	}
	return nil
}

// A reader decrements the live count as soon as it stops, including during a
// rematch handshake. Intentional replacement closes the old socket only after
// the new socket is fully established; no measurement samples are filtered.
type capacityConnection struct {
	conn        *websocket.Conn
	done        chan error
	intentional atomic.Bool
}

func capacityConnected(c *websocket.Conn, human bool, m *capacityMetrics) *capacityConnection {
	if human {
		m.humans.Add(1)
	} else {
		m.spectators.Add(1)
	}
	return &capacityConnection{conn: c, done: make(chan error, 1)}
}
func (c *capacityConnection) close() {
	if c != nil {
		c.intentional.Store(true)
		c.conn.CloseNow()
	}
}
func (c *capacityConnection) finish(ctx context.Context, human bool, m *capacityMetrics, err error) {
	m.disconnected(human)
	if err != nil && ctx.Err() == nil && !c.intentional.Load() {
		m.failure(fmt.Errorf("healthy connection disconnected (player=%v): %w", human, err))
	}
	c.conn.CloseNow()
	c.done <- err
}
func capacitySpectator(ctx context.Context, target *capacityTarget, client *http.Client, base string, m *capacityMetrics) {
	ctx = pprof.WithLabels(ctx, pprof.Labels("capacity_component", "spectator_generator"))
	pprof.SetGoroutineLabels(ctx)
	defer pprof.SetGoroutineLabels(context.Background())
	var retiring *capacityConnection
	defer func() { retiring.close() }()
	for ctx.Err() == nil {
		room, changed := target.current()
		ticket, err := capacityTicket(ctx, client, base, room.ID)
		if err != nil {
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		c, _, err := capacityDial(ctx, client, base, "/v1/ws/spectators?room_id="+room.ID, nil)
		if err != nil {
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		if err = c.Write(ctx, websocket.MessageText, jsonBytes(map[string]string{"type": "authenticate", "ticket": ticket})); err != nil {
			c.CloseNow()
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		_, raw, err := c.Read(ctx)
		if err == nil {
			err = capacityPublicCheck(raw)
		}
		if err != nil {
			c.CloseNow()
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		active := capacityConnected(c, false, m)
		target.audience(room.ID, 1)
		retiring.close()
		retiring = nil
		go func() {
			var readerErr error
			defer func() {
				target.audience(room.ID, -1)
				active.finish(ctx, false, m, readerErr)
			}()
			for {
				_, raw, err := c.Read(ctx)
				if err != nil {
					readerErr = err
					return
				}
				if err = capacityPublicCheck(raw); err != nil {
					readerErr = err
					return
				}
				m.mu.Lock()
				m.frames++
				m.mu.Unlock()
			}
		}()
		select {
		case <-ctx.Done():
			active.close()
			<-active.done
			return
		case <-changed:
			retiring = active
			m.mu.Lock()
			m.reconnects++
			m.mu.Unlock()
		case <-active.done:
			return
		}
	}
}
func capacityPlayer(ctx context.Context, target *capacityTarget, user capacityUser, client *http.Client, base string, m *capacityMetrics) {
	ctx = pprof.WithLabels(ctx, pprof.Labels("capacity_component", "player_generator"))
	pprof.SetGoroutineLabels(ctx)
	defer pprof.SetGoroutineLabels(context.Background())
	var retiring *capacityConnection
	defer func() { retiring.close() }()
	for ctx.Err() == nil {
		room, changed := target.current()
		c, _, err := capacityDial(ctx, client, base, "/v1/ws/players?room_id="+room.ID, &user)
		if err != nil {
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		_, grantRaw, err := c.Read(ctx)
		var grant struct {
			Type string `json:"type"`
		}
		if err == nil {
			err = json.Unmarshal(grantRaw, &grant)
		}
		if err == nil && grant.Type != "control_granted" {
			err = fmt.Errorf("capacity player did not acquire control: %s", grant.Type)
		}
		if err != nil {
			c.CloseNow()
			if ctx.Err() == nil {
				m.failure(err)
			}
			return
		}
		active := capacityConnected(c, true, m)
		retiring.close()
		retiring = nil
		go func() {
			var readerErr error
			defer func() { active.finish(ctx, true, m, readerErr) }()
			pending := map[string]time.Time{}
			seen := map[string]bool{}
			drawn := ""
			for {
				_, raw, err := c.Read(ctx)
				if err != nil {
					readerErr = err
					return
				}
				var frame struct {
					Type      string `json:"type"`
					CommandID string `json:"command_id"`
					Error     struct {
						Code string `json:"code"`
					} `json:"error"`
					View struct {
						DrawnID string `json:"drawn_tile_id"`
					} `json:"view"`
				}
				if err = json.Unmarshal(raw, &frame); err != nil {
					readerErr = err
					return
				}
				switch frame.Type {
				case "snapshot":
					drawn = frame.View.DrawnID
				case "control_readonly":
					readerErr = fmt.Errorf("capacity player failed to obtain controller")
					return
				case "decision_request":
					var decision struct {
						Action
						Options []rulesdk.Option `json:"legal_actions"`
					}
					if err = json.Unmarshal(raw, &decision); err != nil {
						readerErr = err
						return
					}
					if seen[decision.DecisionID] {
						continue
					}
					seen[decision.DecisionID] = true
					o := capacityOption(decision.Options, drawn)
					if o.ID == "" {
						readerErr = fmt.Errorf("empty legal options")
						return
					}
					a := decision.Action
					a.Type = "submit_action"
					a.CommandID = id("capacity_command")
					a.OptionID = o.ID
					pending[a.CommandID] = time.Now()
					if err = c.Write(ctx, websocket.MessageText, jsonBytes(a)); err != nil {
						readerErr = err
						return
					}
				case "command_ack":
					if started, ok := pending[frame.CommandID]; ok {
						m.ack(time.Since(started))
						delete(pending, frame.CommandID)
					}
				case "command_error":
					delete(pending, frame.CommandID)
					if m.measure.Load() {
						m.mu.Lock()
						m.commandErrors[frame.Error.Code]++
						m.mu.Unlock()
						if frame.Error.Code != "DECISION_CLOSED" {
							readerErr = fmt.Errorf("valid protocol action rejected: %s", frame.Error.Code)
							return
						}
					}
				case "error":
					readerErr = fmt.Errorf("player stream error: %s", frame.Error.Code)
					return
				}
			}
		}()
		select {
		case <-ctx.Done():
			active.close()
			<-active.done
			return
		case <-changed:
			retiring = active
		case <-active.done:
			return
		}
	}
}
func capacityOption(options []rulesdk.Option, drawn string) rulesdk.Option {
	for _, typ := range []string{"hu", "replace_flower", "pass"} {
		for _, o := range options {
			if o.Type == typ {
				return o
			}
		}
	}
	for _, o := range options {
		if o.Type == "discard" && o.TileID == drawn {
			return o
		}
	}
	for _, o := range options {
		if o.Type == "discard" {
			return o
		}
	}
	if len(options) > 0 {
		return options[0]
	}
	return rulesdk.Option{}
}

func capacitySlow(ctx context.Context, client *http.Client, base, rid string) (*websocket.Conn, error) {
	ticket, err := capacityTicket(ctx, client, base, rid)
	if err != nil {
		return nil, err
	}
	c, _, err := capacityDial(ctx, client, base, "/v1/ws/spectators?room_id="+rid, nil)
	if err != nil {
		return nil, err
	}
	if err = c.Write(ctx, websocket.MessageText, jsonBytes(map[string]string{"type": "authenticate", "ticket": ticket})); err != nil {
		c.CloseNow()
		return nil, err
	}
	_, raw, err := c.Read(ctx)
	if err == nil {
		err = capacityPublicCheck(raw)
	}
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	return c, nil
}
func capacityCPU() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
func capacityMachine() map[string]any {
	info := map[string]any{"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "measurement_layout": "server and WebSocket load generators in the same process; PostgreSQL is separate", "human_driver": "80 authenticated WS clients choose only server legal options; 120 builtins run the normal Host controller", "network_source": "real net.Dialer.LocalAddr addresses in Linux 127/8; no forwarded-IP headers"}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				info["cpu_model"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && (fields[0] == "MemTotal:" || fields[0] == "MemAvailable:") {
				n, _ := strconv.ParseUint(fields[1], 10, 64)
				info[strings.TrimSuffix(fields[0], ":")+"_bytes"] = n * 1024
			}
		}
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(".", &disk) == nil {
		info["filesystem_available_bytes"] = disk.Bavail * uint64(disk.Bsize)
	}
	return info
}

func TestCapacityOneHour(t *testing.T) {
	value := os.Getenv("OMJ_CAPACITY_SECONDS")
	if value == "" {
		t.Skip("set OMJ_CAPACITY_SECONDS=3600 and TEST_DATABASE_URL for the one-hour capacity release gate")
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 3600 {
		t.Fatal("the capacity acceptance gate requires at least 3600 measured seconds")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("this gate requires Linux real 127/8 source addresses")
	}
	rule := mcr.New()
	s := recoveryService(t, rule)
	metrics := &capacityMetrics{commandErrors: map[string]int{}}
	s.cfg.ObserveTimerLag = metrics.lag
	report := capacityReport{RequestedSeconds: seconds, Machine: capacityMachine(), Population: map[string]any{"human_tables": 20, "bot_tables": 30, "seats": 200, "player_websockets": 80, "builtin_controllers": 120, "healthy_spectators_launched": 1100, "required_minimum_spectators": 1000, "rematch_reconnection_reserve": 100, "rematch_handover": "replacement authenticates before old connection closes; per-IP baseline <=16; table migrations serialized", "hotspot_spectators": 500, "clock": "unaltered standard_16 human/Bot clocks"}, SlowConsumers: 10}
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	base := "http://" + server.Listener.Addr().String()
	authService, err := auth.New(s.pool, auth.Config{BaseURL: base, Mailer: capacityMailer{}, MailEncryptionKey: bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(authService)
	s.cfg.BaseURL = base
	s.RegisterRoutes(mux)
	authService.RegisterRoutes(mux)
	server.Start()
	var wg sync.WaitGroup
	var slowSockets []*websocket.Conn
	var clients []*http.Client
	cpuStart := capacityCPU()
	var measuredStart time.Time
	poolBaseline := s.pool.Stat()
	var cpuProfile *os.File
	profilePrefix := os.Getenv("OMJ_CAPACITY_PROFILE_PREFIX")
	defer func() {
		metrics.measure.Store(false)
		// Stop while the load is still present, so heap samples describe the
		// measured workload rather than an already dismantled test server.
		if cpuProfile != nil {
			pprof.StopCPUProfile()
			if err := cpuProfile.Close(); err != nil {
				t.Errorf("close CPU profile: %v", err)
			}
			heap, err := os.Create(profilePrefix + "-heap.pprof")
			if err == nil {
				err = pprof.WriteHeapProfile(heap)
				closeErr := heap.Close()
				if err == nil {
					err = closeErr
				}
			}
			if err != nil {
				t.Errorf("write heap profile: %v", err)
			}
		}
		poolFinal := s.pool.Stat()
		report.Pool = map[string]any{
			"max_connections": poolFinal.MaxConns(), "total_connections": poolFinal.TotalConns(),
			"acquisitions":               poolFinal.AcquireCount() - poolBaseline.AcquireCount(),
			"acquire_seconds":            (poolFinal.AcquireDuration() - poolBaseline.AcquireDuration()).Seconds(),
			"empty_acquisitions":         poolFinal.EmptyAcquireCount() - poolBaseline.EmptyAcquireCount(),
			"empty_acquire_wait_seconds": (poolFinal.EmptyAcquireWaitTime() - poolBaseline.EmptyAcquireWaitTime()).Seconds(),
			"cancelled_acquisitions":     poolFinal.CanceledAcquireCount() - poolBaseline.CanceledAcquireCount(),
		}
		cancel()
		for _, c := range slowSockets {
			c.CloseNow()
		}
		wg.Wait()
		server.Close()
		for _, c := range clients {
			c.CloseIdleConnections()
		}
		if !measuredStart.IsZero() && report.MeasuredSeconds == 0 {
			report.MeasuredSeconds = time.Since(measuredStart).Seconds()
		}
		report.ElapsedSeconds = report.MeasuredSeconds
		report.Population["minimum_live_spectators"] = metrics.minimumSpectators.Load()
		report.Population["minimum_live_player_websockets"] = metrics.minimumHumans.Load()
		if report.CPUSeconds == 0 {
			report.CPUSeconds = capacityCPU() - cpuStart
		}
		metrics.mu.Lock()
		report.ACKSamples = len(metrics.acks)
		report.ACKP95MS = capacityPercentile(metrics.acks, .95)
		report.TimerSamples = len(metrics.lags)
		report.TimerP99MS = capacityPercentile(metrics.lags, .99)
		report.LatencyDistributions = map[string]any{"ack": capacityDistribution(metrics.acks), "adjudication_lag": capacityDistribution(metrics.lags)}
		report.Pool["peak_acquired_connections"] = metrics.peakPoolAcquired
		report.Errors = append(report.Errors, metrics.errors...)
		report.CommandErrors = metrics.commandErrors
		report.PeakHeapBytes = metrics.peakHeap
		report.PeakRSSBytes = metrics.peakRSS
		report.PeakGoroutines = metrics.peakGoroutines
		report.PeakSockets = metrics.peakSockets
		report.PopulationSamples = metrics.samples
		report.BelowPopulationSamples = metrics.belowPopulation
		report.SpectatorFrames = metrics.frames
		metrics.mu.Unlock()
		_ = s.pool.QueryRow(context.Background(), `SELECT pg_database_size(current_database())`).Scan(&report.DBBytesAfter)
		path := os.Getenv("OMJ_CAPACITY_REPORT")
		if path == "" {
			path = "test-results/capacity-report.json"
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		t.Logf("capacity report JSON:\n%s", b)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
			if err = os.WriteFile(path, b, 0644); err != nil {
				t.Errorf("write capacity report: %v", err)
			}
		} else {
			t.Errorf("create capacity report directory: %v", err)
		}
		t.Logf("capacity report=%s elapsed=%.1fs ACKp95=%.3fms timerp99=%.3fms hands=%d passed=%v", path, report.MeasuredSeconds, report.ACKP95MS, report.TimerP99MS, report.CompletedHands, report.Passed)
	}()
	_ = s.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&report.DBBytesBefore)
	targets := make([]*capacityTarget, 50)
	for i := range targets {
		target := &capacityTarget{human: i < 20, change: make(chan struct{})}
		if target.human {
			for seat := 0; seat < 4; seat++ {
				target.users = append(target.users, capacityNewUser(t, s, fmt.Sprintf("Capacity human %d/%d", i, seat)))
			}
			target.owner = target.users[0]
		} else {
			target.owner = capacityNewUser(t, s, fmt.Sprintf("Capacity bot owner %d", i))
		}
		target.client = capacityClient(capacityIP(10, i), false)
		clients = append(clients, target.client)
		targets[i] = target
		if err = capacityStart(ctx, s, target, base, false); err != nil {
			t.Fatal(err)
		}
	}
	wg.Add(1)
	go func() { defer wg.Done(); s.Run(ctx) }()
	for i := 0; i < 80; i++ {
		target := targets[i/4]
		user := target.users[i%4]
		client := capacityClient(capacityIP(20, i/16), false)
		clients = append(clients, client)
		wg.Add(1)
		go func() { defer wg.Done(); capacityPlayer(ctx, target, user, client, base, metrics) }()
	}
	for i := 0; i < 1100; i++ {
		target := targets[0]
		if i >= 490 {
			target = targets[1+(i-490)%49]
		}
		target.watchers.Add(1)
		client := capacityClient(capacityIP(30, i/16), false)
		clients = append(clients, client)
		wg.Add(1)
		go func() { defer wg.Done(); capacitySpectator(ctx, target, client, base, metrics) }()
	}
	startup := time.NewTimer(2 * time.Minute)
	defer startup.Stop()
	for metrics.humans.Load() != 80 || metrics.spectators.Load() != 1100 {
		metrics.mu.Lock()
		failures := len(metrics.errors)
		metrics.mu.Unlock()
		if failures > 0 {
			t.Fatal("protocol load failed during startup; see report")
		}
		select {
		case <-startup.C:
			t.Fatalf("population not reached: humans=%d spectators=%d", metrics.humans.Load(), metrics.spectators.Load())
		case <-time.After(100 * time.Millisecond):
		}
	}
	// The hotspot combines 490 healthy viewers with ten genuine slow readers.
	hot, _ := targets[0].current()
	for i := 0; i < 10; i++ {
		client := capacityClient(capacityIP(50, i), true)
		clients = append(clients, client)
		c, err := capacitySlow(ctx, client, base, hot.ID)
		if err != nil {
			t.Fatal(err)
		}
		slowSockets = append(slowSockets, c)
	}
	// The next upgrade must be refused at the exact 500-viewer room bound.
	extraClient := capacityClient(capacityIP(40, 0), false)
	clients = append(clients, extraClient)
	c, res, dialErr := capacityDial(ctx, extraClient, base, "/v1/ws/spectators?room_id="+hot.ID, nil)
	if c != nil {
		c.CloseNow()
	}
	report.HotspotLimitRejected = dialErr != nil && res != nil && res.StatusCode == 429
	if !report.HotspotLimitRejected {
		t.Fatal("501st hotspot connection was not rejected")
	}
	// Sixteen healthy clients already use this genuine source IP. Sixteen more
	// are accepted, and the 33rd must be rejected independently of room limits.
	other, _ := targets[49].current()
	ipClient := capacityClient(capacityIP(30, 0), false)
	clients = append(clients, ipClient)
	ephemeral := []*websocket.Conn{}
	for i := 0; i < 16; i++ {
		c, err := capacitySlow(ctx, ipClient, base, other.ID)
		if err != nil {
			t.Fatal(err)
		}
		ephemeral = append(ephemeral, c)
	}
	c, res, dialErr = capacityDial(ctx, ipClient, base, "/v1/ws/spectators?room_id="+other.ID, nil)
	if c != nil {
		c.CloseNow()
	}
	report.IPLimitRejected = dialErr != nil && res != nil && res.StatusCode == 429
	for _, c := range ephemeral {
		c.CloseNow()
	}
	if !report.IPLimitRejected {
		t.Fatal("33rd source-IP connection was not rejected")
	}
	replacements := make([]*http.Client, 10)
	for i := range replacements {
		replacements[i] = capacityClient(capacityIP(51, i), false)
		clients = append(clients, replacements[i])
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		replaced := make([]bool, 10)
		remaining := 10
		for remaining > 0 {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for i := range replaced {
					if replaced[i] {
						continue
					}
					s.mu.Lock()
					released := s.sockets["ip:"+capacityIP(50, i).String()] == 0
					s.mu.Unlock()
					if released {
						replaced[i] = true
						remaining--
						client := replacements[i]
						targets[0].watchers.Add(1)
						wg.Add(1)
						go func() { defer wg.Done(); capacitySpectator(ctx, targets[0], client, base, metrics) }()
					}
				}
			}
		}
	}()
	// Warm up caches/connection pools before the measurement clock starts.
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		t.Fatal("warmup interrupted")
	}
	if profilePrefix != "" {
		if err := os.MkdirAll(filepath.Dir(profilePrefix), 0755); err != nil {
			t.Fatal(err)
		}
		cpuProfile, err = os.Create(profilePrefix + "-cpu.pprof")
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(cpuProfile); err != nil {
			_ = cpuProfile.Close()
			cpuProfile = nil
			t.Fatal(err)
		}
		report.Profiles = map[string]string{"cpu": profilePrefix + "-cpu.pprof", "heap": profilePrefix + "-heap.pprof", "scope": "server and load generator; profile labels distinguish generator player/spectator loops"}
	}
	measuredStart = time.Now()
	poolBaseline = s.pool.Stat()
	lastProgress := measuredStart
	cpuStart = capacityCPU()
	report.StartedAt = measuredStart.UTC()
	metrics.minimumHumans.Store(metrics.humans.Load())
	metrics.minimumSpectators.Store(metrics.spectators.Load())
	metrics.measure.Store(true)
	deadline := time.NewTimer(time.Duration(seconds) * time.Second)
	defer deadline.Stop()
	sample := time.NewTicker(time.Second)
	defer sample.Stop()
	slowCheck := time.NewTimer(5 * time.Minute)
	defer slowCheck.Stop()
	handBaseline := int64(0)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT DISTINCT match_id,hand_index FROM platform_views WHERE participant_id='' AND view->>'phase' IN ('intermission','ended')) q`).Scan(&handBaseline)
	for {
		select {
		case <-deadline.C:
			metrics.measure.Store(false)
			report.MeasuredSeconds = time.Since(measuredStart).Seconds()
			report.CPUSeconds = capacityCPU() - cpuStart
			var totalHands int64
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT DISTINCT match_id,hand_index FROM platform_views WHERE participant_id='' AND view->>'phase' IN ('intermission','ended')) q`).Scan(&totalHands); err != nil {
				t.Fatal(err)
			}
			report.CompletedHands = totalHands - handBaseline
			_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_matches WHERE status='completed'`).Scan(&report.CompletedMatches)
			metrics.mu.Lock()
			ackP95 := capacityPercentile(metrics.acks, .95)
			lagP99 := capacityPercentile(metrics.lags, .99)
			failures := len(metrics.errors)
			missing := metrics.belowPopulation
			samples := metrics.samples
			metrics.mu.Unlock()
			if failures > 0 || report.CompletedHands == 0 || ackP95 < 0 || ackP95 >= 100 || lagP99 < 0 || lagP99 >= 100 || report.SlowConsumersReleased != 10 || missing > 0 || metrics.minimumSpectators.Load() < 1000 || metrics.minimumHumans.Load() < 80 {
				t.Fatalf("capacity acceptance failed: errors=%d hands=%d ACKp95=%.3fms timerp99=%.3fms slow released=%d population dips=%d/%d", failures, report.CompletedHands, ackP95, lagP99, report.SlowConsumersReleased, missing, samples)
			}
			report.Passed = true
			return
		case <-slowCheck.C:
			s.mu.Lock()
			released := 0
			for i := 0; i < 10; i++ {
				if s.sockets["ip:"+capacityIP(50, i).String()] == 0 {
					released++
				}
			}
			s.mu.Unlock()
			report.SlowConsumersReleased = released
			if released != 10 {
				t.Fatal("slow consumers were not released by bounded server writes within five minutes")
			}
		case <-sample.C:
			poolStats := s.pool.Stat()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			var usage syscall.Rusage
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
			s.mu.Lock()
			sockets := s.sockets["all"]
			s.mu.Unlock()
			metrics.mu.Lock()
			metrics.samples++
			if poolStats.AcquiredConns() > metrics.peakPoolAcquired {
				metrics.peakPoolAcquired = poolStats.AcquiredConns()
			}
			if time.Since(lastProgress) >= time.Minute {
				lastProgress = time.Now()
				t.Logf("capacity progress elapsed=%.1fs humans=%d spectators=%d ACKsamples=%d timersamples=%d pool=%d/%d empty_wait=%.3fs acquisitions=%d cpu=%.3fs", time.Since(measuredStart).Seconds(), metrics.humans.Load(), metrics.spectators.Load(), len(metrics.acks), len(metrics.lags), poolStats.AcquiredConns(), poolStats.MaxConns(), (poolStats.EmptyAcquireWaitTime() - poolBaseline.EmptyAcquireWaitTime()).Seconds(), poolStats.AcquireCount()-poolBaseline.AcquireCount(), capacityCPU()-cpuStart)
			}
			if metrics.humans.Load() < 80 || metrics.spectators.Load() < 1000 {
				metrics.belowPopulation++
			}
			if mem.HeapAlloc > metrics.peakHeap {
				metrics.peakHeap = mem.HeapAlloc
			}
			if rss := uint64(usage.Maxrss) * 1024; rss > metrics.peakRSS {
				metrics.peakRSS = rss
			}
			if n := runtime.NumGoroutine(); n > metrics.peakGoroutines {
				metrics.peakGoroutines = n
			}
			if sockets > metrics.peakSockets {
				metrics.peakSockets = sockets
			}
			failures := len(metrics.errors)
			metrics.mu.Unlock()
			if failures > 0 {
				t.Fatal("capacity protocol/privacy error; see report")
			}
			if sockets > 2048 {
				t.Fatal("global socket safety bound exceeded")
			}
			for _, target := range targets {
				room, _ := target.current()
				var status string
				if err = s.pool.QueryRow(ctx, `SELECT status FROM platform_rooms WHERE id=$1`, room.ID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status == "ended" {
					if err = capacityStart(ctx, s, target, base, true); err != nil {
						t.Fatal(err)
					}
					report.Rematches++
				}
			}
		}
	}
}

// This short protocol test validates the load generator's handover itself. It
// does not substitute for the PostgreSQL one-hour capacity acceptance gate.
func TestCapacityDriverRematchPreservesPopulation(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(fmt.Sprintf("human=%v", human), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			newArrived, allowNew, oldClosed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/spectator-ticket") {
					write(w, http.StatusOK, map[string]string{"ticket": "driver-fixture"})
					return
				}
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				roomID := r.URL.Query().Get("room_id")
				if roomID == "old" {
					defer close(oldClosed)
				}
				if !human {
					if _, _, err = c.Read(ctx); err != nil {
						return
					}
				}
				if roomID == "new" {
					close(newArrived)
					select {
					case <-allowNew:
					case <-ctx.Done():
						return
					}
				}
				frame := jsonBytes(map[string]any{"type": "spectator_snapshot", "view": map[string]string{"view_policy": "spectator_discard_only@1"}})
				if human {
					frame = jsonBytes(map[string]string{"type": "control_granted"})
				}
				if c.Write(ctx, websocket.MessageText, frame) != nil {
					return
				}
				_, _, _ = c.Read(ctx)
			}))
			defer server.Close()
			metrics := &capacityMetrics{commandErrors: map[string]int{}}
			target := &capacityTarget{room: Room{ID: "old"}, change: make(chan struct{})}
			workerDone := make(chan struct{})
			go func() {
				defer close(workerDone)
				if human {
					capacityPlayer(ctx, target, capacityUser{}, server.Client(), server.URL, metrics)
				} else {
					capacitySpectator(ctx, target, server.Client(), server.URL, metrics)
				}
			}()
			population := &metrics.spectators
			minimum := &metrics.minimumSpectators
			if human {
				population, minimum = &metrics.humans, &metrics.minimumHumans
			}
			wait := func(predicate func() bool) {
				t.Helper()
				for !predicate() {
					select {
					case <-ctx.Done():
						t.Fatal("driver handover timed out")
					case <-time.After(time.Millisecond):
					}
				}
			}
			wait(func() bool { return population.Load() == 1 })
			minimum.Store(1)
			metrics.measure.Store(true)
			target.replace(Room{ID: "new"})
			select {
			case <-newArrived:
			case <-ctx.Done():
				t.Fatal("new connection never arrived")
			}
			select {
			case <-oldClosed:
				t.Fatal("old connection closed before replacement became ready")
			default:
			}
			if population.Load() != 1 {
				t.Fatal("live population changed during replacement handshake")
			}
			close(allowNew)
			select {
			case <-oldClosed:
			case <-ctx.Done():
				t.Fatal("old connection was not retired")
			}
			wait(func() bool { return population.Load() == 1 })
			if minimum.Load() != 1 {
				t.Fatal("rematch created a live population dip")
			}
			metrics.mu.Lock()
			failures := len(metrics.errors)
			metrics.mu.Unlock()
			if failures != 0 {
				t.Fatal("handover reported protocol failure")
			}
			metrics.measure.Store(false)
			cancel()
			<-workerDone
		})
	}
}
