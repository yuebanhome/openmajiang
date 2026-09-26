package platform

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func publicSocketEntry(interrupted bool) publicCacheEntry {
	body := jsonBytes(map[string]any{
		"type": "spectator_snapshot", "match_id": "match", "hand_id": "hand", "seq": 19,
		"status": "active", "room": map[string]any{"id": "room", "seats": []any{}},
		"platform_interrupted": interrupted, "view_policy": "spectator_discard_only@1",
		"view": map[string]any{"view_policy": "spectator_discard_only@1", "discards": []any{}},
	})
	return publicCacheEntry{body: body, expires: time.Now().Add(time.Minute), digest: sha256.Sum256(body)}
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
	first.CloseNow()
	second.CloseNow()
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
}
