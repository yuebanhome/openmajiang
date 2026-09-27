package apidocs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yuebanhome/openmajiang/api"
	"github.com/yuebanhome/openmajiang/internal/platform"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestPublishedContracts(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)
	for _, path := range []string{"/openapi.json", "/schemas/ws-server.json", "/schemas/ws-client.json", "/schemas/spectator.json", "/schemas/dto.json", "/schemas/protocol.json", "/llms.txt", "/llms-full.txt"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if strings.HasSuffix(path, ".json") && !json.Valid(w.Body.Bytes()) {
			t.Fatalf("invalid JSON %s", path)
		}
		r.Header.Set("If-None-Match", w.Header().Get("ETag"))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 304 {
			t.Fatalf("cache validator %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/schemas/state.json", nil))
	if w.Code != 404 {
		t.Fatal("unknown schemas must not expose files")
	}
}

func TestEveryActualHTTPRouteHasContract(t *testing.T) {
	b, _ := api.Files.ReadFile("openapi.json")
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`\.HandleFunc\("([A-Z]+) ([^" ]+)"`)
	for _, folder := range []string{"../auth", "../platform"} {
		files, err := filepath.Glob(folder + "/*.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range route.FindAllStringSubmatch(string(source), -1) {
				if len(spec.Paths[match[2]][strings.ToLower(match[1])]) == 0 {
					t.Errorf("missing %s %s from %s", match[1], match[2], file)
				}
			}
		}
	}
}

func TestSharedSDKFixtureUsesActualGoDTOs(t *testing.T) {
	b, err := api.Files.ReadFile("fixtures/decision.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Snapshot struct {
			MatchID       string              `json:"match_id"`
			HandID        string              `json:"hand_id"`
			ParticipantID string              `json:"participant_id"`
			Epoch         int64               `json:"control_epoch"`
			Stream        string              `json:"stream_id"`
			Seq           int                 `json:"view_seq"`
			View          mcr.ParticipantView `json:"view"`
		} `json:"snapshot"`
		Decision struct {
			platform.Action
			Observation struct {
				Stream string `json:"stream_id"`
				Seq    int    `json:"view_seq"`
			} `json:"observation_ref"`
		} `json:"decision"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	s, d := fixture.Snapshot, fixture.Decision
	if s.MatchID != d.MatchID || s.HandID != d.HandID || s.ParticipantID != d.ParticipantID || s.Epoch != d.Epoch || s.Stream != d.Observation.Stream || s.Seq != d.Observation.Seq {
		t.Fatal("snapshot barrier identity mismatch")
	}
	if len(s.View.Hand) != 14 || s.View.ParticipantID != s.ParticipantID || s.View.Policy != "participant_private@1" {
		t.Fatal("fixture does not match actual private DTO")
	}
	public, err := api.Files.ReadFile("fixtures/spectator.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		View mcr.SpectatorView `json:"view"`
	}
	if err = json.Unmarshal(public, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.View.Policy != "spectator_discard_only@1" || len(frame.View.Discards) != 1 {
		t.Fatal("public fixture missing discarded face")
	}
}
