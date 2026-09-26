package platform

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicCacheGivesEveryRecipientAnIndependentObject(t *testing.T) {
	s := &Service{publicSnapshots: map[string]publicCacheEntry{"spectator_discard_only@1/room": {body: []byte(`{"seq":19,"view":{"view_policy":"spectator_discard_only@1","discards":[{"discard_id":"d1","kind":"1m","from_seat":0,"claimed":false}]}}`), expires: time.Now().Add(time.Minute)}}}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.cachedPublicSnapshot(httptest.NewRequest("GET", "/", nil), "room")
			if e != nil {
				t.Error(e)
				return
			}
			if v["seq"] != int64(19) {
				t.Error("lost integer sequence")
			}
			if _, ok := v["stream_id"]; ok {
				t.Error("cross-recipient stream leak")
			}
			v["stream_id"] = "recipient"
			delete(v, "seq")
			view := v["view"].(map[string]any)
			view["discards"] = nil
		}()
	}
	wg.Wait()
	v, e := s.cachedPublicSnapshot(httptest.NewRequest("GET", "/", nil), "room")
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(v)
	if !strings.Contains(string(encoded), "1m") || strings.Contains(string(encoded), "recipient") {
		t.Fatal("cached data mutated by consumer")
	}
	s.invalidatePublicSnapshots()
	if len(s.publicSnapshots) != 0 {
		t.Fatal("cache was not invalidated")
	}
}

func TestDeletedAccountAnonymizationPreservesOtherParticipantsAndVisibility(t *testing.T) {
	raw := json.RawMessage(`{"view_policy":"participant_private@1","hand":[{"kind":"8s","tile_id":"own-tile"}],"seats":[{"participant_id":"deleted","name":"Old Personal Name","melds":[]},{"participant_id":"other","name":"Opponent"}]}`)
	redacted, e := anonymizeView(raw, map[string]bool{"deleted": true})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(redacted), "Old Personal Name") || !strings.Contains(string(redacted), "已注销用户") || !strings.Contains(string(redacted), "Opponent") || !strings.Contains(string(redacted), "own-tile") {
		t.Fatal("incorrect anonymization")
	}
	if !strings.Contains(string(raw), "Old Personal Name") {
		t.Fatal("persisted input mutated")
	}
}
