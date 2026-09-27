package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func publicSocketEntry(interrupted bool) publicCacheEntry {
	entry, err := newPublicCacheEntry(map[string]any{
		"type": "spectator_snapshot", "match_id": "match", "hand_id": "hand", "seq": 19,
		"status": "active", "room": map[string]any{"id": "room", "seats": []any{}},
		"platform_interrupted": interrupted, "view_policy": "spectator_discard_only@1",
		"view": map[string]any{"view_policy": "spectator_discard_only@1", "discards": []any{}},
	}, time.Now().Add(time.Minute))
	if err != nil {
		panic(err)
	}
	return entry
}

func TestPublicSocketCacheIsolatesStreamsAndReportsSameSequenceChange(t *testing.T) {
	s := &Service{publicSnapshots: map[string]publicCacheEntry{"spectator_discard_only@1/room": publicSocketEntry(false)}, limits: map[string]rateEntry{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		s.socketLoop(r.Context(), c, r, "room", "", "", true, nil)
	}))
	defer server.Close()
	read := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_, raw, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err = json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if _, present := frame["seq"]; present {
			t.Fatal("database sequence leaked into socket envelope")
		}
		return frame
	}
	dial := func() *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	first, second := dial(), dial()
	a, b := read(first), read(second)
	if a["stream_id"] == b["stream_id"] || a["stream_id"] == "" || a["view_seq"] != float64(1) || b["view_seq"] != float64(1) {
		t.Fatalf("recipient streams were shared: %v %v", a, b)
	}
	// A recovery may change the deadline/interruption metadata without a new
	// rule event. The public digest must detect this even with the same seq.
	s.publicMu.Lock()
	s.publicSnapshots["spectator_discard_only@1/room"] = publicSocketEntry(true)
	s.publicMu.Unlock()
	after := read(first)
	if after["stream_id"] != a["stream_id"] || after["view_seq"] != float64(2) || after["platform_interrupted"] != true {
		t.Fatalf("same-sequence public change was lost: %v", after)
	}
	s.publicMu.Lock()
	body := append([]byte(nil), s.publicSnapshots["spectator_discard_only@1/room"].body...)
	s.publicMu.Unlock()
	var cached map[string]any
	if err := json.Unmarshal(body, &cached); err != nil {
		t.Fatal(err)
	}
	if cached["seq"] != float64(19) || cached["stream_id"] != nil || cached["view_seq"] != nil {
		t.Fatal("recipient mutation changed the shared public cache")
	}
	// A forced resume reuses this connection's stream, then a new hand gets
	// an independent stream starting at sequence one.
	if err := first.Write(ctx, websocket.MessageText, []byte(`{"type":"resume"}`)); err != nil {
		t.Fatal(err)
	}
	resumed := read(first)
	if resumed["stream_id"] != a["stream_id"] || resumed["view_seq"] != float64(3) {
		t.Fatalf("resume changed stream semantics: %v", resumed)
	}
	cached["hand_id"] = "next-hand"
	entry, err := newPublicCacheEntry(cached, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.publicMu.Lock()
	s.publicSnapshots["spectator_discard_only@1/room"] = entry
	s.publicMu.Unlock()
	next := read(first)
	if next["stream_id"] == a["stream_id"] || next["view_seq"] != float64(1) || next["hand_id"] != "next-hand" {
		t.Fatalf("hand transition retained old stream: %v", next)
	}
	first.CloseNow()
	second.CloseNow()
}

func TestPublicSocketFrameMatchesIndependentEnvelope(t *testing.T) {
	for _, view := range []map[string]any{
		{},
		{"type": "room_snapshot", "room": map[string]any{"name": "引号\"与中文"}, "view": nil},
		{"type": "spectator_snapshot", "room": Room{ID: "room", Name: "生产房间", Seats: []Seat{}}, "seq": int64(23), "view": json.RawMessage(`{"view_policy":"spectator_discard_only@1","seats":[{"participant_id":"deleted","name":"已注销用户"}],"discards":[]}`)},
		{"type": "spectator_snapshot", "seq": int64(9007199254740993), "match_id": "match", "hand_id": "hand", "view": map[string]any{"discards": []any{map[string]any{"kind": "1m"}}}, "decision": "excluded", "stream_id": "excluded", "view_seq": -1},
	} {
		entry, err := newPublicCacheEntry(view, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		original := string(entry.body)
		for _, stream := range []string{"stream_A", "quote\"\\\n中文"} {
			frame := entry.socketFrame(stream, 9007199254740993)
			got, err := decodePublicSnapshot(frame)
			if err != nil {
				t.Fatal(err)
			}
			want, err := decodePublicSnapshot(entry.body)
			if err != nil {
				t.Fatal(err)
			}
			delete(want, "seq")
			delete(want, "decision")
			want["stream_id"], want["view_seq"] = stream, int64(9007199254740993)
			if string(jsonBytes(got)) != string(jsonBytes(want)) {
				t.Fatalf("frame differs from independently encoded envelope: %s", frame)
			}
			frame[0] = '!'
			if string(entry.body) != original || !json.Valid(entry.socketFrame(stream, 2)) {
				t.Fatal("recipient frame mutated shared bytes")
			}
		}
	}
}

func BenchmarkPublicCachePolling(b *testing.B) {
	s := &Service{publicSnapshots: map[string]publicCacheEntry{"spectator_discard_only@1/room": publicSocketEntry(false)}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	b.Run("unchanged_entry", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.cachedPublicEntry(r, "room"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode_for_recipient", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.cachedPublicSnapshot(r, "room"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("prepared_recipient_frame", func(b *testing.B) {
		entry := publicSocketEntry(false)
		b.ReportAllocs()
		for b.Loop() {
			_ = entry.socketFrame("stream_A", 1)
		}
	})
	b.Run("legacy_recipient_frame", func(b *testing.B) {
		entry := publicSocketEntry(false)
		b.ReportAllocs()
		for b.Loop() {
			view, err := decodePublicSnapshot(entry.body)
			if err != nil {
				b.Fatal(err)
			}
			delete(view, "seq")
			delete(view, "decision")
			view["stream_id"], view["view_seq"] = "stream_A", int64(1)
			_ = jsonBytes(view)
		}
	})
}
