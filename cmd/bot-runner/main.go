// bot-runner is a self-hosted baseline client. Credentials are read from the
// environment, never command-line arguments or URL parameters.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/yuebanhome/openmajiang/internal/bots"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

type config struct {
	server, room, format, strategy, seed string
	queue, continuous, local             bool
	hands                                int
}
type client struct {
	cfg        config
	http       *http.Client
	key, token string
	pending    map[string]command
}
type command struct {
	Type        string `json:"type"`
	Protocol    string `json:"protocol_version"`
	Match       string `json:"match_id"`
	Hand        string `json:"hand_id"`
	Participant string `json:"participant_id"`
	Seat        int    `json:"seat_id"`
	Assignment  int    `json:"seat_assignment_version"`
	Epoch       int64  `json:"control_epoch"`
	Decision    string `json:"decision_id"`
	Window      string `json:"window_id"`
	Command     string `json:"command_id"`
	Option      string `json:"option_id"`
}
type envelope struct {
	Type        string          `json:"type"`
	View        json.RawMessage `json:"view"`
	Match       string          `json:"match_id"`
	Hand        string          `json:"hand_id"`
	Participant string          `json:"participant_id"`
	Seat        int             `json:"seat_id"`
	Assignment  int             `json:"seat_assignment_version"`
	Epoch       int64           `json:"control_epoch"`
	Decision    string          `json:"decision_id"`
	Window      string          `json:"window_id"`
	Command     string          `json:"command_id"`
	Stream      string          `json:"stream_id"`
	Seq         int64           `json:"view_seq"`
	Ref         struct {
		Stream string `json:"stream_id"`
		Seq    int64  `json:"view_seq"`
	} `json:"observation_ref"`
	ServerTime time.Time        `json:"server_time"`
	Deadline   time.Time        `json:"deadline_at"`
	Options    []rulesdk.Option `json:"legal_actions"`
	Recorded   *struct {
		Command  string `json:"command_id"`
		Decision string `json:"decision_id"`
	} `json:"recorded"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func main() {
	var c config
	flag.StringVar(&c.server, "server", "http://127.0.0.1:8080", "Platform HTTPS base URL (HTTP only on loopback)")
	flag.StringVar(&c.room, "room", "", "Invited room already joined by this bot")
	flag.StringVar(&c.format, "format", "standard_16", "Match format")
	flag.StringVar(&c.strategy, "strategy", "basic_heuristic", "basic_heuristic or random_legal")
	flag.BoolVar(&c.queue, "queue", false, "Join the public bot queue")
	flag.BoolVar(&c.continuous, "continuous", false, "Explicitly opt in to continuous matching")
	flag.BoolVar(&c.local, "local", false, "Run deterministic local rules without credentials")
	flag.IntVar(&c.hands, "hands", 16, "Local number of hands (a positive multiple of 16)")
	flag.StringVar(&c.seed, "seed", "openmajiang-local", "Reproducible local-only wall seed")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if c.local {
		err = runLocal(ctx, c)
	} else {
		err = runOnline(ctx, c)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func runOnline(ctx context.Context, cfg config) error {
	u, e := url.Parse(cfg.server)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid server URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return errors.New("server requires HTTPS except loopback development")
	}
	cfg.server = strings.TrimRight(cfg.server, "/")
	key := os.Getenv("OPENMAJIANG_BOT_KEY")
	if key == "" {
		return errors.New("set OPENMAJIANG_BOT_KEY")
	}
	c := client{cfg: cfg, key: key, pending: map[string]command{}, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	failures := 0
	for ctx.Err() == nil {
		e = c.session(ctx)
		if e == nil && cfg.queue {
			e = c.request(ctx, http.MethodPost, "/v1/bot/queue", map[string]any{"ruleset_id": "openmajiang.mcr", "ruleset_version": "1.0.0", "match_format": cfg.format, "continuous": cfg.continuous}, nil, c.token)
		}
		if e == nil {
			e = c.connect(ctx)
		}
		if ctx.Err() != nil {
			break
		}
		failures++
		delay := time.Second * time.Duration(1<<min(failures, 5))
		log.Printf("Connection ended; retry in %s", delay)
		if !sleep(ctx, delay) {
			break
		}
	}
	return ctx.Err()
}
func (c *client) session(ctx context.Context) error {
	var reply struct {
		Token string `json:"session_token"`
	}
	if e := c.request(ctx, http.MethodPost, "/v1/bot-sessions", map[string]any{"protocol_version": "1.0", "rulesets": []map[string]string{{"id": "openmajiang.mcr", "version": "1.0.0"}}}, &reply, c.key); e != nil {
		return e
	}
	if reply.Token == "" {
		return errors.New("missing session token")
	}
	c.token = reply.Token
	return nil
}
func (c *client) request(ctx context.Context, method, path string, body, out any, token string) error {
	var input io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		input = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, c.cfg.server+path, input)
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, e := c.http.Do(r)
	if e != nil {
		return errors.New("platform request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("platform HTTP %d", response.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (c *client) connect(ctx context.Context) error {
	room := c.cfg.room
	for room == "" {
		var active struct {
			Room *struct {
				ID string `json:"id"`
			} `json:"room"`
			RoomID string `json:"room_id"`
		}
		if e := c.request(ctx, http.MethodGet, "/v1/bot/active-match", nil, &active, c.token); e != nil {
			return e
		}
		room = active.RoomID
		if active.Room != nil {
			room = active.Room.ID
		}
		if room == "" && !sleep(ctx, time.Second) {
			return ctx.Err()
		}
	}
	u, _ := url.Parse(c.cfg.server)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/v1/ws/bots"
	q := u.Query()
	q.Set("room_id", room)
	u.RawQuery = q.Encode()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	conn, _, e := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: h})
	if e != nil {
		return errors.New("bot websocket connection failed")
	}
	defer conn.Close(websocket.StatusNormalClosure, "runner stopped")
	conn.SetReadLimit(1 << 20)
	send := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		out, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(out, websocket.MessageText, b)
	}
	var snapshot envelope
	var generation int64
	for {
		read, cancel := context.WithTimeout(ctx, 35*time.Second)
		_, raw, e := conn.Read(read)
		cancel()
		if e != nil {
			return e
		}
		var msg envelope
		if json.Unmarshal(raw, &msg) != nil {
			return errors.New("invalid protocol frame")
		}
		switch msg.Type {
		case "heartbeat":
			if e = send(map[string]string{"type": "ping"}); e != nil {
				return e
			}
		case "control_changed":
			return errors.New("bot control taken over")
		case "snapshot":
			if msg.Stream == snapshot.Stream && msg.Seq <= generation {
				continue
			}
			snapshot = msg
			generation = msg.Seq
			if msg.Recorded != nil {
				delete(c.pending, msg.Recorded.Decision)
			}
			for _, pending := range c.pending {
				if pending.Match == msg.Match && pending.Hand == msg.Hand {
					if e = send(pending); e != nil {
						return e
					}
				}
			}
		case "decision_request":
			if msg.Stream != snapshot.Stream || msg.Seq <= generation || msg.Ref.Stream != snapshot.Stream || msg.Ref.Seq != snapshot.Seq || msg.Match != snapshot.Match || msg.Hand != snapshot.Hand || msg.Participant != snapshot.Participant || msg.Epoch != snapshot.Epoch {
				if e = send(map[string]string{"type": "resume"}); e != nil {
					return e
				}
				continue
			}
			generation = msg.Seq
			if old, ok := c.pending[msg.Decision]; ok {
				if e = send(old); e != nil {
					return e
				}
				continue
			}
			budget := msg.Deadline.Sub(msg.ServerTime) - 100*time.Millisecond
			if budget <= 0 {
				continue
			}
			started := time.Now()
			choice, e := bots.Choose(c.cfg.strategy, snapshot.View, msg.Options)
			if e != nil {
				return e
			}
			if time.Since(started) >= budget {
				continue
			}
			a := command{Type: "submit_action", Protocol: "1.0", Match: msg.Match, Hand: msg.Hand, Participant: msg.Participant, Seat: msg.Seat, Assignment: msg.Assignment, Epoch: msg.Epoch, Decision: msg.Decision, Window: msg.Window, Command: commandID(), Option: choice}
			c.pending[msg.Decision] = a
			if e = send(a); e != nil {
				return e
			}
		case "command_ack":
			for id, a := range c.pending {
				if a.Command == msg.Command {
					delete(c.pending, id)
				}
			}
		case "command_error":
			for id, a := range c.pending {
				if a.Command == msg.Command {
					delete(c.pending, id)
				}
			}
			if e = send(map[string]string{"type": "resume"}); e != nil {
				return e
			}
		}
	}
}
func commandID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return "cmd_" + hex.EncodeToString(b)
}
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func runLocal(ctx context.Context, c config) error {
	if c.hands < 1 || c.hands%16 != 0 {
		return errors.New("local hands must be a positive multiple of 16")
	}
	rule := mcr.New()
	participants := []rulesdk.Participant{}
	for i := 0; i < 4; i++ {
		participants = append(participants, rulesdk.Participant{ID: fmt.Sprintf("local-%d", i), Name: fmt.Sprintf("Bot %d", i+1), Kind: "builtin"})
	}
	for game := 0; game < c.hands/16; game++ {
		entropy := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", c.seed, game)))
		state, e := rule.Init(rulesdk.Config{Format: "standard_16", Profile: "om-mcr-1", Participants: participants}, entropy[:])
		if e != nil {
			return e
		}
		for step := 0; step < 20000; step++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			flow, e := rule.Inspect(state)
			if e != nil {
				return e
			}
			if flow.MatchEnded {
				log.Printf("local match %d complete (16 hands)", game+1)
				break
			}
			in := rulesdk.Input{}
			switch flow.WindowKind {
			case "intermission":
				in.Type = "next_hand"
			case "reaction":
				in.Type = "resolve"
				in.Choices = map[string]string{}
				for _, d := range flow.Decisions {
					view, e := rule.Project(state, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: d.ParticipantID})
					if e != nil {
						return e
					}
					choice, e := bots.Choose(c.strategy, view, d.Options)
					if e != nil {
						return e
					}
					in.Choices[d.ParticipantID] = choice
				}
			case "self":
				if len(flow.Decisions) != 1 {
					return errors.New("missing self decision")
				}
				d := flow.Decisions[0]
				view, e := rule.Project(state, rulesdk.Viewer{Audience: rulesdk.ParticipantPrivate, ParticipantID: d.ParticipantID})
				if e != nil {
					return e
				}
				choice, e := bots.Choose(c.strategy, view, d.Options)
				if e != nil {
					return e
				}
				in = rulesdk.Input{Type: "action", ParticipantID: d.ParticipantID, OptionID: choice}
			default:
				return errors.New("unsupported local phase")
			}
			transition, e := rule.Apply(state, in)
			if e != nil {
				return e
			}
			state = transition.State
			if step == 19999 {
				return errors.New("local match did not terminate")
			}
		}
	}
	return nil
}
