package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"testing"

	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func TestRegisteredRulePinsLoadedExecutableAndKeepsSourceIdentity(t *testing.T) {
	rule := mcr.New()
	original := rule.Manifest()
	r := New()
	if err := r.Register(rule); err != nil {
		t.Fatal(err)
	}
	bound, ok := r.Get(original.ID, original.Version)
	if !ok {
		t.Fatal("missing registered rule")
	}
	m := bound.Manifest()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(body)
	if m.ArtifactHash != "sha256:"+hex.EncodeToString(expected[:]) || m.ArtifactHash == original.ArtifactHash {
		t.Fatal("registered rule is not pinned to the actual executable")
	}
	if m.SourceHash != original.ArtifactHash || m.Build == nil || m.Build.GoVersion != runtime.Version() || m.Build.Architecture != runtime.GOARCH {
		t.Fatal("source/build identity lost during artifact binding")
	}
	if rule.Manifest().ArtifactHash != original.ArtifactHash {
		t.Fatal("binding mutated pure plugin")
	}
	if len(r.RuleMap()) != 1 || r.List()[0].ArtifactHash != m.ArtifactHash {
		t.Fatal("catalog and host disagree")
	}
}

// Embedding only Rule models existing plugins without the optional capability.
type individualProjectionRule struct{ rulesdk.Rule }

func TestArtifactBindingPreservesOptionalBatchProjection(t *testing.T) {
	original := mcr.New()
	for _, test := range []struct {
		name string
		rule rulesdk.Rule
		want bool
	}{
		{"batch", original, true},
		{"individual", individualProjectionRule{original}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := New()
			if err := registry.Register(test.rule); err != nil {
				t.Fatal(err)
			}
			manifest := original.Manifest()
			bound, ok := registry.Get(manifest.ID, manifest.Version)
			if !ok {
				t.Fatal("registered rule not found")
			}
			batch, ok := bound.(rulesdk.BatchProjector)
			if ok != test.want {
				t.Fatalf("batch capability = %v, want %v", ok, test.want)
			}
			if !ok {
				return
			}
			cfg := rulesdk.Config{Format: "practice_1", Profile: "om-mcr-1", Participants: []rulesdk.Participant{
				{ID: "A", Kind: "human"}, {ID: "B", Kind: "bot"}, {ID: "C", Kind: "bot"}, {ID: "D", Kind: "bot"},
			}}
			raw, err := bound.Init(cfg, bytes.Repeat([]byte{7}, 32))
			if err != nil {
				t.Fatal(err)
			}
			viewer := rulesdk.Viewer{Audience: rulesdk.SpectatorDiscardOnly}
			views, err := batch.ProjectMany(raw, []rulesdk.Viewer{viewer})
			if err != nil || len(views) != 1 {
				t.Fatalf("batch forwarding failed: %v", err)
			}
			want, err := original.Project(raw, viewer)
			if err != nil || !bytes.Equal(views[0], want) {
				t.Fatalf("artifact binding changed projection: %v", err)
			}
		})
	}
}
